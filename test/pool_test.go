package test

// 通用号池（base/pool）黑盒测试：用内存假 Provider 驱动冷热状态机，
// 覆盖转正/领取/续领、spent 复活逐个尝试、到期自愈换号、错误驱动扩容、状态快照脱敏。
// 号池框架与上游协议无关，用例不需要任何真实数据源。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"loomproxy/base/pool"
	"loomproxy/db"
	"loomproxy/models"
)

const (
	fakePoolName  = "fakepool"
	fakeQuotaMax  = 4
	fakeClaimHour = time.Hour
)

var errResourceExpired = errors.New("资源到期")

// fakeProvider 假号源：额度与有效期是用例可编排的内存状态
type fakeProvider struct {
	quota       map[string]pool.Quota // ident → 上游真实额度
	failRefresh map[string]bool       // ident → 刷新失败（模拟上游故障/风控）
	claimExtend time.Duration         // 单次领取延长的有效期（零值=1 小时）
	created     int
	claims      map[string]int
	refreshes   map[string]int // ident → 被探活次数（摊薄型必须恒为 0）
	poolName    string         // 非空则覆盖池名：一条用例里建多个假池时避免 (pool, ident) 唯一索引撞车
}

func newFakeProvider() *fakeProvider {
	return &fakeProvider{
		quota:       map[string]pool.Quota{},
		failRefresh: map[string]bool{},
		claims:      map[string]int{},
		refreshes:   map[string]int{},
	}
}

func (f *fakeProvider) Name() string {
	if f.poolName != "" {
		return f.poolName
	}
	return fakePoolName
}

func (f *fakeProvider) Create(_ context.Context) (*pool.Device, error) {
	f.created++
	ident := fmt.Sprintf("dev-%06d-0000-0000-000000000001", f.created)
	f.quota[ident] = pool.Quota{Total: fakeQuotaMax}
	return &pool.Device{Ident: ident, Attrs: map[string]string{"sn": "sn-" + ident}}, nil
}

func (f *fakeProvider) Refresh(_ context.Context, dev *pool.Device) (pool.Quota, error) {
	f.refreshes[dev.Ident]++
	if f.failRefresh[dev.Ident] {
		return pool.Quota{}, errors.New("模拟上游故障")
	}
	q, ok := f.quota[dev.Ident]
	if !ok {
		return pool.Quota{}, fmt.Errorf("未预期的号 %q", dev.Ident)
	}
	return q, nil
}

// Claim 领取一次：次数 +1，有效期叠加 claimExtend（框架要求 Provider 的领取是无损叠加的）
func (f *fakeProvider) Claim(_ context.Context, dev *pool.Device) error {
	q := f.quota[dev.Ident]
	base := time.Now()
	if !q.ExpiresAt.IsZero() && q.ExpiresAt.After(base) {
		base = q.ExpiresAt
	}
	q.ExpiresAt = base.Add(f.claimDuration())
	q.Used++
	f.quota[dev.Ident] = q
	f.claims[dev.Ident]++
	return nil
}

func (f *fakeProvider) claimDuration() time.Duration {
	if f.claimExtend > 0 {
		return f.claimExtend
	}
	return fakeClaimHour
}

// classifyExpired 给假 Provider 加上「资源到期不计入限流信号」的错误分类
type classifyExpired struct{ *fakeProvider }

func (c classifyExpired) IsResourceExpired(err error) bool { return errors.Is(err, errResourceExpired) }

func fakePoolConfig() pool.Config {
	return pool.Config{
		ColdSpares:  1,
		MaxHot:      3,
		MaxDead:     10,
		RenewBefore: 5 * time.Minute,
		Interval:    time.Hour, // 用例手动驱动 Maintain，不让协程抢跑
	}
}

// newFakePool 构造并登记假池（用例结束自动注销）
func newFakePool(t *testing.T, p *pool.Pool) *pool.Pool {
	t.Helper()
	pool.Register(p)
	t.Cleanup(func() {
		p.Stop()
		pool.Unregister(p.Name())
	})
	return p
}

func insertDevice(t *testing.T, ident, status string, used, total int, expireAt *time.Time) {
	t.Helper()
	dev := models.PoolDevice{
		Pool: fakePoolName, Ident: ident, Status: status,
		TotalQuota: total, UsedQuota: used, ExpireAt: expireAt,
		Attrs: fmt.Sprintf(`{"sn":"sn-%s"}`, ident),
	}
	if err := db.DB.Create(&dev).Error; err != nil {
		t.Fatalf("预置号 %s 失败: %v", ident, err)
	}
}

