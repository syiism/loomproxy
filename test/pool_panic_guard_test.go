package test

// 待办清单 P77 / P78：号池的两条**后台路径**都会调用源写的钩子（Provider.Create/Refresh/Claim），
// 而这两条都不在 gin Recovery 的覆盖范围内——一次 panic 等于整进程死，`Restart=always` 还会把
// 崩溃循环伪装成「运行中」（v63 换装三查第 3 条记的就是这个形状）。
// 同一原则在本仓早已落地两次：源启动钩子逐个 recover（base.RunSourceBoots）、
// 聚合搜索逐目标 recover（app.callSourceHandler）。号池这第三处是漏的。
//
// P78 是同一次巡检路上撞出来的另一条：`initLedger` 逐行探活时**不持锁**改 p.hot/p.cold，
// 而 `app.Run` 里 `go pool.StartAll()` 排在 `http.Server` 之前——端口已经开着、号池还在装填，
// 并发请求走的 `Acquire` 读到的是半装填的切片。

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"loomproxy/base/pool"
)

// lockFree 问一次「p.mu 现在拿得到吗」。HotCount 内部就是 Lock + defer Unlock，
// 拿不到即说明上一个持有者带着 panic 跑了出去、没人解锁——那比崩溃更难发现：
// 池从此不再维护，而进程活着、面板数字还在。
func lockFree(p *pool.Pool) bool {
	done := make(chan struct{})
	go func() {
		p.HotCount()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(3 * time.Second):
		return false
	}
}

// TestPoolMaintainSurvivesProviderPanic 维护 tick 里源实现 panic：拦下、放锁、下一轮照常。
func TestPoolMaintainSurvivesProviderPanic(t *testing.T) {
	newTestServer(t)
	fp := newFakeProvider()
	fp.claimExtend = time.Minute // 转正后即临期，每轮 Maintain 都会去刷新台账

	const ident = "efefefef-0000-0000-0000-0000000000f3"
	p := bringHotUp(t, fp, ident)

	spoken, text := capturePoolLog(t)
	fp.panicRefresh[ident] = true
	p.Maintain() // 修前：panic 直接掀掉整个测试二进制——这条用例的存在就是为了让它先红

	if n := spoken("panic 已拦下"); n != 1 {
		t.Errorf("源实现 panic 只该留下一条 ERROR，实得 %d 条\n日志：%s", n, text())
	}
	if !lockFree(p) {
		t.Fatal("panic 之后 p.mu 仍被持有：下一轮维护会永久死锁（临界区必须用 defer 解锁）")
	}

	// 拦下不等于坏掉：撤掉 panic 后，下一轮照常续领
	delete(fp.panicRefresh, ident)
	fp.quota[ident] = pool.Quota{Total: fakeQuotaMax, ExpiresAt: time.Now().Add(time.Hour)}
	fp.claimExtend = time.Hour
	p.Maintain()
	if n := spoken("renewed hot device"); n != 1 {
		t.Errorf("panic 之后下一轮没能正常续领（%d 条, want 1）——守卫把状态弄坏了\n日志：%s", n, text())
	}
}

// TestPoolStartSurvivesProviderPanic 启动路径（转正首个活跃号）panic：Start 必须自己返回，
// 而不是把 `go pool.StartAll()` 那个协程连同整个进程一起带走。
func TestPoolStartSurvivesProviderPanic(t *testing.T) {
	newTestServer(t)
	fp := newFakeProvider()

	const ident = "f0f0f0f0-0000-0000-0000-0000000000f4"
	insertDevice(t, ident, pool.StatusCold, 0, fakeQuotaMax, nil)
	fp.quota[ident] = pool.Quota{Total: fakeQuotaMax}
	fp.panicClaim[ident] = true // 启动时「转正首个活跃号」这一步就炸

	p := newFakePool(t, pool.New(fp, fakePoolConfig()))
	spoken, text := capturePoolLog(t)
	p.Start()

	if n := spoken("panic 已拦下"); n != 1 {
		t.Errorf("启动路径的 panic 只该留下一条 ERROR，实得 %d 条\n日志：%s", n, text())
	}
	if !lockFree(p) {
		t.Fatal("启动 panic 之后 p.mu 仍被持有：整个号池从此卡死")
	}
}

// raceProvider 是**自己带锁**的假源。这条用例要验的是「号池装填期与并发 Acquire 抢同一份池状态」，
// 如果 Provider 自己的 map 也在竞争，-race 报的就不是被测的那条通路上了
// （判据页「断言空转」第 ④ 形态：没经过那条通路的读数不是读数）。
type raceProvider struct {
	mu     sync.Mutex
	quota  map[string]pool.Quota
	delay  time.Duration
	called int
}

func (r *raceProvider) Name() string { return fakePoolName }

func (r *raceProvider) loaded() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.called
}

// waitForLoaded 等 initLedger 把 want 台探完。不能拿窗口边缘的读数去数——
// 上一版我就是这么把「22/24」读成"前提不成立"的。
func (r *raceProvider) waitForLoaded(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for r.loaded() < want && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
}

func (r *raceProvider) Create(_ context.Context) (*pool.Device, error) {
	return nil, fmt.Errorf("这条用例不该走到建号")
}

func (r *raceProvider) Refresh(_ context.Context, dev *pool.Device) (pool.Quota, error) {
	time.Sleep(r.delay) // 放大窗口：装填 N 台 = N × delay
	r.mu.Lock()
	defer r.mu.Unlock()
	r.called++
	if q, ok := r.quota[dev.Ident]; ok {
		return q, nil
	}
	return pool.Quota{}, fmt.Errorf("未预期的号 %q", dev.Ident)
}

