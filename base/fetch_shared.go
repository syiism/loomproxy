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
	// Touch 把**已存在**的一格续到 now+ttl；键不存在返回 false，绝不新建。
	// 读命中路径调它——被反复命中的热格不再"写入时刻起算到期"，而是"最后一次使用起算"。
	Touch(key string, ttl time.Duration)
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
			sc.Touch(cacheKey, ttl)
			return val, http.StatusOK, nil
		}
	}

	raw, err := doSingleflight("shared:"+cacheKey, func() (interface{}, error) {
		// double-check：等锁期间先行者可能刚把同一条塞进缓存
		if ttl > 0 && sc != nil {
			if val, ok := sc.Get(cacheKey); ok && len(val) > 0 {
				sc.Touch(cacheKey, ttl)
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
		worthCaching := resp.StatusCode >= 200 && resp.StatusCode < 300 &&
			(cacheIf == nil || cacheIf(b))
		if worthCaching {
			sharedCachePut(sc, cacheKey, b, ttl)
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

// sharedCachePut 是"往跨进程缓存写一格"的唯一一处判定。
//
// 抽出来是因为判定有两条通路要用它（`FetchShared` 的写、`SetShared` 的写），
// 而"同一份规则在两处各写一遍"正是会漂的那种形状（待办清单 S81 那一族）：
// 一处放宽、另一处不变，症状就是同一键在两条路上给出不同答案。
//
// 四条都不写时返回 false 而不是报错——**缓存不是正确性的承担者**，
// 写不进去的最坏结果是多打一发上游；但调用方**要检查这个返回值**，
// 静默的 false 等于"这一格根本没落地"，而"填了没生效没人说"是本仓明令禁止的形状。
func sharedCachePut(sc SharedCache, cacheKey string, val []byte, ttl time.Duration) bool {
	if sc == nil || cacheKey == "" || ttl <= 0 || len(val) == 0 {
		return false
	}
	// 键里可能有调用方从请求参数抄进来的片段：边界在入口，不在猜的那一层。
	if len(cacheKey) > maxSharedCacheKeySize {
		return false
	}
	sc.Set(cacheKey, val, ttl)
	return true
}

// maxSharedCacheKeySize 是跨进程缓存键的长度上限。
// 键由调用方拼，而有些段来自客户端（章节标识、书名一类的）——不设上限就等于
// 让调用方拿 Redis 键空间当免费存储，且这些键一旦写进去要到过期才走。
const maxSharedCacheKeySize = 512

// SetShared 把一格**由调用方算出来的值**写进同一层跨进程缓存。
//
// 为什么要有它——`FetchShared` 的写入点长在传输层，存的只能是**上游原始字节**；
// 而有些值只有源自己算得出来。携带形态的正文缓存 v2（其待办清单 S89）要存的是
// 「上游原样的密文信封 + 那一次派生出的章级键」：那把键是解密那一刻才存在的东西，
// 传输层既不认识、也不该认识。让源去开一层自己的缓存就是长出第二条缓存通路
// （§8 那条「上游请求一律经 BaseHandler」的反面），所以这里补一个端口：
// **层与判定仍然只有一份**，多出来的只是"由调用方写"这个入口。
//
// 语义与 `FetchShared`/`PeekShared` 逐条对齐（`ttl <= 0` 不写、未注入缓存层不写、空值不写）——
// 三条里任何一条在两处写法不同，同一格就会出现"一条路写得进、另一路读不出"的分裂。
func (h *BaseHandler) SetShared(_ context.Context, cacheKey string, val []byte, ttl time.Duration) bool {
	return sharedCachePut(loadSharedCache(), cacheKey, val, ttl)
}

// PeekShared 只读地问一句"这一键在不在"：不合并、不打上游。
// **命中会续期**（滑到 now+ttl）：被反复问到的热格不该在最后一次使用之前先到期——
// 正文缓存要付的到期代价是整本重拉（S97 实测 96 次/天 ≈ 8266 发上游只为重数目录），
// 固定 TTL 会把"还在被读"的热门也照这个价付掉；续期只救热格，冷格仍按原窗口走。
// 验证材料那一族不适用：ALTCHA 的 captcha_token 到期由**站方**说了算，客户端续期只会
// 攒出一枚注定被拒的过期凭证——所以 `zj_novel` 的 token 缓存不接这一层（见其 S100 邻近实现）。
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
	sc.Touch(cacheKey, ttl)
	return val, true
}