func deviceStatus(t *testing.T, ident string) string {
	t.Helper()
	return deviceRow(t, fakePoolName, ident).Status
}

// deviceRow 取库里的一行原始记录（池名可变体时用；状态快照是脱敏的，验不了落库事实）
func deviceRow(t *testing.T, poolName, ident string) models.PoolDevice {
	t.Helper()
	var row models.PoolDevice
	if err := db.DB.Where(map[string]interface{}{"pool": poolName, "ident": ident}).
		First(&row).Error; err != nil {
		t.Fatalf("查询号 %s/%s 失败: %v", poolName, ident, err)
	}
	return row
}

// TestPoolAcquirePromotesAndClaims 空池取号：新建冷备 → 转正为活跃号并领取一次；
// 再次取号复用同一活跃号（活跃号可服务无限并发，不重复领取）
func TestPoolAcquirePromotesAndClaims(t *testing.T) {
	newTestServer(t)
	fp := newFakeProvider()
	p := pool.New(fp, fakePoolConfig())

	dev, err := p.Acquire()
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	if got := deviceStatus(t, dev.Ident); got != pool.StatusHot {
		t.Fatalf("号状态 = %q, want hot", got)
	}
	if fp.claims[dev.Ident] != 1 {
		t.Fatalf("领取次数 = %d, want 1", fp.claims[dev.Ident])
	}
	if p.HotCount() != 1 || p.ColdCount() != 0 {
		t.Fatalf("hot=%d cold=%d, want 1/0", p.HotCount(), p.ColdCount())
	}

	dev2, err := p.Acquire()
	if err != nil || dev2.Ident != dev.Ident {
		t.Fatalf("第二次 Acquire = %+v err=%v, want 复用同一活跃号", dev2, err)
	}
	if fp.claims[dev.Ident] != 1 {
		t.Fatalf("复用活跃号却再次领取（%d 次）", fp.claims[dev.Ident])
	}
}

// TestPoolReviveSpentIteratesAll spent 复活逐个尝试：
// 未重置的跳过、刷新失败的判死并继续，直至复活一个已重置的号。
// 修复前只试主键最小的号，其未重置即放弃转新建——其余 spent 号永不复活。
func TestPoolReviveSpentIteratesAll(t *testing.T) {
	newTestServer(t)

	const a, b, c = "aaaaaaaa-0000-0000-0000-0000000000a1",
		"bbbbbbbb-0000-0000-0000-0000000000b2",
		"cccccccc-0000-0000-0000-0000000000c3"
	insertDevice(t, a, pool.StatusSpent, fakeQuotaMax, fakeQuotaMax, nil)
	insertDevice(t, b, pool.StatusSpent, fakeQuotaMax, fakeQuotaMax, nil)
	insertDevice(t, c, pool.StatusSpent, fakeQuotaMax, fakeQuotaMax, nil)

	fp := newFakeProvider()
	fp.quota[a] = pool.Quota{Total: fakeQuotaMax, Used: fakeQuotaMax} // 周期未重置
	fp.quota[b] = pool.Quota{Total: fakeQuotaMax}
	fp.quota[c] = pool.Quota{Total: fakeQuotaMax} // 周期已重置
	fp.failRefresh[b] = true

	p := pool.New(fp, fakePoolConfig())
	p.Maintain() // 冷备水位不足 → 复活 spent

	if p.ColdCount() != 1 {
		t.Fatalf("冷备数 = %d, want 1（未逐个尝试 spent 号？）", p.ColdCount())
	}
	if got := deviceStatus(t, a); got != pool.StatusSpent {
		t.Fatalf("未重置号状态 = %q, want spent", got)
	}
	if got := deviceStatus(t, b); got != pool.StatusDead {
		t.Fatalf("刷新失败号状态 = %q, want dead", got)
	}
	if got := deviceStatus(t, c); got != pool.StatusCold {
		t.Fatalf("已重置号状态 = %q, want cold", got)
	}
	if fp.created != 0 {
		t.Fatalf("复活成功却仍新建了冷备（created=%d）", fp.created)
	}
}

// TestPoolReviveSpentNoneReset 全部 spent 号均未重置时不复活（转而新建冷备）
func TestPoolReviveSpentNoneReset(t *testing.T) {
	newTestServer(t)
	const d = "dddddddd-0000-0000-0000-0000000000d4"
	insertDevice(t, d, pool.StatusSpent, fakeQuotaMax, fakeQuotaMax, nil)

	fp := newFakeProvider()
	fp.quota[d] = pool.Quota{Total: fakeQuotaMax, Used: fakeQuotaMax}

	p := pool.New(fp, fakePoolConfig())
	p.Maintain()

	if got := deviceStatus(t, d); got != pool.StatusSpent {
		t.Fatalf("未重置号被误复活：状态 = %q", got)
	}
	if fp.created != 1 {
		t.Fatalf("新建冷备数 = %d, want 1", fp.created)
	}
}

