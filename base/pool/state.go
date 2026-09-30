package pool

import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"loomproxy/base"
	"loomproxy/conf"
	"loomproxy/db"
	"loomproxy/models"
)

// Config 号池运行参数（零值字段取 DefaultConfig 的对应值）
type Config struct {
	ColdSpares  int           // 冷备号数量（未领取、不过期）
	MaxHot      int           // 热号上限（错误驱动扩容的封顶）
	MaxDead     int           // dead 号保留上限，超出物理清理
	RenewBefore time.Duration // 热号到期前多久续领
	Interval    time.Duration // 维护协程巡检间隔
}

// DefaultConfig 从 POOL_* 环境变量取参；conf 尚未加载时回退内置默认值
// （冷备 2 / 热号上限 3 / dead 保留 10 / 到期前 5 分钟续领 / 每分钟巡检）
func DefaultConfig() Config {
	c := Config{ColdSpares: 2, MaxHot: 3, MaxDead: 10, RenewBefore: 5 * time.Minute, Interval: time.Minute}
	if conf.Config == nil {
		return c
	}
	if n := conf.Config.PoolColdSpares; n > 0 {
		c.ColdSpares = n
	}
	if n := conf.Config.PoolMaxHot; n > 0 {
		c.MaxHot = n
	}
	if n := conf.Config.PoolMaxDead; n >= 0 {
		c.MaxDead = n
	}
	if n := conf.Config.PoolRenewBeforeSec; n > 0 {
		c.RenewBefore = time.Duration(n) * time.Second
	}
	if n := conf.Config.PoolMaintainSec; n > 0 {
		c.Interval = time.Duration(n) * time.Second
	}
	return c
}

func (c Config) withDefaults() Config {
	d := DefaultConfig()
	if c.ColdSpares == 0 {
		c.ColdSpares = d.ColdSpares
	}
	if c.MaxHot == 0 {
		c.MaxHot = d.MaxHot
	}
	if c.MaxDead == 0 {
		c.MaxDead = d.MaxDead
	}
	if c.RenewBefore == 0 {
		c.RenewBefore = d.RenewBefore
	}
	if c.Interval == 0 {
		c.Interval = d.Interval
	}
	return c
}

// Pool 冷热分离号池：
//   - 资源按墙钟时间燃烧（与并发量无关），一个活跃号有效期内可服务无限并发，
//     因此平时只保持 1 个活跃号（多号并行会同步燃烧资源），N 个冷备（未领取不过期）
//   - 活跃号临期由维护协程原地续领（领取叠加无损耗）；周期次数用完则退役为
//     spent，冷号转正；spent 在周期重置后复活为冷备
//   - 上游限流嫌疑（错误率超阈值）时临时增加活跃号，恢复后不主动缩容——
//     多出的活跃号到期自然退役回冷备
type Pool struct {
	provider Provider
	cfg      Config
	flight   *base.Flight

	mu      sync.Mutex
	hot     []*models.PoolDevice
	cold    []*models.PoolDevice
	hotRR   int // 活跃号轮询游标（多活跃号时）
	window  errWindow
	ticker  *time.Ticker
	stopCh  chan struct{}
	running atomic.Bool
}

// New 构造号池（不启动维护协程，不落库）
func New(provider Provider, cfg Config) *Pool {
	return &Pool{
		provider: provider,
		cfg:      cfg.withDefaults(),
		flight:   base.NewFlight(20 * time.Second),
		stopCh:   make(chan struct{}),
	}
}

func (p *Pool) Name() string { return p.provider.Name() }

func (p *Pool) logf(format string, args ...interface{}) {
	log.Printf("pool[%s]: "+format, append([]interface{}{p.Name()}, args...)...)
}

// Start 加载存量号并分类、转正首个活跃号、补齐冷备，然后启动维护协程。
// 数据库未就绪时跳过（仅告警，不阻断启动——与缓存预热的守卫同类）。
func (p *Pool) Start() {
	if db.DB == nil {
		p.logf("数据库未连接，跳过号池初始化")
		return
	}
	if p.running.Swap(true) {
		return // 已启动
	}
	p.logf("initializing pool (hot/cold model)")
	p.initLedger()

	p.mu.Lock()
	if _, err := p.promoteLocked(); err != nil {
		p.logf("转正首个活跃号失败: %v", err)
	}
	p.topUpColdLocked()
	p.mu.Unlock()
	p.logf("init done, hot=%d cold=%d", p.HotCount(), p.ColdCount())

	p.ticker = time.NewTicker(p.cfg.Interval)
	go func() {
		for {
			select {
			case <-p.ticker.C:
				p.Maintain()
			case <-p.stopCh:
				return
			}
		}
	}()
}

