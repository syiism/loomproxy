package admin

// 多设备与密钥的跨用户读数（待办清单 P44）。
//
// 现有数据就够，零 DDL：`auth_sessions` 每行带 device/ip/created_at/last_active_at/revoked_at，
// `api_call_logs` 带 username/ip（**用户密钥的调用也归属到用户名下**，见 utils/auth.go 的密钥分支），
// `api_keys` 带 created_at/last_used_at。所以「一个账号近期从几个 IP 登录、几个 IP 调用、几把 key」算得出；
// 「具体哪把 key 在被别人用」算不出——这就是密钥侧只监控不自动冻结的原因。
//
// 读数与判定分开看：这里的 IP 数只用于**标红**，处置（移出多余会话）在 handlers/auth/device_watch.go，
// 且只按会话数走。

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"loomproxy/db"
	"loomproxy/handlers/auth"
	"loomproxy/models"
)

// DeviceActivityRow 一名用户的设备/密钥分布
type DeviceActivityRow struct {
	UserID         uint       `json:"user_id"`
	Username       string     `json:"username"`
	Nickname       string     `json:"nickname"`
	PlanName       string     `json:"plan_name"` // 默认名：运营读数不被别名修饰（P43 的同一条分界线）
	LastLoginAt    *time.Time `json:"last_login_at"`
	ActiveSessions int64      `json:"active_sessions"`
	LoginDevices   int64      `json:"login_devices"`  // 窗口内出现过的不同设备（UA 解析值）
	LoginIPs       int64      `json:"login_ips"`      // 窗口内登录来源的不同 IP
	CallIPs        int64      `json:"call_ips"`       // 窗口内调用来源的不同 IP（含密钥调用）
	ApiKeys        int64      `json:"api_keys"`       // 在册密钥数
	RevokedRecent  int64      `json:"revoked_recent"` // 窗口内被移出的会话数（自动处置或用户自点，库里不分，见 device_watch.go）
	Suspect        bool       `json:"suspect"`
}

// ListDeviceActivity GET /admin/devices
func ListDeviceActivity(c *gin.Context) {
	// 分页参数与用户列表同形状（各自解析，不为两处复制再抽一层）
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	keyword := strings.TrimSpace(c.Query("keyword"))
	windowStart := auth.WatchWindowStart()

	q := db.DB.Model(&models.User{})
	if keyword != "" {
		like := "%" + escapeLike(keyword) + "%"
		q = q.Where(likeESCAPE("username")+" OR "+likeESCAPE("email")+" OR "+likeESCAPE("nickname"), like, like, like)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}

	var users []models.User
	if err := q.Preload("Plan").Order("id DESC").
		Offset((page - 1) * pageSize).Limit(pageSize).Find(&users).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}

	ids := make([]uint, 0, len(users))
	names := make([]string, 0, len(users))
	for i := range users {
		ids = append(ids, users[i].ID)
		names = append(names, users[i].Username)
	}

	sessions := loadSessionStats(ids, windowStart)
	callIPs := loadCallIPCounts(names, windowStart)
	keyCounts := loadApiKeyCounts(ids)

	suspectIPs := auth.SuspectDistinctIPs()
	rows := make([]DeviceActivityRow, 0, len(users))
	for i := range users {
		u := users[i]
		st := sessions[u.ID]
		row := DeviceActivityRow{
			UserID:         u.ID,
			Username:       u.Username,
			Nickname:       u.Nickname,
			LastLoginAt:    u.LastLoginAt,
			ActiveSessions: st.active,
			LoginDevices:   st.devices,
			LoginIPs:       st.loginIPs,
			CallIPs:        callIPs[u.Username],
			ApiKeys:        keyCounts[u.ID],
			RevokedRecent:  st.revoked,
		}
		if u.Plan != nil {
			row.PlanName = u.Plan.Name
		}
		// 标红只看 IP：会话数超上限在登录时已经被处置掉了，这里看到的多半是「开关还没开」
		row.Suspect = row.LoginIPs >= int64(suspectIPs) || row.CallIPs >= int64(suspectIPs)
		rows = append(rows, row)
	}

	auth.Ok(c, gin.H{
		"list":  rows,
		"total": total,
		// 口径随读数一起下发：面板上「8 个 IP」是不是异常，取决于当时配置的阈值是多少
		// 口径随读数一起下发：面板上「8 个 IP」算不算异常，取决于当时配的阈值是多少
		"config": gin.H{
			"device_watch_enabled": db.GetSetting("device_watch_enabled") == "true",
			"max_active_sessions":  auth.MaxActiveSessions(),
			"window_days":          daysBetween(windowStart),
			"suspect_distinct_ips": suspectIPs,
			"window_start":         windowStart,
		},
	})
}