// TestPoolRenewHotBeforeExpiry 活跃号临期时原地续领（叠加无损），不换号。
// 假 Provider 每次领取只给 1 分钟有效期（低于 RenewBefore 阈值），
// 因此转正后的活跃号在下一轮巡检即触发续领。
func TestPoolRenewHotBeforeExpiry(t *testing.T) {
	newTestServer(t)
	fp := newFakeProvider()
	fp.claimExtend = time.Minute
	p := pool.New(fp, fakePoolConfig())

	dev, err := p.Acquire()
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	if fp.claims[dev.Ident] != 1 {
		t.Fatalf("转正时领取次数 = %d, want 1", fp.claims[dev.Ident])
	}

	p.Maintain()

	if fp.claims[dev.Ident] != 2 {
		t.Fatalf("临期活跃号领取次数 = %d, want 2（续领未发生）", fp.claims[dev.Ident])
	}
	if p.HotCount() != 1 {
		t.Fatalf("hot=%d, want 1（续领不应换号）", p.HotCount())
	}
}

// TestPoolRetireSpentHotAndSwitch 活跃号周期用尽：退役 spent 并转正冷备替补
func TestPoolRetireSpentHotAndSwitch(t *testing.T) {
	newTestServer(t)
	fp := newFakeProvider()
	fp.claimExtend = time.Minute // 转正后即为临期，巡检会去刷新真实额度

	const hotIdent, spareIdent = "eeeeeeee-0000-0000-0000-0000000000e1",
		"ffffffff-0000-0000-0000-0000000000e2"
	insertDevice(t, hotIdent, pool.StatusCold, 0, fakeQuotaMax, nil)
	insertDevice(t, spareIdent, pool.StatusCold, 0, fakeQuotaMax, nil)
	fp.quota[hotIdent] = pool.Quota{Total: fakeQuotaMax}
	fp.quota[spareIdent] = pool.Quota{Total: fakeQuotaMax}

	p := newFakePool(t, pool.New(fp, fakePoolConfig()))
	p.Start() // 从库存量分类 → 转正一个活跃号 → 补齐冷备

	if p.HotCount() != 1 {
		t.Fatalf("启动后 hot=%d, want 1", p.HotCount())
	}
	if got := deviceStatus(t, hotIdent); got != pool.StatusHot {
		t.Fatalf("首个转正的活跃号 = %q, want hot（用例前提不成立）", got)
	}

	// 让活跃号周期用满（有效期仍是临期值，促使巡检去刷新它）
	q := fp.quota[hotIdent]
	q.Used = fakeQuotaMax
	fp.quota[hotIdent] = q

	p.Maintain()

	if got := deviceStatus(t, hotIdent); got != pool.StatusSpent {
		t.Fatalf("用尽号状态 = %q, want spent", got)
	}
	if got := deviceStatus(t, spareIdent); got != pool.StatusHot {
		t.Fatalf("替补冷备状态 = %q, want hot", got)
	}
	if p.HotCount() != 1 {
		t.Fatalf("hot=%d, want 1（替补未转正）", p.HotCount())
	}
}

// TestPoolReauthorizeSwitchesDevice 资源到期自愈：可补领则同号补领，周期用尽则换号
func TestPoolReauthorizeSwitchesDevice(t *testing.T) {
	newTestServer(t)
	fp := newFakeProvider()
	p := pool.New(fp, fakePoolConfig())

	first, err := p.Acquire()
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}

	// 仍有余量 → 同号补领
	got, err := p.Reauthorize(first)
	if err != nil || got == nil || got.Ident != first.Ident {
		t.Fatalf("补领自愈 = %+v err=%v, want 同标识", got, err)
	}

	// 周期用尽 → 退役换号
	q := fp.quota[first.Ident]
	q.Used = q.Total
	fp.quota[first.Ident] = q

	second, err := p.Reauthorize(first)
	if err != nil {
		t.Fatalf("换号自愈失败: %v", err)
	}
	if second == nil || second.Ident == first.Ident {
		t.Fatalf("换号自愈返回 = %+v, want 新号", second)
	}
	if got := deviceStatus(t, first.Ident); got != pool.StatusSpent {
		t.Fatalf("原号状态 = %q, want spent", got)
	}
	if got := deviceStatus(t, second.Ident); got != pool.StatusHot {
		t.Fatalf("新号状态 = %q, want hot", got)
	}
}

