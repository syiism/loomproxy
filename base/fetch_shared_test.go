package base

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type memCache struct {
	mu   sync.Mutex
	m    map[string]string
	ttl  map[string]time.Duration
	gets atomic.Int64
	sets atomic.Int64
}

func newMemCache() *memCache {
	return &memCache{m: map[string]string{}, ttl: map[string]time.Duration{}}
}

func (c *memCache) Get(key string) ([]byte, bool) {
	c.gets.Add(1)
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[key]
	return []byte(v), ok
}

func (c *memCache) Set(key string, val []byte, ttl time.Duration) {
	c.sets.Add(1)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[key] = string(val)
	c.ttl[key] = ttl
}

func (c *memCache) peek(t *testing.T, key string) (string, bool) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[key]
	return v, ok
}

// sharedUpstream 数着被真打了几次——这套机制的全部价值都在这个计数上。
func sharedUpstream(t *testing.T, status int, body string, delay time.Duration) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		if delay > 0 {
			time.Sleep(delay)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestFetchSharedCachesAndCoalesces(t *testing.T) {
	srv, hits := sharedUpstream(t, http.StatusOK, "正文内容", 60*time.Millisecond)
	cache := newMemCache()
	SetSharedCache(cache)
	t.Cleanup(func() { SetSharedCache(nil) })

	h := &BaseHandler{Path: "/demo/content"}
	key := "content:demo:sess1:chap1"

	// ① 并发同键只打一发上游（合并的价值：热书同一章被同时点开）
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body, status, err := h.FetchShared(context.Background(), srv.URL, "POST",
				map[string]string{"content-type": "application/json"},
				strings.NewReader(`{"chapter_id":"chap1"}`), key, 30*time.Second)
			if err != nil {
				t.Errorf("并发调用出错: %v", err)
				return
			}
			if status != http.StatusOK || string(body) != "正文内容" {
				t.Errorf("结果不符: status=%d body=%q", status, body)
			}
		}()
	}
	wg.Wait()
	if n := hits.Load(); n != 1 {
		t.Errorf("并发 8 次应只打上游 1 次，实际 %d 次（合并没生效）", n)
	}
	if got, ok := cache.peek(t, key); !ok || got != "正文内容" {
		t.Errorf("成功响应没写进缓存: ok=%v got=%q", ok, got)
	}
	if d := cache.ttl[key]; d != 30*time.Second {
		t.Errorf("写入没用调用侧的 TTL：实际 %v（要的是 30s，不是全局 CACHE_TTL）", d)
	}

	// ② 第二次直接命中缓存，不再打上游
	body, status, err := h.FetchShared(context.Background(), srv.URL, "POST", nil, nil, key, 30*time.Second)
	if err != nil || status != http.StatusOK || string(body) != "正文内容" {
		t.Fatalf("缓存命中路径不符: %d %q %v", status, body, err)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("命中缓存后不该再打上游，实际共 %d 次", n)
	}
}

func TestFetchSharedDoesNotCacheFailures(t *testing.T) {
	for _, st := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusInternalServerError} {
		srv, hits := sharedUpstream(t, st, `{"code":401,"message":"invalid session"}`, 0)
		cache := newMemCache()
		SetSharedCache(cache)
		h := &BaseHandler{Path: "/demo/content"}
		key := "content:demo:fail:" + http.StatusText(st)

		if _, _, err := h.FetchShared(context.Background(), srv.URL, "POST", nil, strings.NewReader("{}"), key, time.Minute); err != nil {
			continue // 非 2xx 由 Fetch 之外判：这里只关心"有没有被缓存"
		}
		if _, ok := cache.peek(t, key); ok {
			t.Errorf("%d 的响应被写进了缓存——一次风控拒绝会被缓存成\"这本书读不了\"", st)
		}
		if n := hits.Load(); n != 1 {
			t.Errorf("%d 时上游调用数 = %d", st, n)
		}
		SetSharedCache(nil)
	}
}

func TestFetchSharedTTLZeroOnlyCoalesces(t *testing.T) {
	srv, hits := sharedUpstream(t, http.StatusOK, "正文", 40*time.Millisecond)
	cache := newMemCache()
	SetSharedCache(cache)
	t.Cleanup(func() { SetSharedCache(nil) })
	h := &BaseHandler{Path: "/demo/content"}
	key := "content:demo:nocache"

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := h.FetchShared(context.Background(), srv.URL, "POST", nil, strings.NewReader("{}"), key, 0); err != nil {
				t.Errorf("ttl=0 出错: %v", err)
			}
		}()
	}
	wg.Wait()
	if n := hits.Load(); n != 1 {
		t.Errorf("ttl=0 仍应合并并发，实际打了 %d 次", n)
	}
	if _, ok := cache.peek(t, key); ok {
		t.Error("ttl=0 不该写缓存（只想省上游、不想留内容面就用这一档）")
	}
}

func TestFetchSharedWithoutCacheAndEmptyKey(t *testing.T) {
	srv, hits := sharedUpstream(t, http.StatusOK, "正文", 0)
	SetSharedCache(nil)
	h := &BaseHandler{Path: "/demo/content"}

	// 没注入缓存（测试与不接 Redis 的部署）：照常工作，只是不缓存
	body, _, err := h.FetchShared(context.Background(), srv.URL, "POST", nil, strings.NewReader("{}"), "content:demo:x", time.Minute)
	if err != nil || string(body) != "正文" {
		t.Fatalf("未注入缓存时不该失败: %q %v", body, err)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("上游调用数 = %d", n)
	}
	// 空键必须出声，而不是静默退化成一个共享的空键（那会把不同章节的响应混在一起）
	if _, _, err := h.FetchShared(context.Background(), srv.URL, "POST", nil, nil, "", time.Minute); err == nil {
		t.Error("cacheKey 为空必须报错：空键等于把不同响应共用一格")
	}
}
