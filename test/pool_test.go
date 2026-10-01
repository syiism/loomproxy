package test

// 通用号池（base/pool）黑盒测试：用内存假 Provider 驱动冷热状态机，
// 覆盖转正/领取/续领、spent 复活逐个尝试、到期自愈换号、错误驱动扩容、状态快照脱敏。
// 号池框架与上游协议无关，用例不需要任何真实数据源。

import (
	"context"
	"errors"
	"fmt"
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
}

func newFakeProvider() *fakeProvider {
	return &fakeProvider{
		quota:       map[string]pool.Quota{},
		failRefresh: map[string]bool{},
		claims:      map[string]int{},
	}
}

func (f *fakeProvider) Name() string { return fakePoolName }

func (f *fakeProvider) Create(_ context.Context) (*pool.Device, error) {
	f.created++
	ident := fmt.Sprintf("dev-%06d-0000-0000-000000000001", f.created)
	f.quota[ident] = pool.Quota{Total: fakeQuotaMax}
	return &pool.Device{Ident: ident, Attrs: map[string]string{"sn": "sn-" + ident}}, nil
}

func (f *fakeProvider) Refresh(_ context.Context, dev *pool.Device) (pool.Quota, error) {
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
	var got string
	if err := db.DB.Model(&models.PoolDevice{}).
		Where("pool = ? AND ident = ?", fakePoolName, ident).
		Select("status").Scan(&got).Error; err != nil {
		t.Fatalf("查询号 %s 状态失败: %v", ident, err)
	}
	return got
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
