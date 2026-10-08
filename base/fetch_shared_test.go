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
				strings.NewReader(`{"chapter_id":"chap1"}`), key, 30*time.Second, nil)
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
	body, status, err := h.FetchShared(context.Background(), srv.URL, "POST", nil, nil, key, 30*time.Second, nil)
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

		if _, _, err := h.FetchShared(context.Background(), srv.URL, "POST", nil, strings.NewReader("{}"), key, time.Minute, nil); err != nil {
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
			if _, _, err := h.FetchShared(context.Background(), srv.URL, "POST", nil, strings.NewReader("{}"), key, 0, nil); err != nil {
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
	body, _, err := h.FetchShared(context.Background(), srv.URL, "POST", nil, strings.NewReader("{}"), "content:demo:x", time.Minute, nil)
	if err != nil || string(body) != "正文" {
		t.Fatalf("未注入缓存时不该失败: %q %v", body, err)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("上游调用数 = %d", n)
	}
	// 空键必须出声，而不是静默退化成一个共享的空键（那会把不同章节的响应混在一起）
	if _, _, err := h.FetchShared(context.Background(), srv.URL, "POST", nil, nil, "", time.Minute, nil); err == nil {
		t.Error("cacheKey 为空必须报错：空键等于把不同响应共用一格")
	}
}

// TestFetchSharedNeedsCallerVerdict 钉住这条判据的来路：**状态码不够**。
//
// 现网实测的形状（携带形态 2026-10-08）：上游把业务失败装进 **HTTP 200** 的信封里回
// （`{"code":4001,…}`）。骨架只按状态码判"值得缓存"，那一枚坏信封就进了缓存，
// 同章后续 15 个请求每个都在 2ms 内命中它、各自再报一次"读不了"——
// 一次失败被放大成整个 TTL 内的稳定失败。骨架不认识任何一个源的成功形状，
// 所以判定必须是调用方给的；这里两侧都要验：拒绝它（不缓存、第二发真打上游）与认可它（缓存）。
func TestFetchSharedNeedsCallerVerdict(t *testing.T) {
	envBad := `{"code":4001,"message":"chapter unavailable"}`
	ok := func(body []byte) bool { return strings.Contains(string(body), `"code":0`) }

	// ① 拒绝业务失败的 200：不进缓存，第二次必须真打上游
	srv, hits := sharedUpstream(t, http.StatusOK, envBad, 0)
	cache := newMemCache()
	SetSharedCache(cache)
	h := &BaseHandler{Path: "/demo/content"}
	key := "content:demo:verdict"
	if _, st, err := h.FetchShared(context.Background(), srv.URL, "POST", nil,
		strings.NewReader("{}"), key, time.Minute, ok); err != nil || st != http.StatusOK {
		t.Fatalf("第一发不符: %d %v", st, err)
	}
	if _, found := cache.peek(t, key); found {
		t.Error("业务失败的 200 被缓存了——一次失败会被钉成整个 TTL 的稳定失败")
	}
	if _, _, err := h.FetchShared(context.Background(), srv.URL, "POST", nil,
		strings.NewReader("{}"), key, time.Minute, ok); err != nil {
		t.Fatalf("第二发不符: %v", err)
	}
	if n := hits.Load(); n != 2 {
		t.Errorf("坏信封不该进缓存，第二发要真打上游，实际共 %d 次", n)
	}

	// ② 认可成功的 200：进缓存，第二次不再打上游
	srv2, hits2 := sharedUpstream(t, http.StatusOK, `{"code":0,"data":{}}`, 0)
	cache2 := newMemCache()
	SetSharedCache(cache2)
	for i := 0; i < 2; i++ {
		if _, _, err := h.FetchShared(context.Background(), srv2.URL, "POST", nil,
			strings.NewReader("{}"), key, time.Minute, ok); err != nil {
			t.Fatalf("成功路径第 %d 发不符: %v", i+1, err)
		}
	}
	if n := hits2.Load(); n != 1 {
		t.Errorf("业务成功的响应该命中缓存，上游却被打了几次：%d", n)
	}

	// ③ 传 nil = 退回"只看状态码"那一档：仍按 2xx 缓存（老调用方语义不变）
	srv3, hits3 := sharedUpstream(t, http.StatusOK, "正文", 0)
	cache3 := newMemCache()
	SetSharedCache(cache3)
	for i := 0; i < 2; i++ {
		if _, _, err := h.FetchShared(context.Background(), srv3.URL, "POST", nil,
			strings.NewReader("{}"), "content:demo:nil", time.Minute, nil); err != nil {
			t.Fatalf("nil 判定第 %d 发不符: %v", i+1, err)
		}
	}
	if n := hits3.Load(); n != 1 {
		t.Errorf("cacheIf=nil 时该照旧按 2xx 缓存，上游被打了几次：%d", n)
	}
}

// TestReplayableRequestRestartsBody 钉住重试那一发**不能发空体**。
//
// 判据的形状：`client.Do` 一次就消费掉调用方给的 reader，而带代理池的那条路上有第二次 Do
// （换代理/回退直连）。纸间这类源每次 POST 都带签名信封，第二次发空体被上游回"缺参数/签名不符"，
// 读起来却像我们的签名错了——症状与真因隔了一层，所以这一格必须有用例，而不是靠注释。
func TestReplayableRequestRestartsBody(t *testing.T) {
	payload := `{"version":1,"data":"密文","nonce":"iv"}`
	req, err := newReplayableRequest(context.Background(), "POST", "http://example.invalid/api",
		strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	first, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != payload {
		t.Fatalf("第一发就读错了体: %q", first)
	}
	// 这条断言管的是那个**隐式**依赖：`http.NewRequest` 给 *bytes.Reader 挂的 GetBody。
	// 它不在我们的代码里，所以坏掉时只能靠这里出声。
	if req.GetBody == nil {
		t.Fatal("请求没有可重放的 GetBody——代理重试那条路会发空体")
	}
	// 模拟代理重试：体已经被读空，重发的这一发必须拿回同一份字节
	if err := restartBody(req); err != nil {
		t.Fatalf("倒回请求体失败: %v", err)
	}
	second, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(second) != payload {
		t.Errorf("重试发出的是空体/错体（第一发已消费，倒回去要原样）: %q", second)
	}

	// 无体的 GET 保持 Body=nil——塞一个零长 reader 会让某些上游按 POST 判
	getReq, err := newReplayableRequest(context.Background(), "GET", "http://example.invalid/api", nil)
	if err != nil {
		t.Fatal(err)
	}
	if getReq.Body != nil {
		t.Error("GET 不该被装上请求体")
	}
	if getReq.GetBody != nil {
		t.Error("GET 没有体，不需要倒回机制")
	}
}

// TestReplayableRequestCapsBody 超过缓冲上限要报错，而不是发一个被截断的信封。
func TestReplayableRequestCapsBody(t *testing.T) {
	big := strings.Repeat("a", int(maxRequestBodySize)+1)
	if _, err := newReplayableRequest(context.Background(), "POST", "http://example.invalid/api",
		strings.NewReader(big)); err == nil {
		t.Error("超上限的请求体被静默接受了")
	}
}