// TestPoolErrorDrivenExpansion 错误率超阈值时扩容活跃号；
// Provider 判定为「资源到期」的错误不计入（走自愈而非扩容）
func TestPoolErrorDrivenExpansion(t *testing.T) {
	newTestServer(t)
	fp := newFakeProvider()
	p := pool.New(classifyExpired{fp}, fakePoolConfig())

	if _, err := p.Acquire(); err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}

	for i := 0; i < 30; i++ {
		p.Report(errResourceExpired)
	}
	p.Maintain()
	if p.HotCount() != 1 {
		t.Fatalf("资源到期错误触发扩容：hot=%d, want 1", p.HotCount())
	}

	for i := 0; i < 30; i++ {
		p.Report(errors.New("上游返回 429"))
	}
	p.Maintain()
	if p.HotCount() != 2 {
		t.Fatalf("疑似限流未触发扩容：hot=%d, want 2", p.HotCount())
	}
}

// TestPoolStatusMasksIdent 状态快照：标识脱敏、凭证只列键名不列值
func TestPoolStatusMasksIdent(t *testing.T) {
	newTestServer(t)
	const ident = "ffffffff-0000-0000-0000-0000000000ff"
	insertDevice(t, ident, pool.StatusCold, 0, fakeQuotaMax, nil)

	fp := newFakeProvider()
	p := newFakePool(t, pool.New(fp, fakePoolConfig()))

	st := p.Status()
	if st.Running {
		t.Fatal("未启动的池不应报告 Running")
	}
	if st.Counts[pool.StatusCold] != 1 {
		t.Fatalf("counts[cold] = %d, want 1", st.Counts[pool.StatusCold])
	}
	if len(st.Devices) != 1 {
		t.Fatalf("号明细数 = %d, want 1", len(st.Devices))
	}
	d := st.Devices[0]
	if d.Ident == ident {
		t.Fatalf("标识未脱敏: %q", d.Ident)
	}
	if len(d.Creds) != 1 || d.Creds[0] != "sn" {
		t.Fatalf("凭证键名 = %v, want [sn]", d.Creds)
	}

	// 已启动的池必须报告 Running=true：面板「号池」页据此渲染「运行中/未启动」，
	// Status() 早先漏了这个字段的赋值，运行中的池也恒显示未启动
	p.Start()
	st = p.Status()
	if !st.Running {
		t.Fatal("已启动的池应报告 Running=true")
	}
	if st.Config.MaintainSec == 0 {
		t.Errorf("运行中的池应带出运行参数，实得 %+v", st.Config)
	}
}

// TestPrioritizeClaimedDevices 转正候选排序：已领取且在有效期内的冷备优先，
// 越早到期越优先转正；未领取与已过期的保持原顺序在后
func TestPrioritizeClaimedDevices(t *testing.T) {
	now := time.Now()
	expSoon := now.Add(30 * time.Minute)
	expLate := now.Add(2 * time.Hour)
	expPast := now.Add(-time.Hour)

	got := pool.PrioritizeClaimed([]*models.PoolDevice{
		{Ident: "fresh-1"},
		{Ident: "claimed-late", UsedQuota: 1, ExpireAt: &expLate},
		{Ident: "fresh-2"},
		{Ident: "claimed-soon", UsedQuota: 1, ExpireAt: &expSoon},
		{Ident: "expired-claimed", UsedQuota: 1, ExpireAt: &expPast},
	})

	want := []string{"claimed-soon", "claimed-late", "fresh-1", "fresh-2", "expired-claimed"}
	if len(got) != len(want) {
		t.Fatalf("候选数 = %d, want %d", len(got), len(want))
	}
	for i, ident := range want {
		if got[i].Ident != ident {
			t.Fatalf("第 %d 位 = %s, want %s", i, got[i].Ident, ident)
		}
	}
}

