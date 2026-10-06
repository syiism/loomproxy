package pool

import (
	"context"
	"errors"
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

// Config 号池运行参数（零值字段取 DefaultConfig 的对应值）。
//
// **一条歧义要写明**（待办清单 P85）：`0` 在下面这些字段里的意思是"没写"，不是"要 0 个"——
// `ColdSpares` / `MaxHot` / `MaxDead` / `RenewBefore` / `Interval` / `HookTimeout` 都会把 0 回填成
// 默认档（`MaxDead` 想留 0 个目前只能靠 `POOL_MAX_DEAD=0` 且源侧留空）。
// 而 `MaxDevices` 的 0 **是一个值**（0=不限），`TargetDevices` 的 0 另有回退链（0 → MaxDevices → 1）——
// 同一个结构体里两种 0 并存。现网没有活症状（qm_device 被名额夹回 0 冷备、fq 摊薄型不看 ColdSpares，
// 两者实测 cold=0），但下一个想写"显式 0"的源会踩。要不要把 API 改成能表达"显式 0"，已登记成决策项。
type Config struct {
	ColdSpares      int           // 冷备号数量（未领取、不过期）
	MaxHot          int           // 热号上限（错误驱动扩容的封顶）
	MaxDead         int           // dead 号保留上限，超出物理清理
	MaxDevices      int           // 本池总号数上限（0=不限）：建号会对上游产生不可逆增长时的闸门
	Kind            string        // 池形态：空/KindBurnWallClock = 墙钟燃烧型；KindSpread = 用量摊薄型（框架按它分叉调度）
	TargetDevices   int           // spread 型的目标可用号数（0 → 取 MaxDevices，再 0 → 1）；wallclock 型不用
	CooldownDefault time.Duration // spread 型里 Reauthorize 的兜底冷却时长（0 → 10 分钟）
	RenewBefore     time.Duration // 热号到期前多久续领
	Interval        time.Duration // 维护协程巡检间隔
	HookTimeout     time.Duration // 一次源钩子调用（Create/Refresh/Claim）的超时上限（0 → 30 秒）
}

// DefaultConfig 从 POOL_* 环境变量取参；conf 尚未加载时回退内置默认值
// （冷备 2 / 热号上限 3 / dead 保留 10 / 到期前 5 分钟续领 / 每分钟巡检）
func DefaultConfig() Config {
	c := Config{ColdSpares: 2, MaxHot: 3, MaxDead: 10, RenewBefore: 5 * time.Minute, Interval: time.Minute,
		HookTimeout: defaultHookTimeout}
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
	if n := conf.Config.PoolHookTimeoutSec; n > 0 {
		c.HookTimeout = time.Duration(n) * time.Second
	}
	return c
}

// KindBurnWallClock 墙钟燃烧型——Kind 留空时的默认含义：只保持 1 个活跃号 + N 个冷备，
// 临期续领、用尽换号、限流扩容（面板标签见待办清单 P6）。
const KindBurnWallClock = "burn_wall_clock"

// KindSpread 用量摊薄型：号不因墙钟过期，只在被风控时临时不可用，理想是把请求摊到多台。
// 该形态下框架只调 Provider.Create——不 Claim、不 Refresh、启动不逐行探活（探活本身就是风控成本），
// 可用性由业务侧经 Pool.Cooldown 上报，到期自动回可用（待办清单 P5）。
// 「只保持 1 个活跃号 + 冷备续领」那套对这种池是有害的（会把所有请求打到同一台设备上），
// 所以按 Kind 分叉调度，而不是再加一个布尔开关。
const KindSpread = "spread"

// ErrCapacityReached 池内总号数已到 Config.MaxDevices 上限、无法再新建号。做成 sentinel
// 是为了让源侧与调用方能 errors.Is 判定，而不是去比字符串。
var ErrCapacityReached = errors.New("号池已达总号数上限")

func (c Config) withDefaults() Config {
	d := DefaultConfig()
	if c.Kind == KindSpread && c.CooldownDefault <= 0 {
		// 只对 spread 回填：墙钟型没有「冷却」概念，无条件回填会让它的节奏被默认值改动
		c.CooldownDefault = 10 * time.Minute
	}
	// MaxDevices 刻意不参与零值回填：0 的语义就是「不限」，填成默认值反而把不限制的池锁死
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
	if c.HookTimeout == 0 {
		c.HookTimeout = d.HookTimeout
	}
	// 水位不得超过名额——这是 deadRetention 那条规则（「先留得下可用号，再谈留档」）的另一半：
	// 燃烧型的名义目标是 1 个活跃 + ColdSpares 个冷备，而 MaxDevices 把 cold+hot+dead 全算进去，
	// 两个数字过去互不知情（源侧写死的 MaxDevices 与运维改过的 POOL_COLD_SPARES 各说各话）。
	// 补不到的水位不是算错，是**每个巡检都要新建一个号、每次都被自己的上限判住**：生产 qm_device
	// （MaxDevices=1、默认水位 2）就是这个形状——功能没坏（活跃号照常服务），但日志每 60 秒一条
	// error，把「这个池有问题」的读数长期污染（分支 S34 发布时实测）。
	// 只往下夹水位，**MaxDevices 本身不动**：它是「建号对上游是不可逆增长」的闸门，
	// 按水位收紧它会把错误驱动扩容的余量一起削掉（P35 特意留给它的那部分）。
	// 且下限 1：建号只走「补齐冷备」这一条路，夹到 0 等于让池一个号都建不出来。MaxDevices=0 即不限。
	if c.MaxDevices > 0 {
		if c.Kind == KindSpread {
			// 摊薄型没有活跃/冷备之分，水位就是目标人数；未声明时 spreadTarget 自己退到名额
			if c.TargetDevices > c.MaxDevices {
				c.TargetDevices = c.MaxDevices
			}
		} else if room := c.MaxDevices - 1; room < c.ColdSpares { // 名额先给那 1 个活跃号
			if room < 1 {
				room = 1
			}
			c.ColdSpares = room
		}
	}
	return c
}

// Pool 号池：调度节奏由 Config.Kind 决定，两种形态共用同一套持久化与快照。
//
// 墙钟燃烧型（默认）——资源按墙钟时间燃烧（与并发量无关），一个活跃号有效期内可服务
// 无限并发，因此平时只保持 1 个活跃号（多号并行会同步燃烧资源），N 个冷备（未领取不过期）：
//   - 活跃号临期由维护协程原地续领（领取叠加无损耗）；周期次数用完则退役为
//     spent，冷号转正；spent 在周期重置后复活为冷备
//   - 上游限流嫌疑（错误率超阈值）时临时增加活跃号，恢复后不主动缩容——
//     多出的活跃号到期自然退役回冷备
//
// 用量摊薄型（KindSpread）——号不因墙钟过期，燃烧速度取决于请求量，故 hot 就是「全部可用号」：
//   - Acquire 在全部可用号上轮询；转正不 Claim、启动不逐行 Refresh（打上游本身就是风控成本）
//   - 无「周期额度」概念，失效表达是冷却：业务侧经 Pool.Cooldown 上报，到期自动回轮询
//   - 缺号才建号，受 MaxDevices 约束；巡检只做放行冷却 + 补到目标数 + 清死号
type Pool struct {
	provider Provider
	cfg      Config
	flight   *base.Flight

	mu       sync.Mutex
	hot      []*models.PoolDevice
	cold     []*models.PoolDevice
	hotRR    int                  // 活跃号轮询游标（多活跃号时）
	cooldown []*models.PoolDevice // 冷却中的号（只 spread 型会填）

	// 周期性重试的失败只说一次（noteBad / noteGood），键是"路径 + 号身份"。
	// 只在持 p.mu 的维护路径里读写，所以不另加锁。
	lastBad      map[string]string
	lastBadCount map[string]int
	window       errWindow
	// ticker 由 Start 创建、Stop 撤销，两者可能真并发（优雅关停撞上启动中的装填），
	// 所以是原子指针而不是普通字段（待办清单 P80）
	ticker  atomic.Pointer[time.Ticker]
	stopCh  chan struct{}
	running atomic.Bool
	// capNoted 建号被总上限判住是否已出声——只在状态变化时记一条，见 topUpColdLocked
	capNoted bool
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

// spread 报告本池是否为用量摊薄型。框架内的分叉统一走它，不把 Kind 比较散落各处。
func (p *Pool) spread() bool { return p.cfg.Kind == KindSpread }

// spreadTarget 摊薄型的目标可用号数：未显式声明就退到 MaxDevices，再退到 1
func (p *Pool) spreadTarget() int {
	if p.cfg.TargetDevices > 0 {
		return p.cfg.TargetDevices
	}
	if p.cfg.MaxDevices > 0 {
		return p.cfg.MaxDevices
	}
	return 1
}

func (p *Pool) logf(format string, args ...interface{}) {
	log.Printf("pool[%s]: "+format, append([]interface{}{p.Name()}, args...)...)
}

// locked 在持锁状态下跑一段临界区，**用 defer 解锁**。
// 号池的临界区里会调源写的钩子（Provider.Refresh/Claim/Create），显式 Lock/Unlock 的写法
// 一旦被 panic 穿过去，锁就永久 held：下一 tick 的 HotCount/Maintain 全部阻塞，
// 表现为「池从此不再维护、而进程活着、面板数字还在」——比崩溃更难发现（待办清单 P77）。
func (p *Pool) locked(fn func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fn()
}

// 后台路径（启动、维护 tick）的兜底直接 defer base.Guard（P77/P78 → P103 收成一处实现）。
//
// 为什么不再包一层 p.guardPanic(...)：`recover()` 只认**被 panic 的那个函数直接 defer 的那个调用**。
// 第四十九遍我先写的是 `defer p.guardPanic(...)` 而 guardPanic 里再调 base.Guard——
// 那已经不是直接 defer，recover() 返回 nil，`test/pool_panic_guard_test.go` 当场红
// （`TestPoolMaintainSurvivesProviderPanic`）。所以这两处改成直接 defer 到 base.Guard，
// 名字在这里拼好（defer 的实参在 defer 语句执行时就求值，p.Name() 不经锁）。

// noteBad 判断这条坏消息该不该说：**与上一次同一种失败就只累计不出声**，返回 false。
//
// 维护协程每一 tick 都会重试临期号，所以"持续性、但不是故障"的状态（上游当天收益到顶、正在维护、
// 网络一时不通）如果每轮都喊一条，就成了每 60 秒一条的噪音——那正是待办清单 P36 为
// `ErrCapacityReached` 单独处理过的形状（生产 v62 上线后 qm_device 每 60 秒一条 error，
// 把「这个池有问题」的读数泡坏）。当时只补了一处；这一族真正的形状是
// **“任何周期性重试的失败都会每轮喊一次”**，所以这道守卫放在维护循环的三个出口上。
// 键含身份（哪台号、哪条路径），值取错误文案：文案变了就算状态变化，要说。
func (p *Pool) noteBad(key, msg string) bool {
	if p.lastBad == nil {
		p.lastBad = map[string]string{}
	}
	if p.lastBadCount == nil {
		p.lastBadCount = map[string]int{}
	}
	same := p.lastBad[key] == msg
	p.lastBad[key] = msg
	if same {
		p.lastBadCount[key]++
		return false
	}
	p.lastBadCount[key] = 1
	return true
}

// noteGood 在这条路径从坏转好时返回 (此前累计失败次数, true)；本来一直好则返回 (0, false)。
// 恢复那一句必须带累计数：只说"好了"会让人以为刚才只错了一次。
// **只在成功分支调用**——在失败分支里调它会把状态清掉，下一轮就冒出假的"已恢复"。
func (p *Pool) noteGood(key string) (int, bool) {
	prev, ok := p.lastBad[key]
	if !ok || prev == "" {
		return 0, false
	}
	n := p.lastBadCount[key]
	delete(p.lastBad, key)
	delete(p.lastBadCount, key)
	return n, true
}

// Start 加载存量号并分类、转正首个活跃号、补齐冷备，然后启动维护协程。
// 数据库未就绪时跳过（仅告警，不阻断启动——与缓存预热的守卫同类）。
func (p *Pool) Start() {
	defer base.Guard("号池 " + p.Name() + " 的启动路径")
	if db.DB == nil {
		p.logf("数据库未连接，跳过号池初始化")
		return
	}
	if p.running.Swap(true) {
		return // 已启动
	}
	if p.spread() {
		p.logf("initializing pool (spread model: 全部可用号轮询，不 Claim 不探活)")
	} else {
		p.logf("initializing pool (hot/cold model)")
	}
	p.initLedger()

	p.locked(func() {
		if p.spread() {
			// 摊薄型没有「转正首个活跃号 + 补冷备」这回事：可用号本来就是全部号，
			// 走一次燃烧型路径等于在启动时对上游 Claim/探活（方案 §3.4 明令禁止）
			if _, err := p.acquireSpreadLocked(); err != nil {
				p.logf("启动后可用号为空: %v", err)
			}
		} else {
			if _, err := p.promoteLocked(); err != nil {
				p.logf("转正首个活跃号失败: %v", err)
			}
			p.topUpColdLocked()
		}
	})
	p.logf("init done, hot=%d cold=%d cooldown=%d", p.HotCount(), p.ColdCount(), p.CooldownCount())

	// ticker 是 **Start 写、Stop 读** 的两块门牌，而这两条路径会真的重叠：优雅关停（lifecycle 的 StopAll）
	// 撞上还在逐行探活装填的启动协程——现网每次部署都有这个窗口（`go pool.StartAll()` 在监听之前，
	// 而 systemd 的重启间隔只有几秒）。用普通字段读它就会被 `-race` 判竞争（待办清单 P80，
	// 是 P78 那条用例在整包跑的时候撞出来的），所以指针原子读写，协程内只用自己的局部变量。
	t := time.NewTicker(p.cfg.Interval)
	p.ticker.Store(t)
	go func() {
		defer t.Stop() // 协程退出时一定收掉定时器，包括「Stop 已经先跑过、这里立刻从 stopCh 返回」那一支
		for {
			select {
			case <-t.C:
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
	if t := p.ticker.Swap(nil); t != nil {
		t.Stop()
	}
	close(p.stopCh)
	p.logf("stopped")
}

func (p *Pool) Running() bool { return p.running.Load() }

// initLedger 装载存量号并分类（重启即迁移：上一轮遗留的 hot/spent 重新归类）。
// 燃烧型逐行 Refresh 取真实额度（续期成本为零），摊薄型纯本地分类（不打上游）
func (p *Pool) initLedger() {
	var rows []models.PoolDevice
	if err := db.DB.Where("pool = ? AND status != ?", p.Name(), StatusDead).
		Order("id").Find(&rows).Error; err != nil {
		p.logf("加载存量号失败: %v", err)
		return
	}
	if p.spread() {
		// 本地分类：可用性来自业务上报而不是上游查询。逐行 Refresh 会在启动时打满 N 次签名请求，
		// 对摊薄型是纯风险无收益（方案 §3.4）
		p.locked(func() {
			now := time.Now()
			for i := range rows {
				row := &rows[i]
				if row.Status == StatusCooldown && row.ExpireAt != nil && row.ExpireAt.After(now) {
					p.cooldown = append(p.cooldown, row)
					continue
				}
				// 冷却到期与上一轮的 hot/cold/spent 一律归为可用（摊薄型没有周期额度概念）
				if row.Status != StatusHot {
					p.setStatus(row, StatusHot)
				}
				p.hot = append(p.hot, row)
			}
			p.logf("loaded %d usable + %d cooling devices from db（未打上游）", len(p.hot), len(p.cooldown))
		})
		return
	}
	// 装填必须与请求路径互斥：`app.Run` 里 `go pool.StartAll()` 排在 `http.Server` 之前，
	// 而燃烧型要逐行打上游探活——端口已经开着、号池还在半装填时，并发请求的 Acquire
	// 读到的就是正在被 append 的 p.hot/p.cold（待办清单 P78；-race 实测报在本文件的 cold append 与 promoteLocked 的读之间）
	p.locked(func() {
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
	})
}

// defaultHookTimeout 是一次源钩子调用的超时上限默认值。
// 框架在这里能保证的只有「传下去一个带截止的 ctx」：Provider 若自己另起 context 打上游，
// 拦不住它——而维护与装填是**持着 p.mu 调钩子**的，一个不读 ctx 的源能卡住整个池的请求路径。
// 所以这条是给守规矩的源兜底，契约（必须把 ctx 传给网络调用）写在 Provider 的声明处。
const defaultHookTimeout = 30 * time.Second

// hookCtx 给一次源钩子调用套上截止。超时到了不额外喊一条日志：调用返回的 err 走的是
// 既有的失败出口（noteBad + 一条 ERROR），"超了多久" 从 err 里就看得见（context.DeadlineExceeded）。
func (p *Pool) hookCtx() (context.Context, context.CancelFunc) {
	d := p.cfg.HookTimeout
	if d <= 0 {
		d = defaultHookTimeout
	}
	return context.WithTimeout(context.Background(), d)
}

// callCreate 建新号一次（同样带截止）。超时有一条要写明的代价：上游可能已经真的建出了那个号，
// 而我们没拿到返回值——它就成了对方侧的一个孤儿，我们不重试也不去猜。以前没有超时，
// 那种情况下是**整个池卡在 Create 里不动**（还持着 p.mu），两害相权取前者。
func (p *Pool) callCreate() (*Device, error) {
	ctx, cancel := p.hookCtx()
	defer cancel()
	return p.provider.Create(ctx)
}

// refresh 拉取真实额度并写回台账（不落库，由调用方 Save）
func (p *Pool) refresh(row *models.PoolDevice) (Quota, error) {
	ctx, cancel := p.hookCtx()
	defer cancel()
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
	ctx, cancel := p.hookCtx()
	defer cancel()
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

// Acquire 取一个号：燃烧型返回单个活跃号（有效期内可服务无限并发），
// 无活跃号时现场转正、冷备也没有则新建后转正；摊薄型在全部可用号上轮询。
func (p *Pool) Acquire() (*Device, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.spread() {
		// 摊薄型：在全部可用号上轮询。一个号都没有时由 acquireSpreadLocked 给出带原因的错误
		return p.acquireSpreadLocked()
	}

	if len(p.hot) > 0 {
		p.hotRR = (p.hotRR + 1) % len(p.hot)
		return deviceOf(p.hot[p.hotRR]), nil
	}
	row, err := p.promoteLocked()
	if err != nil {
		if _, cerr := p.createColdLocked(); cerr != nil {
			// 两个错都带上：只报「无可用号」会让人看不出是补号被上限拦了还是上游真没号了
			return nil, fmt.Errorf("无可用号: %w（补建号: %w）", err, cerr)
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
	if p.spread() {
		// 摊薄型没有「续领」可做：把这台临时冻结一段，另取一台。
		// 源侧有精确冷却口径时应直接调 Pool.Cooldown，这里只是兜底路径
		p.Cooldown(ident, time.Now().Add(p.cfg.CooldownDefault), "上游判定不可用（Reauthorize 兜底）")
		return p.Acquire()
	}
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
	defer base.Guard("号池 " + p.Name() + " 的维护路径")
	if db.DB == nil {
		return
	}
	if p.spread() {
		// 只做三件事：放行到期冷却、把可用号补到目标数、清死号。
		// 续领与错误驱动扩容是燃烧型概念，对摊薄型无意义（所有可用号本来就都在轮询）
		p.locked(func() {
			if _, err := p.acquireSpreadLocked(); err != nil {
				p.logf("maintain: %v", err) // 全冷却不是异常状态，但必须看得见（别静默降级）
			}
		})
		p.purgeDead()
		return
	}
	p.locked(p.maintainBurnLocked)

	p.purgeDead()

	// 错误驱动扩容：疑似上游限流时增加活跃号（上限 MaxHot），
	// 恢复后不主动缩容——额外活跃号到期自然退役回冷备
	if rate, n := p.window.reset(); n >= 20 && rate > 0.3 {
		p.locked(func() {
			if len(p.hot) < p.cfg.MaxHot {
				if _, err := p.promoteLocked(); err == nil {
					p.logf("疑似上游限流（窗口错误率 %.0f%% / %d 次采样），活跃号扩容至 %d",
						rate*100, n, len(p.hot))
				}
			}
		})
	}
}

// maintainBurnLocked 是燃烧型维护的主体（调用方持锁）：临期续领、用尽退役并转正替补、补齐冷备。
// 单独成一个方法只为给临界区提供 defer 解锁的落点（见 locked 的注释）——判定与机制一点没变。
func (p *Pool) maintainBurnLocked() {
	for _, row := range append([]*models.PoolDevice{}, p.hot...) {
		if remaining(row) > p.cfg.RenewBefore {
			continue
		}
		q, err := p.refresh(row)
		if err != nil {
			// 台账刷新失败下轮再试；同一种错连续出现只说第一句（noteBad/noteGood 的分工写在各自注释里）
			if p.noteBad("refresh "+row.Ident, err.Error()) {
				p.logf("refresh hot device %s failed: %v（同一种失败此后只累计次数，恢复时再说一次）", row.Ident, err)
			}
			continue // 下轮再试
		}
		if n, ok := p.noteGood("refresh " + row.Ident); ok && n > 0 {
			p.logf("refresh hot device %s 台账已恢复（此前连续失败 %d 次）", row.Ident, n)
		}
		if !q.Exhausted() {
			if err := p.claim(row); err != nil {
				// 续领失败里有相当一部分是**业务上的正常状态**（如某源金币当天到顶，
				// Provider 明说"今日收益已到顶"）——那不是故障，每 60 秒喊一条只会把读数泡坏
				if p.noteBad("renew-claim "+row.Ident, err.Error()) {
					p.logf("renew claim for %s failed: %v（同一种失败此后只累计次数，恢复时再说一次）", row.Ident, err)
				}
			} else {
				if n, ok := p.noteGood("renew-claim " + row.Ident); ok && n > 0 {
					p.logf("renew claim for %s 已恢复（此前连续失败 %d 次）", row.Ident, n)
				}
				p.logf("renewed hot device %s (valid %.0f min)", row.Ident, remaining(row).Minutes())
			}
			continue
		}
		p.logf("hot device %s spent, retiring", row.Ident)
		p.removeLocked(&p.hot, row)
		p.setStatus(row, StatusSpent)
		if _, err := p.promoteLocked(); err != nil {
			if p.noteBad("promote", err.Error()) {
				p.logf("promote replacement failed: %v（同一种失败此后只累计次数，恢复时再说一次）", err)
			}
		} else if n, ok := p.noteGood("promote"); ok && n > 0 {
			p.logf("promote replacement 已恢复（此前连续失败 %d 次）", n)
		}
	}
	p.topUpColdLocked()
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

// createColdLocked 新建一个冷备号（只建号不领取，不过期）（调用方持锁）。
// MaxDevices 判在这里而不是让每个源自己数——「建号会对上游产生不可逆增长」是池的共性
// （待办清单 P4）。计数含 spent/dead，与源侧原先自己实现的口径一致：判死清理腾出的名额重新可用。
func (p *Pool) createColdLocked() (*models.PoolDevice, error) {
	if p.cfg.MaxDevices > 0 {
		n, err := p.countRows()
		if err != nil {
			return nil, err
		}
		if n >= int64(p.cfg.MaxDevices) {
			// 占着名额的可能全是死号：先让 purgeDead 按生效保留上限腾一次，再判失败。
			// 不等下一轮巡检是因为巡检间隔里源侧一直在拿不到号的错里空转。
			p.purgeDead()
			if n, err = p.countRows(); err != nil {
				return nil, err
			}
		}
		if n >= int64(p.cfg.MaxDevices) {
			return nil, fmt.Errorf("%w: %s（上限 %d，现有 %d，其中死号占位 %d 条而保留上限 %d）",
				ErrCapacityReached, p.Name(), p.cfg.MaxDevices, n, p.countDead(), p.deadRetention())
		}
	}
	dev, err := p.callCreate()
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
			if errors.Is(err, ErrCapacityReached) {
				// 被总上限判住是**闸门在做事**，不是故障：名额已被活跃号与冷备占满时补不出号。
				// 每个巡检重复喊一条，只会把日志与面板读数泡成「这个池坏了」——单设备池
				// （MaxDevices=1）必然长期如此，因为它的一个名额就该给那一个活跃号。
				// 出声规则：状态变化时一条（首次被判住），建号成功即复位。
				if !p.capNoted {
					p.logf("冷备补不满: %v（按总上限判定，非故障）", err)
					p.capNoted = true
				}
				return
			}
			p.logf("create cold device failed: %v", err)
			return
		}
		p.capNoted = false
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

// purgeDead 死号超量物理清理（限量保留便于排查上游风控）。
// 保留上限走 deadRetention，不直接用 MaxDead——否则 MaxDead > MaxDevices 的池
// 永远清不动死号，而那些死号正占着建号名额（待办清单 P35，登记于分支 S27）。
func (p *Pool) purgeDead() {
	var count int64
	if err := db.DB.Model(&models.PoolDevice{}).
		Where("pool = ? AND status = ?", p.Name(), StatusDead).Count(&count).Error; err != nil {
		return
	}
	keep := p.deadRetention()
	if count <= int64(keep) {
		return
	}
	// 「删最旧的几条」必须先把 id 捞出来再按 id 删：GORM 的 `Limit(n).Delete()` 只在 MySQL 成立，
	// SQLite 直接忽略 LIMIT（= 把匹配到的全删了），而开发/测试库正是 SQLite——
	// 用例断言「保留集还剩几条」才看得见这件事，只断言「变少了」会一路绿到生产（待办清单 P35）
	var ids []uint
	if err := db.DB.Unscoped().Model(&models.PoolDevice{}).
		Where("pool = ? AND status = ?", p.Name(), StatusDead).
		Order("id ASC").Limit(int(count)-keep).Pluck("id", &ids).Error; err != nil {
		p.logf("purge dead devices failed: %v", err)
		return
	}
	if len(ids) == 0 {
		return
	}
	if err := db.DB.Unscoped().Delete(&models.PoolDevice{}, ids).Error; err != nil {
		p.logf("purge dead devices failed: %v", err)
		return
	}
	toDelete := len(ids)
	if keep != p.cfg.MaxDead {
		// 上限被容量压下来时必须说出来：运维看到的 5 台里为什么死号只留 2 条，
		// 不该靠翻代码（同 P19 那条「该由系统自己说」）
		p.logf("purged %d dead devices（保留 %d 条；配置 MaxDead=%d，被总号上限 %d 与可用目标 %d 压到 %d）",
			toDelete, keep, p.cfg.MaxDead, p.cfg.MaxDevices, p.usableTarget(), keep)
		return
	}
	p.logf("purged %d dead devices", toDelete)
}

// usableTarget 这个池要维持的可用号数：spread 看轮询目标，燃烧型是 1 个活跃 + N 个冷备。
func (p *Pool) usableTarget() int {
	if p.spread() {
		return p.spreadTarget()
	}
	return 1 + p.cfg.ColdSpares
}

// deadRetention 实际生效的死号保留上限。
//
// 两个旋钮过去互不知情：`MaxDevices` 是「建号对上游是不可逆增长」的闸门（P4，计数含 dead），
// `MaxDead` 是排查风控的留档上限；**当 MaxDevices < MaxDead 时死号既清不掉、又占满名额**，
// 池只剩一条 "create cold device failed" 的 error 日志就静默失能——生产 uxx 就是 5 < 10 这个形状。
// 规则：**先留得下可用号，再谈留档**——保留上限不得超过「总上限减去可用目标」，
// 且只在设了总上限时收紧（`MaxDevices=0` 即不限，保留上限就是配置值）。
// 于是 spread 池里 `TargetDevices == MaxDevices` 时上限为 0：那类池每台死号都是纯名额损失。
func (p *Pool) deadRetention() int {
	if p.cfg.MaxDevices <= 0 {
		return p.cfg.MaxDead
	}
	room := p.cfg.MaxDevices - p.usableTarget()
	if room < 0 {
		room = 0
	}
	if room < p.cfg.MaxDead {
		return room
	}
	return p.cfg.MaxDead
}

// countRows 池内行数（GORM 默认作用域，**软删的行不占名额**——这也是手工清死号
// 用软删就能腾位的原因，见分支 S27）
func (p *Pool) countRows() (int64, error) {
	var n int64
	err := db.DB.Model(&models.PoolDevice{}).Where("pool = ?", p.Name()).Count(&n).Error
	return n, err
}

// countDead 池内 dead 行数（只给错误文案用，读数失败按 0 处理不影响判定）
func (p *Pool) countDead() int64 {
	var n int64
	db.DB.Model(&models.PoolDevice{}).Where("pool = ? AND status = ?", p.Name(), StatusDead).Count(&n)
	return n
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
	defer w.mu.Unlock()
	w.total++
	if isErr {
		w.errs++
	}
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

// —— 用量摊薄型（KindSpread）专用路径：见待办清单 P5 与 docs/数据源/号池方案-fq_hg会话池融入.md

// acquireSpreadLocked 在**全部可用号**上轮询取一个（调用方持锁）：
// 先把到期的冷却号放回可用、再补到目标数量（冷号转正不 Claim，缺号才建号）。
// 一个可用号都没有时报错，并把「为什么没有」一起带出——补号被总号数上限拦住，
// 与「这个池根本没建过号 / 全在冷却」是几种排查方向，混成一句话就等于没报。
func (p *Pool) acquireSpreadLocked() (*Device, error) {
	p.reapCooldownLocked()
	cerr := p.fillSpreadLocked()
	if len(p.hot) == 0 {
		// 三种「没有可用号」要分得开：没建过号、全在冷却、想补号却被上限拦住。
		// 后两种常同时发生（冷却到只剩零台又补不动），那就两条都报——只报上限会把人带偏。
		switch {
		case cerr != nil && len(p.cooldown) > 0:
			return nil, fmt.Errorf("号池 %s 无可用号：%d 个号全部在冷却，补号又被总号数上限拦住: %w",
				p.Name(), len(p.cooldown), cerr)
		case cerr != nil:
			return nil, fmt.Errorf("号池 %s 无可用号: %w", p.Name(), cerr)
		case len(p.cooldown) > 0:
			return nil, fmt.Errorf("号池 %s 无可用号：%d 个号全部在冷却", p.Name(), len(p.cooldown))
		default:
			return nil, fmt.Errorf("号池 %s 无可用号（还没建过号）", p.Name())
		}
	}
	p.hotRR = (p.hotRR + 1) % len(p.hot)
	return deviceOf(p.hot[p.hotRR]), nil
}

// fillSpreadLocked 把可用号补到 spreadTarget（调用方持锁），返回建号失败的原因（可为 nil）。
// 与燃烧型的 promoteLocked 关键差别：**不 Refresh、不 Claim**——摊薄型的号没有「领取」这一步，
// 打上游探活本身就是风控成本。
func (p *Pool) fillSpreadLocked() error {
	for len(p.hot) < p.spreadTarget() {
		if len(p.cold) == 0 {
			if _, err := p.createColdLocked(); err != nil {
				p.logf("spread: 补号失败: %v", err)
				return err
			}
		}
		if len(p.cold) == 0 {
			return nil
		}
		row := p.cold[0]
		p.removeLocked(&p.cold, row)
		p.setStatus(row, StatusHot)
		p.hot = append(p.hot, row)
		p.logf("spread: device %s 进入可用轮询（不 Claim）", row.Ident)
	}
	return nil
}

// reapCooldownLocked 把冷却到期的号放回可用轮询（调用方持锁）
func (p *Pool) reapCooldownLocked() {
	now := time.Now()
	var keep []*models.PoolDevice
	for _, row := range p.cooldown {
		if row.ExpireAt != nil && row.ExpireAt.After(now) {
			keep = append(keep, row)
			continue
		}
		row.Note = ""
		p.setStatus(row, StatusHot)
		p.hot = append(p.hot, row)
		p.logf("spread: device %s 冷却到期，放回可用轮询", row.Ident)
	}
	p.cooldown = keep
}

// Cooldown 把一个号标记为「临时不可用直到 until」，reason 进 Note 供面板排查。
// 这是摊薄型的失效表达口：源侧判定被风控时调它，而不是把号判死——
// 设备会话被冻一小时与账号被永久封禁不是一回事，混成 dead 会被 MaxDead 清掉。
// 燃烧型池调用它没有意义（那类池的不可用由额度与到期表达），照做但不额外分叉。
func (p *Pool) Cooldown(ident string, until time.Time, reason string) {
	if db.DB == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	row := p.rowByIdent(ident)
	if row == nil {
		p.logf("Cooldown: 池里没有号 %s", ident)
		return
	}
	p.removeLocked(&p.hot, row)
	p.removeLocked(&p.cold, row)
	p.removeLocked(&p.cooldown, row)
	if until.Sub(time.Now()) <= 0 {
		p.setStatus(row, StatusCold)
		p.cold = append(p.cold, row)
		return
	}
	t := until
	row.ExpireAt = &t
	row.Note = clipNote(reason)
	p.setStatus(row, StatusCooldown)
	p.cooldown = append(p.cooldown, row)
	p.logf("spread: device %s 冷却至 %s（原因：%s）", ident, until.Format(time.RFC3339), row.Note)
}

// UpdatePayload 写回某个号的嵌套凭证载荷（会话 cookie 刷新后回写这类场景）。
// 框架不解析内容，只负责落库。优先改**内存里那一份**再落库：只写库的话，
// Acquire 返回的仍是旧的内存行，症状是「回写了新 cookie，下一个请求又拿到过期的」，
// 且只有重启才自愈。库中号（spent/dead）不在内存里，退回按库取一份。
func (p *Pool) UpdatePayload(ident string, payload []byte) error {
	if db.DB == nil {
		return fmt.Errorf("数据库未就绪")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	row := p.memRowLocked(ident)
	if row == nil {
		if row = p.rowByIdent(ident); row == nil {
			return fmt.Errorf("号池 %s 中不存在号 %s", p.Name(), ident)
		}
	}
	row.Payload = string(payload)
	return db.DB.Select("Payload").Save(row).Error
}

// memRowLocked 在内存的三个集合里按标识找号（调用方持锁）
func (p *Pool) memRowLocked(ident string) *models.PoolDevice {
	for _, list := range [][]*models.PoolDevice{p.hot, p.cold, p.cooldown} {
		for _, d := range list {
			if d.Ident == ident {
				return d
			}
		}
	}
	return nil
}

// CooldownCount 当前冷却中的号数（spread 池的可观测口径：可用 = HotCount，冷却 = 这个数）
func (p *Pool) CooldownCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.cooldown)
}

// clipNote 原因落库限长：列宽按最坏输入定（255），但界面一行放不下长串
func clipNote(s string) string {
	r := []rune(s)
	if len(r) > 120 {
		return string(r[:120]) + "…"
	}
	return s
}