func (r *raceProvider) Claim(_ context.Context, dev *pool.Device) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	q := r.quota[dev.Ident]
	q.ExpiresAt = time.Now().Add(time.Hour)
	q.Used++
	r.quota[dev.Ident] = q
	return nil
}

// TestPoolStopDuringStartIsSynchronized 优雅关停撞上「还在装填的启动」——P78 那条用例在整包跑时
// 撞出来的第二条（待办清单 P80）：`ticker` 原来是个普通字段，Start 写、Stop 读，而这两条路径真的重叠
// （lifecycle 的 StopAll 跑在关停协程，StartAll 是 `go` 出去的，装填还要逐行打上游）。
// 竞争之外还有泄漏：Stop 早于 Start 建 ticker 时，读到的 nil 让那个定时器永不被停。
//
// **这条用例的覆盖面必须写清**：它断言的是「Stop 之后不再有探活、而在途装填会跑完」这个握手性质，
// 它**不能确定性地复现那个数据竞争**——把 ticker 改回普通字段再单跑这条，它照样绿（实测过）。
// 竞争的凭据是整包 `-race` 报出来的那两条栈（待办清单 P80 记录了原文），与 P57 是同一种情形：
// **检测器只在时序撞对时出声，所以"单跑没报"不等于"没有"**，别把这条用例读成竞争的守卫。
func TestPoolStopDuringStartIsSynchronized(t *testing.T) {
	newTestServer(t)
	const rows = 8
	rp := &raceProvider{quota: map[string]pool.Quota{}, delay: 15 * time.Millisecond}
	soon := time.Now().Add(30 * time.Second) // 装填后每台都是临期号 → 每个 tick 都会去探活，泄漏才数得出来
	for i := 0; i < rows; i++ {
		ident := fmt.Sprintf("stopxy-%02d-0000-0000-0000-0000000000ff", i)
		insertDevice(t, ident, pool.StatusCold, 0, fakeQuotaMax, nil)
		rp.quota[ident] = pool.Quota{Total: fakeQuotaMax, ExpiresAt: soon}
	}

	cfg := fakePoolConfig()
	cfg.Interval = 60 * time.Millisecond // 让「停没停下来」可观测：没停就会每 60ms 探一次
	p := pool.New(rp, cfg)
	pool.Register(p)
	t.Cleanup(func() { p.Stop(); pool.Unregister(p.Name()) })

	go p.Start()
	time.Sleep(40 * time.Millisecond) // 装填还在中途（8 台 × 15ms）
	p.Stop()

	// 在途的那轮装填会跑完（它持有 p.mu，Stop 不抢），之后不该再有探活
	deadline := time.Now().Add(5 * time.Second)
	last, stable := rp.loaded(), 0
	for time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		if n := rp.loaded(); n == last {
			stable++
			if stable >= 5 {
				break // 连续 5 次读数不变 = 装填确实跑完了，不是采样太巧
			}
		} else {
			last, stable = n, 0
		}
	}
	before := rp.loaded()
	time.Sleep(300 * time.Millisecond) // ≈ 5 个 tick
	if after := rp.loaded(); after != before {
		t.Errorf("Stop 之后探活次数从 %d 涨到 %d——维护协程/定时器没停干净（关停期间还会打上游）", before, after)
	}
	if before < rows {
		t.Errorf("在途装填只跑了 %d/%d 台——用例没经过「Stop 早于 ticker 创建」那一支", before, rows)
	}
}

// 检测器只会说"撞上了"，所以另加三条硬断言钉住前提：窗口真的开过、装填真的跑完、池没有卡死。
// TestPoolInitLedgerConcurrentWithAcquire 启动装填期与并发 Acquire 的竞争——主断言交给 -race。
// 检测器只会说"撞上了"，所以另加三条硬断言钉住前提：窗口真的开过、装填真的跑完、池没有卡死。
func TestPoolInitLedgerConcurrentWithAcquire(t *testing.T) {
	newTestServer(t)
	const rows = 24
	rp := &raceProvider{quota: map[string]pool.Quota{}, delay: 15 * time.Millisecond}
	loadDone := time.Now().Add(time.Duration(rows) * rp.delay)
	for i := 0; i < rows; i++ {
		ident := fmt.Sprintf("racy-%03d-0000-0000-0000-0000000000fe", i)
		insertDevice(t, ident, pool.StatusCold, 0, fakeQuotaMax, nil)
		rp.quota[ident] = pool.Quota{Total: fakeQuotaMax, ExpiresAt: loadDone.Add(time.Hour)}
	}

	attempts := 0
	p := newFakePool(t, pool.New(rp, fakePoolConfig()))
	go p.Start()
	// 窗口信号：Start 一进来就把 running 置真，而 initLedger 还在逐行探活——
	// 这正是「端口已开、号池未装填完」的形状（app.go: `go pool.StartAll()` 在 http.Server 之前）
	for !p.Running() {
		runtime.Gosched()
	}
	for time.Now().Before(loadDone) {
		attempts++
		if _, err := p.Acquire(); err != nil {
			time.Sleep(time.Millisecond)
		}
	}

	rp.waitForLoaded(t, rows)
	if !lockFree(p) {
		t.Fatal("并发 Acquire 之后 p.mu 拿不到——装填路径没有与请求路径互斥")
	}
	if called := rp.loaded(); called < rows {
		t.Errorf("initLedger 只探活了 %d 台, want ≥%d——装填没跑完，上面的并发窗口不成立", called, rows)
	}
	if attempts == 0 {
		t.Error("装填期间一次 Acquire 都没发生——这条用例没经过被测通路（断言空转）")
	}
}