// TestPoolMaxDevicesGate 池级总号数上限（待办清单 P4）：建号对上游是不可逆增长，
// 这道闸门该在框架里，而不是每个会建号的源各自数一遍 pool_devices。
func TestPoolMaxDevicesGate(t *testing.T) {
	newTestServer(t)

	// 假 Provider 的 ident 是确定性的，池名默认固定——一条用例里建三个池会在 (pool, ident)
	// 唯一索引上撞车，所以给每个池换个名字

	// 1) 已到顶：池里唯一的号是 dead，Acquire 需要补号，补号被上限判住，且错误可 errors.Is 判定
	prov := newFakeProvider()
	cfg := fakePoolConfig()
	cfg.ColdSpares = 1
	cfg.MaxDevices = 1
	prov.poolName = "gate_cap"
	p := newFakePool(t, pool.New(prov, cfg))
	if err := db.DB.Create(&models.PoolDevice{
		Pool: p.Name(), Ident: "已判死的号", Status: pool.StatusDead,
	}).Error; err != nil {
		t.Fatalf("预置 dead 号失败: %v", err)
	}
	if _, err := p.Acquire(); !errors.Is(err, pool.ErrCapacityReached) {
		t.Fatalf("补号应被总号数上限判住并给出 ErrCapacityReached，实得 %v", err)
	}
	if prov.created != 0 {
		t.Errorf("到顶后仍向上游建了 %d 个号（应在建号前就判住）", prov.created)
	}

	// 2) 补冷备时被拦在门外：ColdSpares 想建 5 个，池内总数只到 2
	prov2 := newFakeProvider()
	cfg2 := fakePoolConfig()
	cfg2.ColdSpares = 5
	cfg2.MaxDevices = 2
	prov2.poolName = "gate_topup"
	p2 := newFakePool(t, pool.New(prov2, cfg2))
	if _, err := p2.Acquire(); err != nil {
		t.Fatalf("首次 Acquire 失败: %v", err)
	}
	p2.Maintain()
	var rows int64
	if err := db.DB.Model(&models.PoolDevice{}).Where("pool = ?", p2.Name()).Count(&rows).Error; err != nil {
		t.Fatalf("数池内行失败: %v", err)
	}
	if rows > 2 || prov2.created > 2 {
		t.Errorf("池内 %d 行、上游建号 %d 次，都不该越过 MaxDevices=2", rows, prov2.created)
	}
	if rows < 1 {
		t.Errorf("上限判过了头：池内一个号都没有")
	}

	// 3) 0 = 不限：同配置不设上限时，冷备照 ColdSpares 补齐（这条防的是「零值被当成上限」）
	prov3 := newFakeProvider()
	cfg3 := fakePoolConfig()
	cfg3.ColdSpares = 3
	cfg3.MaxDevices = 0
	prov3.poolName = "gate_uncapped"
	p3 := newFakePool(t, pool.New(prov3, cfg3))
	if _, err := p3.Acquire(); err != nil {
		t.Fatalf("不设上限时 Acquire 失败: %v", err)
	}
	p3.Maintain()
	if n := p3.HotCount() + p3.ColdCount(); n < 3 {
		t.Errorf("MaxDevices=0 应不限：池内只有 %d 个号，而冷备目标是 3", n)
	}

	// 4) 快照要带出上限，且未启动的池也带（面板读到全零 config 会误判成「没配」）
	if got := p2.Status().Config.MaxDevices; got != 2 {
		t.Errorf("快照 max_devices = %d，want 2", got)
	}
	if p2.Running() {
		t.Errorf("这条断言的前提是本池未 Start，Running 应为 false")
	}
}

// TestPoolStatusCarriesKind 池形态声明位（待办清单 P6）：快照必须把形态带出去——
// 「用量摊薄型」池（P5）里一排 cold/hot 号会被管理员读成「池没工作」，标签是唯一的消歧手段。
// 这里同时钉住 /admin/pools 的 JSON 里有 kind。
func TestPoolStatusCarriesKind(t *testing.T) {
	srv := newTestServer(t)

	fp := newFakeProvider()
	fp.poolName = "gate_kind_unset"
	p := newFakePool(t, pool.New(fp, fakePoolConfig()))
	if got := p.Status().Config.Kind; got != "" {
		t.Errorf("未声明形态时应留空（面板把空值读成墙钟燃烧型），实得 %q", got)
	}

	fp2 := newFakeProvider()
	fp2.poolName = "gate_kind_burn"
	cfg2 := fakePoolConfig()
	cfg2.Kind = pool.KindBurnWallClock
	p2 := newFakePool(t, pool.New(fp2, cfg2))
	if got := p2.Status().Config.Kind; got != pool.KindBurnWallClock {
		t.Errorf("快照 kind = %q，want %q", got, pool.KindBurnWallClock)
	}

	// 未启动的池也必须带出 kind 与上限：面板读到全零 config 会误判成「没配」
	if p2.Running() {
		t.Fatalf("本用例的前提是池未 Start")
	}

	_, env := doJSON(t, srv, "GET", "/admin/pools", nil, authHeader(adminToken(t, srv)))
	var pools []struct {
		Name   string `json:"name"`
		Config struct {
			Kind       string `json:"kind"`
			MaxDevices int    `json:"max_devices"`
		} `json:"config"`
	}
	if err := json.Unmarshal(env.Data, &pools); err != nil {
		t.Fatalf("解析 /admin/pools 失败: %v", err)
	}
	found := map[string]string{}
	for _, one := range pools {
		found[one.Name] = one.Config.Kind
	}
	if found["gate_kind_burn"] != pool.KindBurnWallClock {
		t.Errorf("/admin/pools 里 gate_kind_burn 的 kind = %q，want %q（全部：%+v）",
			found["gate_kind_burn"], pool.KindBurnWallClock, found)
	}
}

