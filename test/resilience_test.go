package test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"loomproxy-go/base"
	"loomproxy-go/conf"
)

// setupConf 初始化测试用配置（conf.Config 为指针，未经 Load 时为 nil）
func setupConf(failures int, cooldown float64) {
	conf.Config = &conf.ConfMgr{
		TimeoutConnect:         5,
		TimeoutPool:            10,
		UpstreamCacheMaxSize:   512,
		CircuitBreakerEnabled:  true,
		CircuitBreakerFailures: failures,
		CircuitBreakerCooldown: cooldown,
	}
}

// 请求合并：N 个并发相同请求只应触发 1 次真实上游调用
func TestSingleflightCoalescesConcurrentRequests(t *testing.T) {
	setupConf(100, 30)

	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		time.Sleep(200 * time.Millisecond) // 放大并发窗口
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	h := base.NewBaseHandler()
	h.UpstreamCacheTTL = 0 // 关闭缓存，单独验证合并

	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m, err := h.FetchJSON(context.Background(), srv.URL, nil)
			if err != nil {
				t.Errorf("FetchJSON 出错: %v", err)
				return
			}
			if m["ok"] != true {
				t.Errorf("响应内容不符: %v", m)
			}
		}()
	}
	wg.Wait()

	if got := hits.Load(); got != 1 {
		t.Fatalf("期望上游只被调用 1 次，实际 %d 次", got)
	}
}

// 熔断：连续失败达到阈值后，后续请求快速失败（503）且不再触达上游
func TestCircuitBreakerOpensAfterThreshold(t *testing.T) {
	setupConf(3, 60)

	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	h := base.NewBaseHandler()
	h.UpstreamCacheTTL = 0

	for i := 1; i <= 6; i++ {
		_, err := h.FetchJSON(context.Background(), srv.URL, nil)
		if err == nil {
			t.Fatalf("第 %d 次请求期望错误", i)
		}
		ue, ok := base.IsUpstreamError(err)
		if !ok {
			t.Fatalf("第 %d 次请求期望 UpstreamError，实际: %v", i, err)
		}
		want := http.StatusInternalServerError
		if i > 3 {
			want = http.StatusServiceUnavailable // 熔断开启
		}
		if ue.StatusCode != want {
			t.Fatalf("第 %d 次请求期望状态 %d，实际 %d", i, want, ue.StatusCode)
		}
	}

	if got := hits.Load(); got != 3 {
		t.Fatalf("熔断后上游不应再被调用，期望 3 次，实际 %d 次", got)
	}
}

// 熔断冷却期结束后放行探测：上游恢复则熔断关闭
func TestCircuitBreakerHalfOpenRecovery(t *testing.T) {
	setupConf(2, 0.2) // 200ms 冷却

	var fail atomic.Bool
	fail.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	h := base.NewBaseHandler()
	h.UpstreamCacheTTL = 0

	// 触发熔断
	for i := 0; i < 2; i++ {
		_, _ = h.FetchJSON(context.Background(), srv.URL, nil)
	}
	// 冷却期内快速失败
	if _, err := h.FetchJSON(context.Background(), srv.URL, nil); err == nil {
		t.Fatal("冷却期内期望熔断错误")
	}
	// 等冷却结束，上游恢复
	time.Sleep(300 * time.Millisecond)
	fail.Store(false)
	m, err := h.FetchJSON(context.Background(), srv.URL, nil)
	if err != nil || m["ok"] != true {
		t.Fatalf("冷却结束后探测请求应成功: err=%v", err)
	}
}

// 降级：上游故障时返回过期缓存
func TestStaleCacheFallbackOnUpstreamFailure(t *testing.T) {
	setupConf(100, 30) // 阈值调高，不干扰本用例

	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	h := base.NewBaseHandler()
	h.UpstreamCacheTTL = time.Nanosecond // 立即过期，确保第二次请求会重打上游

	m, err := h.FetchJSON(context.Background(), srv.URL, nil)
	if err != nil || m["ok"] != true {
		t.Fatalf("首次请求应成功: err=%v", err)
	}

	time.Sleep(10 * time.Millisecond) // 等缓存过期
	fail.Store(true)

	m, err = h.FetchJSON(context.Background(), srv.URL, nil)
	if err != nil {
		t.Fatalf("上游故障且存在过期缓存时应降级成功，实际错误: %v", err)
	}
	if m["ok"] != true {
		t.Fatalf("降级返回内容不符: %v", m)
	}
}
