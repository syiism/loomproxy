package base

// 正文类响应的跨进程缓存端口。
//
// 为什么是端口而不是直接调 `utils.DefaultCache()`：**AGENTS §4 那条硬边界——base 不得导入 utils**。
// 所以这里只声明"要一件能按 key 取/存字节的东西"，具体接哪一层（Redis 或纯内存）由装配期决定：
// `app` 同时看得到 base 与 utils，适配器落在那儿。
//
// 未注入时一切照旧可用：`FetchShared` 退化成"只做并发合并、不缓存"，不报错也不 panic——
// 测试与不接 Redis 的部署都是这个形状。

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"
)

// SharedCache 是按 key 存字节的跨进程缓存；ttl 是**这一条**的存活时间。
type SharedCache interface {
	Get(key string) ([]byte, bool)
	Set(key string, val []byte, ttl time.Duration)
}

// ResponseFilter 是调用方对「这一份响应值不值得进缓存」的判定。
//
// 为什么要有它而不让骨架自己判：**骨架不认识任何一个源的成功形状**。
// 信封站（业务码在 body 里、HTTP 恒 200）与状态码站（4xx/5xx 就是失败）是两类判法，
// 把任何一类写死进骨架，另一类就会把失败当成功存起来。
type ResponseFilter func(body []byte) bool

// 存法用互斥而不是 atomic.Value：**atomic.Value 要求每次存的具体类型一致**，
// 而这里天生要往同一个槽里放「适配器」与「清空用的哨兵」两种类型——
// 第一版那么写，用例一 cleanup 就 panic（`store of inconsistently typed value`）。
// 注入只发生在装配期与测试里，读侧的这点开销不构成热点。
var (
	sharedCacheMu sync.RWMutex
	sharedCache   SharedCache
)

// SetSharedCache 在装配期注入（一次；重复注入直接覆盖，便于测试换替身）。传 nil = 不缓存。
func SetSharedCache(c SharedCache) {
	sharedCacheMu.Lock()
	defer sharedCacheMu.Unlock()
	sharedCache = c
}

func loadSharedCache() SharedCache {
	sharedCacheMu.RLock()
	defer sharedCacheMu.RUnlock()
	return sharedCache
}

// FetchShared 与 `Fetch` 走同一条传输通路（熔断、代理池、UA 轮换全都照旧），另外接上两件事：
//
//	① 并发合并：同一 cacheKey 的并发请求只打一发上游（进程内 singleflight）；
//
// ② 可选的跨进程缓存：装配期注入的那一层（现网是 Redis）。
//
// 它是给 **POST 型源**用的。现成那两条带缓存的通路 `FetchJSON`/`FetchText` 是 GET，
// 缓存键能从 URL + headers 推出来；而签名/加密站的 body 与 nonce/时间戳/签名每次都不同，
// **推不出可复用的键**——所以这里要求调用方显式给 cacheKey。
//
// 三条使用约束（都是这套机制成立的前提，不是风格）：
//   - **键必须含全部会改变响应的维度**。以 zj_novel 的正文为例：响应是加密信封，
//     解它要用当时那枚会话的键，所以键里要带 sessionID；换了会话还复用旧条目就是解不开，
//     而不是"读到旧正文"。
//   - `ttl <= 0` = 只合并、不缓存（想省上游但不想留内容面就用这一档）。
//   - 进缓存的门槛：**2xx 且非空，且 `cacheIf` 认可**（传 nil 就只看前两条）。
//     为什么状态码不够——**信封站的业务失败照样回 HTTP 200**，`{code:4001}` 那种坏信封
//     一旦进缓存，就等于替上游把"这一章读不了"钉满整个 TTL。这条不是推演：
//     2026-10-08 携带形态实测，一枚业务失败被同章的后续 15 个请求命中、每个 2ms 返回，
//     而它们本该各自重试上游。成功与否只有调用方读得懂（它认识那个信封），所以判定交给调用方。
//     （同族：S54「超限要报错不要截断」——坏结果进缓存比没有缓存更难看。）
func (h *BaseHandler) FetchShared(ctx context.Context, url string, method string,
	headers map[string]string, body io.Reader, cacheKey string, ttl time.Duration,
	cacheIf ResponseFilter) ([]byte, int, error) {

	if cacheKey == "" {
		return nil, 0, NewUpstreamError(http.StatusInternalServerError, "FetchShared 需要显式 cacheKey", nil)
	}
	sc := loadSharedCache()
	if ttl > 0 && sc != nil {
		if val, ok := sc.Get(cacheKey); ok && len(val) > 0 {
			return val, http.StatusOK, nil
		}
	}

	raw, err := doSingleflight("shared:"+cacheKey, func() (interface{}, error) {
		// double-check：等锁期间先行者可能刚把同一条塞进缓存
		if ttl > 0 && sc != nil {
			if val, ok := sc.Get(cacheKey); ok && len(val) > 0 {
				return cachedResult{body: val, status: http.StatusOK}, nil
			}
		}
		resp, ferr := h.Fetch(ctx, url, method, headers, body)
		if ferr != nil {
			return nil, ferr
		}
		defer resp.Body.Close()
		buf := acquireBodyBuf()
		defer releaseBodyBuf(buf)
		if _, rerr := io.Copy(buf, io.LimitReader(resp.Body, maxResponseBodySize)); rerr != nil {
			return nil, rerr
		}
		b := append([]byte(nil), buf.Bytes()...)
		worthCaching := resp.StatusCode >= 200 && resp.StatusCode < 300 && len(b) > 0 &&
			(cacheIf == nil || cacheIf(b))
		if worthCaching && ttl > 0 && sc != nil {
			sc.Set(cacheKey, b, ttl)
		}
		return cachedResult{body: b, status: resp.StatusCode}, nil
	})
	if err != nil {
		return nil, 0, err
	}
	switch v := raw.(type) {
	case cachedResult:
		return v.body, v.status, nil
	case []byte: // 缓存命中分支直接回了字节（保留这一型以防将来两条路径合流）
		return v, http.StatusOK, nil
	}
	return nil, 0, NewUpstreamError(http.StatusInternalServerError, "FetchShared 返回了意外的类型", nil)
}

// cachedResult 带类型地占住 singleflight 的共享值：singleflight 按键合并，
// 同键不同形状的返回值挤在同一个 interface 里，取用时靠类型断言分辨。
type cachedResult struct {
	body   []byte
	status int
}

// PeekShared 只读地问一句"这一键在不在"：不合并、不打上游、不刷新 TTL。
//
// 为什么要有它——**读缓存的时机必须在"取验证材料"之前**。带验证的源（纸间是 ALTCHA：取挑战 →
// 本地解 PoW → verify → 才轮到那一发被缓存的正文）如果先解验证再问缓存，
// 命中就只省掉最后一发，前面那两发照打；缓存越有效，这个顺序问题越隐蔽，
// 因为命中率读数会看起来"还行"。携带形态 2026-10-08 实测到的就是这一格（见其待办清单 S87）。
//
// 与 `FetchShared` 一致的两条语义：`ttl <= 0` 一律算未命中（那一档本来就不存东西）；
// 未注入缓存层时永远未命中。
func (h *BaseHandler) PeekShared(_ context.Context, cacheKey string, ttl time.Duration) ([]byte, bool) {
	if cacheKey == "" || ttl <= 0 {
		return nil, false
	}
	sc := loadSharedCache()
	if sc == nil {
		return nil, false
	}
	val, ok := sc.Get(cacheKey)
	if !ok || len(val) == 0 {
		return nil, false
	}
	return val, true
}