// —— 用量摊薄型（KindSpread，待办清单 P5）——
// 这类池的号不因墙钟过期、燃烧速度取决于请求量：框架只建号，不领取、不探活，
// 请求摊到全部可用号上，被风控的号临时冷却而非判死。

func spreadConfig() pool.Config {
	c := fakePoolConfig()
	c.Kind = pool.KindSpread
	c.TargetDevices = 3
	c.MaxDevices = 3
	return c
}

func countAll(m map[string]int) (n int) {
	for _, v := range m {
		n += v
	}
	return n
}

// TestPoolSpreadAcquireRoundRobin 取号在全部可用号上轮询，且全程不碰 Claim/Refresh
// （打上游探活本身就是风控成本，这是两种形态最关键的分叉点）
func TestPoolSpreadAcquireRoundRobin(t *testing.T) {
	newTestServer(t)
	fp := newFakeProvider()
	fp.poolName = "spread_rr"
	p := newFakePool(t, pool.New(fp, spreadConfig()))

	seen := map[string]int{}
	for i := 0; i < 9; i++ {
		dev, err := p.Acquire()
		if err != nil {
			t.Fatalf("第 %d 次 Acquire 失败: %v", i, err)
		}
		seen[dev.Ident]++
	}
	if len(seen) != 3 {
		t.Fatalf("9 次取号只命中 %d 个号，want 3（请求没摊开，仍在打同一台）", len(seen))
	}
	for ident, n := range seen {
		if n != 3 {
			t.Errorf("号 %s 命中 %d 次，want 3（轮询不均）", ident, n)
		}
	}
	if p.HotCount() != 3 || p.ColdCount() != 0 {
		t.Errorf("hot=%d cold=%d, want 3/0（摊薄型可用号即全部号，不该留冷备）", p.HotCount(), p.ColdCount())
	}
	if got := countAll(fp.claims); got != 0 {
		t.Errorf("摊薄型仍领取了 %d 次（Claim 只属于燃烧型契约）", got)
	}
	if got := countAll(fp.refreshes); got != 0 {
		t.Errorf("摊薄型仍探活了 %d 次（Refresh 只属于燃烧型契约）", got)
	}
}

// TestPoolSpreadCooldownExcludesThenReaps 冷却的号立即退出轮询、原因落库、到期后自动放回
func TestPoolSpreadCooldownExcludesThenReaps(t *testing.T) {
	newTestServer(t)
	fp := newFakeProvider()
	fp.poolName = "spread_cool"
	cfg := spreadConfig()
	cfg.TargetDevices, cfg.MaxDevices = 2, 2
	p := newFakePool(t, pool.New(fp, cfg))

	first, err := p.Acquire()
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	second, err := p.Acquire()
	if err != nil || second.Ident == first.Ident {
		t.Fatalf("第二次 Acquire = %+v err=%v, want 另一台", second, err)
	}

	p.Cooldown(first.Ident, time.Now().Add(30*time.Millisecond), "上游 429，冷却一轮")
	if p.CooldownCount() != 1 {
		t.Fatalf("冷却数 = %d, want 1", p.CooldownCount())
	}
	row := deviceRow(t, fp.Name(), first.Ident)
	if row.Status != pool.StatusCooldown {
		t.Errorf("冷却号落库状态 = %q, want cooldown", row.Status)
	}
	if row.Note != "上游 429，冷却一轮" {
		t.Errorf("冷却原因未落库: %q", row.Note)
	}

	for i := 0; i < 6; i++ {
		dev, err := p.Acquire()
		if err != nil {
			t.Fatalf("冷却期间 Acquire 失败: %v", err)
		}
		if dev.Ident == first.Ident {
			t.Fatalf("冷却中的号仍被取出（第 %d 次）", i)
		}
	}

	time.Sleep(60 * time.Millisecond)
	p.Maintain()
	if p.CooldownCount() != 0 {
		t.Fatalf("冷却到期后仍占着冷却集合：%d", p.CooldownCount())
	}
	if got := deviceRow(t, fp.Name(), first.Ident).Status; got != pool.StatusHot {
		t.Errorf("复归号状态 = %q, want hot", got)
	}
	if p.HotCount() != 2 {
		t.Errorf("hot=%d, want 2（复归的号没回到轮询集合）", p.HotCount())
	}
}

