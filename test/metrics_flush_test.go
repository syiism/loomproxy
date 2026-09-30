package test

// base/metrics.go 落库行为测试。

import (
	"sync"
	"testing"
	"time"

	"loomproxy-go/base"
)

// TestDrainWaitsInflightFlush 回归：淘汰批（50 条）异步落库飞行中时，
// DrainRecentCalls 必须等待其完成——否则关停瞬间该批明细永久丢失
// （生产实测：重启后 lifetime_total 少了恰好 50）
func TestDrainWaitsInflightFlush(t *testing.T) {
	base.ResetMetrics()

	flusherStarted := make(chan struct{})
	flusherRelease := make(chan struct{})
	var mu sync.Mutex
	var flushed int

	base.SetMetricsFlusher(func(calls []base.RecentCall) {
		close(flusherStarted)
		<-flusherRelease // 模拟慢落库，制造飞行窗口
		mu.Lock()
		flushed += len(calls)
		mu.Unlock()
	})
	t.Cleanup(func() {
		base.SetMetricsFlusher(nil)
		base.ResetMetrics()
	})

	// 写满 250 条触发淘汰：最旧 50 条离环并启动异步落库（阻塞在 flusherRelease）
	for i := 0; i < 250; i++ {
		base.RecordCall("fake_c", "search", "", "1.2.3.4", 200, time.Millisecond)
	}
	<-flusherStarted

	// Drain 应当阻塞等待飞行中的落库，而不是立即返回
	drainDone := make(chan []base.RecentCall, 1)
	go func() { drainDone <- base.DrainRecentCalls() }()

	select {
	case <-drainDone:
		t.Fatal("Drain 未等待飞行中的落库（竞态仍存在）")
	case <-time.After(100 * time.Millisecond):
		// 符合预期：Drain 仍在等待
	}

	close(flusherRelease)
	drained := <-drainDone

	mu.Lock()
	defer mu.Unlock()
	if flushed != 50 {
		t.Fatalf("淘汰批落库条数 = %d, want 50", flushed)
	}
	if len(drained) != 200 {
		t.Fatalf("Drain 返回条数 = %d, want 200（250 - 淘汰 50）", len(drained))
	}
	// 关键不变量：淘汰落库 + Drain 兜底 = 全部 250 条，零丢失
	if flushed+len(drained) != 250 {
		t.Fatalf("落库总数 = %d, want 250（存在丢失）", flushed+len(drained))
	}
}

// TestFlushedCountsForLifetime 回归：会话聚合含已落库部分，
// FlushedCounts 须准确返回淘汰批计数供 lifetime 口径扣除（否则
// 重启前 lifetime_total 虚高 50 的倍数——生产两次"重启丢数"实为双计）
func TestFlushedCountsForLifetime(t *testing.T) {
	base.ResetMetrics()
	base.SetMetricsFlusher(func(calls []base.RecentCall) {})
	t.Cleanup(func() {
		base.SetMetricsFlusher(nil)
		base.ResetMetrics()
	})

	// 触发一次淘汰（250 条 = 200 保留 + 50 淘汰）
	for i := 0; i < 250; i++ {
		base.RecordCall("fake_c", "search", "", "1.2.3.4", 200, time.Millisecond)
	}

	flushed, total := base.FlushedCounts()
	if total != 50 {
		t.Fatalf("flushedTotal = %d, want 50", total)
	}
	if flushed["fake_c/search"] != 50 {
		t.Fatalf("flushed[fake_c/search] = %d, want 50", flushed["fake_c/search"])
	}

	base.ResetMetrics()
	if _, total := base.FlushedCounts(); total != 0 {
		t.Fatalf("ResetMetrics 后 flushedTotal = %d, want 0", total)
	}
}