type sessionStats struct {
	active   int64
	devices  int64
	loginIPs int64
	revoked  int64
}

// loadSessionStats 窗口内每个用户的会话分布（一次取回、在内存里算 distinct）
func loadSessionStats(ids []uint, windowStart time.Time) map[uint]sessionStats {
	out := make(map[uint]sessionStats, len(ids))
	if len(ids) == 0 {
		return out
	}
	type row struct {
		UserID       uint
		Device       string
		IP           string
		RevokedAt    *time.Time
		ExpiresAt    time.Time
		LastActiveAt time.Time
	}
	var rows []row
	// 窗口口径：这段时间**建立**的会话（借号是换人登录，不是同一个人刷活跃，
	// 用 created_at 比 last_active_at 更贴近要抓的形状）
	if err := db.DB.Model(&models.AuthSession{}).
		Select("user_id, device, ip, revoked_at, expires_at, last_active_at").
		Where("user_id IN ? AND created_at >= ?", ids, windowStart).
		Scan(&rows).Error; err != nil {
		return out
	}
	now := time.Now()
	devSeen := map[uint]map[string]bool{}
	ipSeen := map[uint]map[string]bool{}
	for _, r := range rows {
		s := out[r.UserID]
		if r.RevokedAt != nil {
			s.revoked++
		} else if r.ExpiresAt.After(now) {
			s.active++
		}
		if devSeen[r.UserID] == nil {
			devSeen[r.UserID] = map[string]bool{}
			ipSeen[r.UserID] = map[string]bool{}
		}
		if r.Device != "" {
			devSeen[r.UserID][r.Device] = true
		}
		if r.IP != "" {
			ipSeen[r.UserID][r.IP] = true
		}
		out[r.UserID] = s
	}
	for id, s := range out {
		s.devices = int64(len(devSeen[id]))
		s.loginIPs = int64(len(ipSeen[id]))
		out[id] = s
	}
	return out
}

// loadCallIPCounts 窗口内每个用户名的不同调用来源 IP 数（密钥调用也算在内）
func loadCallIPCounts(names []string, windowStart time.Time) map[string]int64 {
	out := map[string]int64{}
	if len(names) == 0 {
		return out
	}
	var rows []struct {
		Username string
		N        int64
	}
	if err := db.DB.Model(&models.ApiCallLog{}).
		Select("username, COUNT(DISTINCT ip) AS n").
		Where("username IN ? AND created_at >= ?", names, windowStart).
		Group("username").Scan(&rows).Error; err != nil {
		return out
	}
	for _, r := range rows {
		out[r.Username] = r.N
	}
	return out
}

func loadApiKeyCounts(ids []uint) map[uint]int64 {
	out := map[uint]int64{}
	if len(ids) == 0 {
		return out
	}
	var rows []struct {
		UserID uint
		N      int64
	}
	if err := db.DB.Model(&models.ApiKey{}).
		Select("user_id, COUNT(*) AS n").Where("user_id IN ?", ids).
		Group("user_id").Scan(&rows).Error; err != nil {
		return out
	}
	for _, r := range rows {
		out[r.UserID] = r.N
	}
	return out
}

func daysBetween(start time.Time) int {
	d := int(time.Since(start).Hours() / 24)
	if d < 1 {
		d = 1
	}
	return d
}