// TestPoolSpreadReauthorizeCoolsInsteadOfSwitching 兜底自愈路径：摊薄型没有「补领」可做，
// 把这台冻结一段（默认 10 分钟）另取一台——绝不能退化成燃烧型的 spent/换号，那会把号池掏空
func TestPoolSpreadReauthorizeCoolsInsteadOfSwitching(t *testing.T) {
	newTestServer(t)
	fp := newFakeProvider()
	fp.poolName = "spread_reauth"
	cfg := spreadConfig()
	cfg.TargetDevices, cfg.MaxDevices = 2, 2
	p := newFakePool(t, pool.New(fp, cfg))
	if got := p.Status().Config.CooldownDefSec; got != 600 {
		t.Fatalf("spread 池未显式声明冷却时长时应回填 600s，实得 %d", got)
	}

	dev, err := p.Acquire()
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	got, err := p.Reauthorize(dev)
	if err != nil {
		t.Fatalf("Reauthorize 失败: %v", err)
	}
	if got == nil || got.Ident == dev.Ident {
		t.Fatalf("Reauthorize 返回 = %+v, want 另一台", got)
	}
	if status := deviceRow(t, fp.Name(), dev.Ident).Status; status != pool.StatusCooldown {
		t.Errorf("被判不可用的号状态 = %q, want cooldown（被当成 spent 了？）", status)
	}
	if countAll(fp.claims) != 0 {
		t.Errorf("兜底路径仍发生了领取：%+v", fp.claims)
	}
}

// TestPoolSpreadStartWithoutUpstreamProbe 启动装载存量号：一次上游请求都不发；
// 冷却未到期继续冷却，冷却已到期归回可用
func TestPoolSpreadStartWithoutUpstreamProbe(t *testing.T) {
	newTestServer(t)
	fp := newFakeProvider()
	fp.poolName = "spread_start"
	cooling := "aaaaaaaa-0000-0000-0000-0000000000c1"
	reaped := "aaaaaaaa-0000-0000-0000-0000000000c2"
	future, past := time.Now().Add(time.Hour), time.Now().Add(-time.Hour)
	for _, one := range []models.PoolDevice{
		{Pool: fp.Name(), Ident: "aaaaaaaa-0000-0000-0000-0000000000c0", Status: pool.StatusCold},
		{Pool: fp.Name(), Ident: cooling, Status: pool.StatusCooldown, ExpireAt: &future, Note: "还没到点"},
		{Pool: fp.Name(), Ident: reaped, Status: pool.StatusCooldown, ExpireAt: &past},
	} {
		if err := db.DB.Create(&one).Error; err != nil {
			t.Fatalf("预置号 %s 失败: %v", one.Ident, err)
		}
	}

	p := newFakePool(t, pool.New(fp, spreadConfig()))
	p.Start()

	if got := countAll(fp.refreshes); got != 0 {
		t.Errorf("启动时探活 %d 次，want 0", got)
	}
	if got := countAll(fp.claims); got != 0 {
		t.Errorf("启动时领取 %d 次，want 0", got)
	}
	if got := fp.created; got != 0 {
		t.Errorf("存量号已够用时仍建号 %d 个", got)
	}
	if p.CooldownCount() != 1 {
		t.Errorf("冷却数 = %d, want 1（未到期那条该继续冷却）", p.CooldownCount())
	}
	if p.HotCount() != 2 {
		t.Errorf("可用数 = %d, want 2（cold + 冷却到期的那条）", p.HotCount())
	}
	if got := deviceRow(t, fp.Name(), reaped).Status; got != pool.StatusHot {
		t.Errorf("冷却到期号状态 = %q, want hot（内存归位了但库里没改，重启会反复）", got)
	}
}

