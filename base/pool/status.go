package pool

import (
	"time"

	"loomproxy/db"
	"loomproxy/models"
)

// ConfigInfo 号池运行参数快照
type ConfigInfo struct {
	ColdSpares     int `json:"cold_spares"`
	MaxHot         int `json:"max_hot"`
	MaxDead        int `json:"max_dead"`
	MaxDevices     int `json:"max_devices"` // 0=不限
	Kind           string `json:"kind"`      // 池形态；空即 pool.KindBurnWallClock
	RenewBeforeSec int `json:"renew_before_sec"`
	MaintainSec    int `json:"maintain_sec"`
}

// DeviceInfo 号状态快照（标识与凭证已脱敏——它们是上游签名凭证，不得完整外发）
type DeviceInfo struct {
	Ident      string     `json:"ident"`
	Creds      []string   `json:"creds,omitempty"` // 凭证字段名（只列键名，不列值）
	Status     string     `json:"status"`
	TotalQuota int        `json:"total_quota"`
	UsedQuota  int        `json:"used_quota"`
	ExpireAt   *time.Time `json:"expire_at"`
}

// Status 号池状态快照
type Status struct {
	Name    string           `json:"name"`
	Running bool             `json:"running"`
	Config  ConfigInfo       `json:"config"`
	Counts  map[string]int64 `json:"counts"`
	Devices []DeviceInfo     `json:"devices"`
}

// deviceStatusLimit 非活跃号明细的展示上限（防死号刷屏）
const deviceStatusLimit = 50

// Status 返回本池快照：运行中取内存活跃/冷备 + 库中 spent/dead 计数；
// 未启动（号池开关关闭或尚未初始化）时仅返回库中记录，Running=false
func (p *Pool) Status() *Status {
	st := &Status{
		Name:    p.Name(),
		Running: p.Running(),
		Config:  p.configInfo(),
		Counts:  map[string]int64{StatusHot: 0, StatusCold: 0, StatusSpent: 0, StatusDead: 0},
		Devices: make([]DeviceInfo, 0),
	}
	if db.DB == nil {
		return st
	}

	if !p.Running() {
		fillCountsFromDB(st, p.Name())
		st.Devices = append(st.Devices, listDevices(p.Name(), deviceStatusLimit)...)
		return st
	}

	p.mu.Lock()
	st.Counts[StatusHot] = int64(len(p.hot))
	st.Counts[StatusCold] = int64(len(p.cold))
	for _, d := range p.hot {
		st.Devices = append(st.Devices, snapshot(d))
	}
	for _, d := range p.cold {
		st.Devices = append(st.Devices, snapshot(d))
	}
	p.mu.Unlock()

	// spent/dead 不驻留内存，从库中补充
	st.Counts[StatusSpent] = countByStatus(p.Name(), StatusSpent)
	st.Counts[StatusDead] = countByStatus(p.Name(), StatusDead)
	st.Devices = append(st.Devices, listDevices(p.Name(), deviceStatusLimit, StatusSpent, StatusDead)...)
	return st
}

// configInfo 运行参数快照：不取锁也不碰库（构造期就定了）。未启动的池也要带出 config——
// 面板读到全零 config 会被误读成「没配上限」
func (p *Pool) configInfo() ConfigInfo {
	return ConfigInfo{
		ColdSpares:     p.cfg.ColdSpares,
		MaxHot:         p.cfg.MaxHot,
		MaxDead:        p.cfg.MaxDead,
		MaxDevices:     p.cfg.MaxDevices,
		Kind:           p.cfg.Kind,
		RenewBeforeSec: int(p.cfg.RenewBefore.Seconds()),
		MaintainSec:    int(p.cfg.Interval.Seconds()),
	}
}

func fillCountsFromDB(st *Status, name string) {
	type row struct {
		Status string
		N      int64
	}
	var rows []row
	db.DB.Model(&models.PoolDevice{}).
		Where("pool = ?", name).
		Select("status, COUNT(*) AS n").Group("status").Scan(&rows)
	for _, r := range rows {
		st.Counts[r.Status] = r.N
	}
}

func countByStatus(name, status string) int64 {
	var n int64
	db.DB.Model(&models.PoolDevice{}).Where("pool = ? AND status = ?", name, status).Count(&n)
	return n
}

// listDevices 按状态列明细（statuses 为空 = 不限状态），标识脱敏
func listDevices(name string, limit int, statuses ...string) []DeviceInfo {
	q := db.DB.Where("pool = ?", name)
	if len(statuses) > 0 {
		q = q.Where("status IN ?", statuses)
	}
	var rows []models.PoolDevice
	q.Order("updated_at DESC").Limit(limit).Find(&rows)
	out := make([]DeviceInfo, 0, len(rows))
	for i := range rows {
		out = append(out, snapshot(&rows[i]))
	}
	return out
}

func snapshot(d *models.PoolDevice) DeviceInfo {
	creds := make([]string, 0)
	for k := range decodeAttrs(d.Attrs) {
		creds = append(creds, k)
	}
	return DeviceInfo{
		Ident:      maskIdent(d.Ident),
		Creds:      creds,
		Status:     d.Status,
		TotalQuota: d.TotalQuota,
		UsedQuota:  d.UsedQuota,
		ExpireAt:   d.ExpireAt,
	}
}

// maskIdent 标识脱敏：保留前 8 后 4
func maskIdent(s string) string {
	if len(s) <= 12 {
		return "***"
	}
	return s[:8] + "***" + s[len(s)-4:]
}