func (p *Pool) Stop() {
	if !p.running.CompareAndSwap(true, false) {
		return
	}
	if p.ticker != nil {
		p.ticker.Stop()
	}
	close(p.stopCh)
	p.logf("stopped")
}

func (p *Pool) Running() bool { return p.running.Load() }

// initLedger 把库中全部非 dead 号逐一刷新后分类（重启即迁移：
// 上一轮遗留的 hot/spent 按真实额度重新归类，续期成本为零）
func (p *Pool) initLedger() {
	var rows []models.PoolDevice
	if err := db.DB.Where("pool = ? AND status != ?", p.Name(), StatusDead).
		Order("id").Find(&rows).Error; err != nil {
		p.logf("加载存量号失败: %v", err)
		return
	}
	for i := range rows {
		row := &rows[i]
		q, err := p.refresh(row)
		if err != nil {
			p.markDead(row)
			continue
		}
		if q.Exhausted() {
			p.setStatus(row, StatusSpent)
			continue
		}
		p.setStatus(row, StatusCold)
		p.cold = append(p.cold, row)
	}
	p.logf("loaded %d cold devices from db", len(p.cold))
}

// refresh 拉取真实额度并写回台账（不落库，由调用方 Save）
func (p *Pool) refresh(row *models.PoolDevice) (Quota, error) {
	ctx := context.Background()
	q, err := p.provider.Refresh(ctx, deviceOf(row))
	if err != nil {
		return Quota{}, err
	}
	row.TotalQuota, row.UsedQuota = q.Total, q.Used
	if q.ExpiresAt.IsZero() {
		row.ExpireAt = nil
	} else {
		t := q.ExpiresAt
		row.ExpireAt = &t
	}
	if err := db.DB.Save(row).Error; err != nil {
		return Quota{}, err
	}
	return q, nil
}

// claim 为号领取一次资源，并刷新台账
func (p *Pool) claim(row *models.PoolDevice) error {
	ctx := context.Background()
	if err := p.provider.Claim(ctx, deviceOf(row)); err != nil {
		return err
	}
	_, err := p.refresh(row)
	return err
}

func (p *Pool) setStatus(row *models.PoolDevice, status string) {
	row.Status = status
	if err := db.DB.Save(row).Error; err != nil {
		p.logf("保存号 %s 状态失败: %v", row.Ident, err)
	}
}

func (p *Pool) markDead(row *models.PoolDevice) {
	p.logf("device %s marked dead", row.Ident)
	p.setStatus(row, StatusDead)
}

// Acquire 取一个活跃号（有效期内的号可服务无限并发，无需多号分摊）。
// 无活跃号时现场转正一个；冷备也没有则新建后转正。
func (p *Pool) Acquire() (*Device, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.hot) > 0 {
		p.hotRR = (p.hotRR + 1) % len(p.hot)
		return deviceOf(p.hot[p.hotRR]), nil
	}
	row, err := p.promoteLocked()
	if err != nil {
		if _, cerr := p.createColdLocked(); cerr != nil {
			return nil, fmt.Errorf("无可用号: %v", err)
		}
		row, err = p.promoteLocked()
	}
	if row == nil {
		return nil, err
	}
	return deviceOf(row), err
}

// Reauthorize 处理上游「资源到期」类失败：同一号的并发失败只触发一次补领/换号
// （singleflight 合并），其余调用方共享结果。调用方不得持有 p.mu。
func (p *Pool) Reauthorize(dev *Device) (*Device, error) {
	res, err := p.flight.Do(dev.Ident, func() (interface{}, error) {
		return p.reauthorize(dev.Ident)
	})
	if err != nil {
		return nil, err
	}
	out, _ := res.(*Device)
	return out, nil
}

func (p *Pool) reauthorize(ident string) (*Device, error) {
	row := p.rowByIdent(ident)
	if row == nil {
		return nil, fmt.Errorf("号池 %s 中不存在号 %s", p.Name(), ident)
	}
	// 刷新真实状态：可能只是本地台账过期
	q, rerr := p.refresh(row)
	if rerr == nil && !q.Exhausted() {
		if err := p.claim(row); err == nil {
			p.logf("device %s re-authorized via claim (valid %.0f min)", ident, remaining(row).Minutes())
			return deviceOf(row), nil
		}
	}
	// 周期次数用完或刷新失败：退役换号
	p.logf("device %s cannot re-authorize, switching", ident)
	p.mu.Lock()
	defer p.mu.Unlock()

	p.removeLocked(&p.hot, row)
	if rerr != nil {
		p.markDead(row)
	} else {
		p.setStatus(row, StatusSpent)
	}
	newRow, err := p.promoteLocked()
	if err != nil {
		if _, cerr := p.createColdLocked(); cerr == nil {
			newRow, err = p.promoteLocked()
		}
	}
	p.topUpColdLocked()
	if err != nil {
		return nil, err
	}
	return deviceOf(newRow), nil
}