// TestPoolSpreadFillRespectsMaxDevices 补号到目标数受总号数上限约束：TargetDevices 越不过 MaxDevices
func TestPoolSpreadFillRespectsMaxDevices(t *testing.T) {
	newTestServer(t)
	fp := newFakeProvider()
	fp.poolName = "spread_cap"
	cfg := spreadConfig()
	cfg.TargetDevices, cfg.MaxDevices = 5, 2
	p := newFakePool(t, pool.New(fp, cfg))

	for i := 0; i < 10; i++ {
		if _, err := p.Acquire(); err != nil {
			t.Fatalf("第 %d 次 Acquire 失败: %v", i, err)
		}
	}
	p.Maintain()
	if p.HotCount() != 2 {
		t.Errorf("可用数 = %d, want 2（越过了 MaxDevices=2）", p.HotCount())
	}
	if fp.created != 2 {
		t.Errorf("上游建号 %d 次, want 2", fp.created)
	}
}

// TestPoolSpreadPayloadAndSnapshot 嵌套凭证走 Payload（Attrs 是扁平表，塞嵌套会被静默丢弃），
// 快照只出第一层键名与冷却原因，值一律不出接口
func TestPoolSpreadPayloadAndSnapshot(t *testing.T) {
	srv := newTestServer(t)
	fp := newFakeProvider()
	fp.poolName = "spread_payload"
	cfg := spreadConfig()
	cfg.TargetDevices, cfg.MaxDevices = 2, 2
	p := newFakePool(t, pool.New(fp, cfg))

	dev, err := p.Acquire()
	if err != nil {
		t.Fatalf("Acquire 失败: %v", err)
	}
	// 嵌套凭证进 Payload：Attrs 是扁平 string 表，塞结构体进去会被 decodeAttrs 静默丢弃
	const secret = "supersensitive-cookie-value"
	nested, err := json.Marshal(map[string]map[string]string{
		"user": {"token": secret, "device_id": "d-1"},
	})
	if err != nil {
		t.Fatalf("构造嵌套凭证失败: %v", err)
	}
	if err := p.UpdatePayload(dev.Ident, nested); err != nil {
		t.Fatalf("UpdatePayload 失败: %v", err)
	}
	// 必须回库里读一遍：运行中的池快照取的是内存里那同一批行指针，
	// 只看快照会让一次失败的 UPDATE 蒙混过关（症状是重启后凭证全丢）
	if got := deviceRow(t, fp.Name(), dev.Ident).Payload; got != string(nested) {
		t.Fatalf("UpdatePayload 未落库：库里的 payload = %q", got)
	}
	// 取号也要立刻拿到新载荷：Acquire 返回的是内存里那一份行，只写库等于池继续发旧凭证
	back := false
	for i := 0; i < 3 && !back; i++ {
		got, err := p.Acquire()
		if err != nil {
			t.Fatalf("回写后 Acquire 失败: %v", err)
		}
		if got.Ident == dev.Ident {
			back = true
			if string(got.Payload) != string(nested) {
				t.Fatalf("Acquire 仍返回旧载荷（内存行没跟着更新）：%q", got.Payload)
			}
		}
	}
	if !back {
		t.Fatal("三轮轮询都没取回刚回写载荷的那个号")
	}
	p.Cooldown(dev.Ident, time.Now().Add(time.Hour), "设备被冻一小时")

	st := p.Status()
	if st.Config.Kind != pool.KindSpread || st.Config.TargetDevices != 2 {
		t.Errorf("spread 配置未带出快照：%+v", st.Config)
	}
	if st.Counts[pool.StatusCooldown] != 1 {
		t.Errorf("counts[cooldown] = %d, want 1", st.Counts[pool.StatusCooldown])
	}
	var found *pool.DeviceInfo
	for i := range st.Devices {
		if st.Devices[i].Status == pool.StatusCooldown {
			found = &st.Devices[i]
		}
	}
	if found == nil {
		t.Fatalf("快照里没有冷却中的号：%+v", st.Devices)
	}
	if len(found.PayloadKeys) != 1 || found.PayloadKeys[0] != "user" {
		t.Errorf("payload_keys = %v, want [user]", found.PayloadKeys)
	}
	if found.Note != "设备被冻一小时" {
		t.Errorf("note = %q, want 冷却原因", found.Note)
	}
	if found.Ident == dev.Ident {
		t.Errorf("冷却号标识未脱敏: %q", found.Ident)
	}

	_, env := doJSON(t, srv, "GET", "/admin/pools", nil, authHeader(adminToken(t, srv)))
	if raw := string(env.Data); strings.Contains(raw, secret) {
		t.Errorf("/admin/pools 泄出了载荷值")
	} else if !strings.Contains(raw, `"payload_keys"`) {
		t.Errorf("/admin/pools 未带出 payload_keys：%s", raw)
	}
}