// Report 上报一次业务调用结果，用于错误驱动的活跃号扩容。
// 资源到期类错误（走自愈而非限流）经 Provider 判定后不计入。
func (p *Pool) Report(err error) {
	if err == nil {
		p.window.record(false)
		return
	}
	if c, ok := p.provider.(ResourceExpiredClassifier); ok && c.IsResourceExpired(err) {
		return
	}
	p.window.record(true)
}

// Maintain 巡检：活跃号续期/退役、冷备补齐、dead 清理、限流扩容评估
func (p *Pool) Maintain() {
	if db.DB == nil {
		return
	}
	p.mu.Lock()
	for _, row := range append([]*models.PoolDevice{}, p.hot...) {
		if remaining(row) > p.cfg.RenewBefore {
			continue
		}
		q, err := p.refresh(row)
		if err != nil {
			p.logf("refresh hot device %s failed: %v", row.Ident, err)
			continue // 下轮再试
		}
		if !q.Exhausted() {
			if err := p.claim(row); err != nil {
				p.logf("renew claim for %s failed: %v", row.Ident, err)
			} else {
				p.logf("renewed hot device %s (valid %.0f min)", row.Ident, remaining(row).Minutes())
			}
			continue
		}
		p.logf("hot device %s spent, retiring", row.Ident)
		p.removeLocked(&p.hot, row)
		p.setStatus(row, StatusSpent)
		if _, err := p.promoteLocked(); err != nil {
			p.logf("promote replacement failed: %v", err)
		}
	}
	p.topUpColdLocked()
	p.mu.Unlock()

	p.purgeDead()

	// 错误驱动扩容：疑似上游限流时增加活跃号（上限 MaxHot），
	// 恢复后不主动缩容——额外活跃号到期自然退役回冷备
	if rate, n := p.window.reset(); n >= 20 && rate > 0.3 {
		p.mu.Lock()
		if len(p.hot) < p.cfg.MaxHot {
			if _, err := p.promoteLocked(); err == nil {
				p.logf("疑似上游限流（窗口错误率 %.0f%% / %d 次采样），活跃号扩容至 %d",
					rate*100, n, len(p.hot))
			}
		}
		p.mu.Unlock()
	}
}

// promoteLocked 把一个冷号转为活跃号（领取资源）（调用方持锁）。
// 候选按 PrioritizeClaimed 排序：已领取且仍在有效期的冷备优先转正。
func (p *Pool) promoteLocked() (*models.PoolDevice, error) {
	for _, row := range PrioritizeClaimed(p.cold) {
		// 转正前刷新真实状态（周期重置在此自然体现）
		q, err := p.refresh(row)
		if err != nil {
			p.removeLocked(&p.cold, row)
			p.markDead(row)
			continue
		}
		if q.Exhausted() {
			p.removeLocked(&p.cold, row)
			p.setStatus(row, StatusSpent)
			continue
		}
		// 仍有有效期（如重启后恢复的号）直接转正，不重复领取
		if remaining(row) <= p.cfg.RenewBefore {
			if err := p.claim(row); err != nil {
				p.logf("claim for %s failed: %v", row.Ident, err)
				p.removeLocked(&p.cold, row)
				p.setStatus(row, StatusSpent)
				continue
			}
		}
		p.removeLocked(&p.cold, row)
		p.setStatus(row, StatusHot)
		p.hot = append(p.hot, row)
		p.logf("promoted device %s to hot (valid %.0f min)", row.Ident, remaining(row).Minutes())
		return row, nil
	}
	return nil, fmt.Errorf("无可用冷备号")
}

// createColdLocked 新建一个冷备号（只建号不领取，不过期）（调用方持锁）
func (p *Pool) createColdLocked() (*models.PoolDevice, error) {
	dev, err := p.provider.Create(context.Background())
	if err != nil {
		return nil, err
	}
	row := &models.PoolDevice{Pool: p.Name(), Ident: dev.Ident, Status: StatusCold, Attrs: encodeAttrs(dev.Attrs)}
	if err := db.DB.Create(row).Error; err != nil {
		return nil, err
	}
	p.cold = append(p.cold, row)
	p.logf("created cold device %s", row.Ident)
	return row, nil
}

// topUpColdLocked 冷备补齐至 ColdSpares：先尝试复活 spent，再新建（调用方持锁）
func (p *Pool) topUpColdLocked() {
	for len(p.cold) < p.cfg.ColdSpares {
		if p.reviveOneSpentLocked() {
			continue
		}
		if _, err := p.createColdLocked(); err != nil {
			p.logf("create cold device failed: %v", err)
			return
		}
	}
}

// reviveOneSpentLocked 尝试复活一个 spent 号（周期重置后可领）（调用方持锁）。
// 按 id 升序逐个刷新：额度未用满即转 cold 返回 true；刷新失败的判死并续试下一个；
// 全部仍处用满状态（或无 spent）才返回 false。
// 只试首个号会在长期未重置的场景下饿死其余 spent 号，跨周期也无法复活。
func (p *Pool) reviveOneSpentLocked() bool {
	var rows []models.PoolDevice
	if err := db.DB.Where("pool = ? AND status = ?", p.Name(), StatusSpent).
		Order("id").Find(&rows).Error; err != nil || len(rows) == 0 {
		return false
	}
	for i := range rows {
		row := &rows[i]
		q, err := p.refresh(row)
		if err != nil {
			p.markDead(row)
			continue // 已判死，试下一个 spent 号
		}
		if !q.Exhausted() {
			p.setStatus(row, StatusCold)
			p.cold = append(p.cold, row)
			p.logf("revived spent device %s", row.Ident)
			return true
		}
	}
	return false
}

// purgeDead 死号超量物理清理（限量保留便于排查上游风控）
func (p *Pool) purgeDead() {
	var count int64
	if err := db.DB.Model(&models.PoolDevice{}).
		Where("pool = ? AND status = ?", p.Name(), StatusDead).Count(&count).Error; err != nil {
		return
	}
	if count <= int64(p.cfg.MaxDead) {
		return
	}
	toDelete := int(count) - p.cfg.MaxDead
	db.DB.Unscoped().Where("pool = ? AND status = ?", p.Name(), StatusDead).
		Limit(toDelete).Delete(&models.PoolDevice{})
	p.logf("purged %d dead devices", toDelete)
}

// removeLocked 从列表移除（调用方持锁），按 Ident 匹配
func (p *Pool) removeLocked(list *[]*models.PoolDevice, row *models.PoolDevice) {
	for i, d := range *list {
		if d.ID == row.ID {
			*list = append((*list)[:i], (*list)[i+1:]...)
			return
		}
	}
}

// rowByIdent 按标识取号记录（走库查询，无需持池锁）
func (p *Pool) rowByIdent(ident string) *models.PoolDevice {
	var row models.PoolDevice
	if err := db.DB.Where("pool = ? AND ident = ?", p.Name(), ident).First(&row).Error; err != nil {
		return nil
	}
	return &row
}

// PrioritizeClaimed 转正候选排序：优先使用已领取且仍在有效期的冷备
// （其时长按墙钟燃烧，放着不用也是浪费，越早到期的越优先转正）；
// 未领取或已过期的保持原相对顺序在后。纯函数，导出以便测试。
func PrioritizeClaimed(cold []*models.PoolDevice) []*models.PoolDevice {
	claimed := make([]*models.PoolDevice, 0, len(cold))
	rest := make([]*models.PoolDevice, 0, len(cold))
	for _, d := range cold {
		if d.UsedQuota > 0 && remaining(d) > 0 {
			claimed = append(claimed, d)
		} else {
			rest = append(rest, d)
		}
	}
	sort.SliceStable(claimed, func(i, j int) bool {
		return claimed[i].ExpireAt.Before(*claimed[j].ExpireAt)
	})
	return append(claimed, rest...)
}

func remaining(row *models.PoolDevice) time.Duration {
	if row.ExpireAt == nil {
		return 0
	}
	return time.Until(*row.ExpireAt)
}

func (p *Pool) HotCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.hot)
}

func (p *Pool) ColdCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.cold)
}

// errWindow 错误率滑动窗口：record 上报，reset 由维护协程每轮取数并清零
type errWindow struct {
	mu    sync.Mutex
	total int
	errs  int
}

func (w *errWindow) record(isErr bool) {
	w.mu.Lock()
	w.total++
	if isErr {
		w.errs++
	}
	w.mu.Unlock()
}

func (w *errWindow) reset() (rate float64, n int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n = w.total
	if n > 0 {
		rate = float64(w.errs) / float64(n)
	}
	w.total, w.errs = 0, 0
	return
}
