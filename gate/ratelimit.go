package gate

// 速率限制：窗口计数（允许突发）与固定间隔（不可突发）两种口径并存，
// 优先级 套餐级 > 全局，同级内 窗口 > 间隔；同时按 IP 维度与用户维度判定，两者都放行才放行。
//
// 注册 Def：Name="ratelimit"，Scope=Route，Order=600——三轴最后一位，
// 放在 billing(500) 之后才能做到「被限流的请求不扣额度」，放在 monitor(200) 之后
// 才能让 429 计入监控并喂自动拉黑。

import (
	"fmt"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	"loomproxy/conf"
	"loomproxy/db"
	"loomproxy/middleware"
	"loomproxy/models"
	"loomproxy/utils"
)

func init() {
	middleware.Register(middleware.Def{
		Name:    "ratelimit",
		Scope:   middleware.Route,
		Order:   middleware.OrderRateLimit,
		Applies: func(s middleware.Spec) bool { return s.Source != "" },
		Build:   func(s middleware.Spec) gin.HandlerFunc { return RateLimitMiddleware(s.Source, s.Action) },
	})
}

const (
	// defaultWindowSec 窗口计数限流未填窗口长度时的默认窗口（秒）
	defaultWindowSec = 60
	// maxWindowLimit 窗口次数的防御性封顶：滑动窗口按次数分配环形缓冲，
	// 误配超大值会为每个 IP/用户 key 分配过多内存
	maxWindowLimit = 10000
	// limiterIdleTTL 限流器闲置多久后可回收；回收不改变限流语义（闲置超过该时长的
	// 窗口内记录已全部过期，间隔模式的令牌也早已回满）
	limiterIdleTTL = 10 * time.Minute
)

// rateLimitSpec 某接口对某用户生效的限流规则（两种口径互斥，窗口计数优先）
type rateLimitSpec struct {
	limitCount int64         // >0：窗口计数，窗口内最多 limitCount 次（允许突发）
	window     time.Duration // 窗口长度（仅窗口计数模式）
	interval   time.Duration // >0：固定间隔，每 interval 允许 1 次（不可突发）
}

func (s rateLimitSpec) unlimited() bool {
	return s.limitCount <= 0 && s.interval <= 0
}

// specFrom 由库中配置构造限流规则：limit_count>0 走窗口计数，否则回退 interval 间隔模型
func specFrom(limitCount, windowSec, interval int64) rateLimitSpec {
	if limitCount > 0 {
		if limitCount > maxWindowLimit {
			log.Printf("WARN: 窗口限流次数 %d 超过上限 %d，按上限处理", limitCount, maxWindowLimit)
			limitCount = maxWindowLimit
		}
		if windowSec <= 0 {
			windowSec = defaultWindowSec
		}
		return rateLimitSpec{limitCount: limitCount, window: time.Duration(windowSec) * time.Second}
	}
	if interval > 0 {
		return rateLimitSpec{interval: time.Duration(interval) * time.Second}
	}
	return rateLimitSpec{}
}

// resolveRateLimit 解析接口的生效限流：套餐级配置优先（窗口 > 间隔），
// 套餐级未配置限流时回退全局配置（同样窗口 > 间隔）
func resolveRateLimit(planID uint, sourceName, action string) rateLimitSpec {
	if planID > 0 {
		// group_code 列存数据源码，与管理面板写入口径一致
		var planCost models.QuotaCostPlan
		if err := db.DB.Where("plan_id = ? AND group_code = ? AND interface = ?", planID, sourceName, action).First(&planCost).Error; err == nil {
			if spec := specFrom(planCost.LimitCount, planCost.WindowSec, planCost.Interval); !spec.unlimited() {
				return spec
			}
		}
	}
	var cost models.QuotaCost
	if err := db.DB.Where("group_code = ? AND interface = ?", sourceName, action).First(&cost).Error; err == nil {
		return specFrom(cost.LimitCount, cost.WindowSec, cost.Interval)
	}
	return rateLimitSpec{}
}

// rateLimiter 固定间隔限流器（间隔模型：每 N 秒允许 1 次请求）
type rateLimiter struct {
	mu       sync.Mutex
	lastTime time.Time
	tokens   float64
	interval int64        // 请求间隔(秒)，0=不限
	lastQPS  int64        // 兼容旧数据，保留最后使用的QPS值
	lastSeen atomic.Int64 // 最近使用时间（UnixNano），仅供闲置回收读取
}

// windowLimiter 滑动窗口计数限流器：任意 window 长度内最多 limit 次请求。
// 与间隔模型的区别：允许突发——窗口内前 limit 次立即放行，用满后需等最早一次滑出窗口。
type windowLimiter struct {
	mu       sync.Mutex
	limit    int64
	window   time.Duration
	stamps   []time.Time // 环形缓冲，容量 = limit；n < limit 时 head 指向空闲槽，n == limit 时 head 指向最旧一条
	head     int
	n        int
	lastSeen atomic.Int64
}

var (
	rateLimiters       = make(map[string]*rateLimiter)
	rateLimiterMu      sync.RWMutex
	windowLimiters     = make(map[string]*windowLimiter)
	windowLimiterMu    sync.RWMutex
	limiterJanitorOnce sync.Once
)

// startLimiterJanitor 启动闲置限流器回收协程（懒启动，每进程只启一次）。
// 限流 key 含 IP/用户维度，不回收会随访问过的 IP/用户数无限增长。
// 回收条件由各限流器自己判定（idleExpired）：闲置超过其配置周期即可无损丢弃，
// 低速率配置（如 interval=3600）不会被 10 分钟的固定阈值提前清掉而放行。
func startLimiterJanitor() {
	limiterJanitorOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(5 * time.Minute)
			defer ticker.Stop()
			for range ticker.C {
				now := time.Now()
				// 两段各包闭包：这是循环体，就地 defer 要等协程退出才解锁，第二次 tick 就抢不到锁了
				// （尾解锁才是问题——panic 会把锁永久留在手里，待办清单 P93）
				func() {
					rateLimiterMu.Lock()
					defer rateLimiterMu.Unlock()
					for k, l := range rateLimiters {
						if l.idleExpired(now) {
							delete(rateLimiters, k)
						}
					}
				}()

				func() {
					windowLimiterMu.Lock()
					defer windowLimiterMu.Unlock()
					for k, l := range windowLimiters {
						if l.idleExpired(now) {
							delete(windowLimiters, k)
						}
					}
				}()
			}
		}()
	})
}

// idleExpired 是否可回收：闲置时长超过 max(limiterIdleTTL, 2×interval)。
// 闲置超过一个 interval 后令牌已回满，丢弃与保留等价（2× 是安全余量）。
func (rl *rateLimiter) idleExpired(now time.Time) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	ttl := limiterIdleTTL
	if d := time.Duration(rl.interval) * time.Second * 2; d > ttl {
		ttl = d
	}
	return now.Sub(time.Unix(0, rl.lastSeen.Load())) > ttl
}

// idleExpired 是否可回收：闲置时长超过 max(limiterIdleTTL, 2×window)。
// 闲置超过一个窗口后所有记录都已滑出，丢弃与保留等价。
func (wl *windowLimiter) idleExpired(now time.Time) bool {
	wl.mu.Lock()
	defer wl.mu.Unlock()
	ttl := limiterIdleTTL
	if d := wl.window * 2; d > ttl {
		ttl = d
	}
	return now.Sub(time.Unix(0, wl.lastSeen.Load())) > ttl
}

// LimiterIdleExpiredForTest 供测试验证闲置回收判定（回归低速率限流被回收协程稀释）：
// 返回（间隔限流器是否可回收, 窗口限流器是否可回收）。
func LimiterIdleExpiredForTest(intervalSec, windowSec int64, idle time.Duration) (bool, bool) {
	now := time.Now()
	rl := &rateLimiter{lastTime: now, tokens: 1, interval: intervalSec}
	rl.lastSeen.Store(now.Add(-idle).UnixNano())
	wl := &windowLimiter{limit: 1, window: time.Duration(windowSec) * time.Second}
	wl.lastSeen.Store(now.Add(-idle).UnixNano())
	return rl.idleExpired(now), wl.idleExpired(now)
}

// getRateLimiter 获取（或创建）某接口的间隔限流器
func getRateLimiter(key string) *rateLimiter {
	startLimiterJanitor()
	// 读那一段包闭包，让 defer 只盖住这一次查表（待办清单 P93：尾解锁会被 panic 跳过）
	limiter, ok := func() (*rateLimiter, bool) {
		rateLimiterMu.RLock()
		defer rateLimiterMu.RUnlock()
		l, ok := rateLimiters[key]
		return l, ok
	}()
	if ok {
		return limiter
	}

	rateLimiterMu.Lock()
	defer rateLimiterMu.Unlock()
	// double-check 防止并发重复创建
	if limiter, ok = rateLimiters[key]; ok {
		return limiter
	}

	limiter = &rateLimiter{
		lastTime: time.Now(),
		tokens:   1,
	}
	limiter.lastSeen.Store(time.Now().UnixNano())
	rateLimiters[key] = limiter
	return limiter
}

// getWindowLimiter 获取（或创建）某接口的窗口计数限流器
func getWindowLimiter(key string) *windowLimiter {
	startLimiterJanitor()
	// 同 getRateLimiter：读表那一段包闭包（待办清单 P93）
	limiter, ok := func() (*windowLimiter, bool) {
		windowLimiterMu.RLock()
		defer windowLimiterMu.RUnlock()
		l, ok := windowLimiters[key]
		return l, ok
	}()
	if ok {
		return limiter
	}

	windowLimiterMu.Lock()
	defer windowLimiterMu.Unlock()
	// double-check 防止并发重复创建
	if limiter, ok = windowLimiters[key]; ok {
		return limiter
	}
	limiter = &windowLimiter{}
	limiter.lastSeen.Store(time.Now().UnixNano())
	windowLimiters[key] = limiter
	return limiter
}

// allow 允许请求通过（使用间隔模型）；interval 为请求间隔秒数（<=0 表示不限）
func (rl *rateLimiter) allow(interval int64) bool {
	rl.lastSeen.Store(time.Now().UnixNano())

	rl.mu.Lock()
	defer rl.mu.Unlock()

	rl.interval = interval

	var intervalSec float64

	// 优先用 interval（秒）
	if rl.interval > 0 {
		intervalSec = float64(rl.interval)
	} else if rl.lastQPS > 0 {
		// 兼容旧数据：QPS 模型（每秒N个token）
		intervalSec = 1.0 / float64(rl.lastQPS)
	} else {
		// 无限制
		return true
	}

	now := time.Now()
	elapsed := now.Sub(rl.lastTime).Seconds()
	rl.tokens += elapsed / intervalSec // 每 interval 秒增加 1 个 token
	if rl.tokens > 1 {
		rl.tokens = 1
	}
	rl.lastTime = now

	if rl.tokens >= 1 {
		rl.tokens--
		return true
	}
	return false
}

// allow 滑动窗口计数：窗口内最多 limit 次；被拒绝的请求不计数（不占用额度）
func (wl *windowLimiter) allow(limit int64, window time.Duration) bool {
	if limit <= 0 {
		return true
	}
	if window <= 0 {
		window = defaultWindowSec * time.Second
	}
	wl.lastSeen.Store(time.Now().UnixNano())

	wl.mu.Lock()
	defer wl.mu.Unlock()

	if wl.limit != limit {
		// 管理面板改了次数上限：重建环形缓冲（原窗口计数作废，按新配置重新计数）
		wl.limit = limit
		wl.stamps = make([]time.Time, limit)
		wl.head, wl.n = 0, 0
	}
	wl.window = window

	now := time.Now()
	if wl.n == int(limit) && now.Sub(wl.stamps[wl.head]) < window {
		return false
	}
	wl.stamps[wl.head] = now
	wl.head = (wl.head + 1) % int(limit)
	if wl.n < int(limit) {
		wl.n++
	}
	return true
}

// allowRequest 按生效规则判定单个维度（IP / 用户）是否放行；key 为空表示该维度不参与
func allowRequest(key string, spec rateLimitSpec) bool {
	if key == "" {
		return true
	}
	if spec.limitCount > 0 {
		return getWindowLimiter(key).allow(spec.limitCount, spec.window)
	}
	return getRateLimiter(key).allow(int64(spec.interval / time.Second))
}

// tooFrequent 统一的限流拒绝响应（429）
func tooFrequent(c *gin.Context) {
	c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
		"code": conf.Config.ErrorCode,
		"msg":  "接口请求过于频繁，请稍后重试",
	})
}

// RateLimitMiddleware 基于 QuotaCost/QuotaCostPlan 的速率限制中间件（导出供装配层使用）
// 支持按 IP 和用户双维度限制：两者都放行才放行
func RateLimitMiddleware(sourceName, action string) gin.HandlerFunc {
	return rateLimitMiddleware(sourceName, action)
}

// rateLimitMiddleware 速率限制中间件
// 支持按 IP 和用户双维度限制：两者都放行才放行，任一被拒返回 429
// 套餐级配置 (QuotaCostPlan) 优先于全局 (QuotaCost)，两级内部均为「窗口计数 > 固定间隔」
func rateLimitMiddleware(sourceName, action string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 跳过非数据源路由
		if sourceName == "" {
			c.Next()
			return
		}

		if uid, exists := c.Get("user_id"); exists {
			if id, ok := uid.(uint); ok && id > 0 {
				var user models.User
				if err := db.DB.Preload("Roles").Preload("Plan").First(&user, id).Error; err == nil {
					if user.HasRole("admin") {
						c.Next()
						return
					}
				}
			}
		}

		// 确定用户的生效套餐
		var planID uint
		if uid, exists := c.Get("user_id"); exists {
			if id, ok := uid.(uint); ok && id > 0 {
				var user models.User
				if err := db.DB.Preload("Plan").First(&user, id).Error; err == nil && user.Plan != nil {
					plan := ResolvePlan(&user)
					if plan.ID > 0 {
						planID = plan.ID
					}
				}
			}
		} else {
			// 未通过 apiauth 的路由：自行解析 JWT
			tokenStr := utils.TokenFromRequest(c)
			if tokenStr != "" {
				if claims, err := utils.ParseToken(tokenStr); err == nil {
					var user models.User
					if err := db.DB.Preload("Plan").First(&user, claims.UserID).Error; err == nil {
						plan := ResolvePlan(&user)
						if plan.ID > 0 {
							planID = plan.ID
						}
					}
				}
			}
		}

		spec := resolveRateLimit(planID, sourceName, action)
		if spec.unlimited() {
			c.Next()
			return
		}

		// 取限流 key：IP 维度使用全局 key；用户维度 key 包含 planID 以隔离不同套餐
		ipKey := fmt.Sprintf("rate:%s:%s:ip:%s", sourceName, action, c.ClientIP())
		userID := uint(0)
		if uid, exists := c.Get("user_id"); exists {
			if id, ok := uid.(uint); ok {
				userID = id
			}
		} else {
			tokenStr := utils.TokenFromRequest(c)
			if tokenStr != "" {
				if claims, err := utils.ParseToken(tokenStr); err == nil {
					userID = claims.UserID
				}
			}
		}
		userKey := ""
		if userID > 0 {
			userKey = fmt.Sprintf("rate:%s:%s:user:%d:p%d", sourceName, action, userID, planID)
		}

		// IP 维度 + 用户维度都要检查（allow 内部自行加锁并写入配置，外层不得再持锁）
		if !allowRequest(ipKey, spec) {
			tooFrequent(c)
			return
		}
		if !allowRequest(userKey, spec) {
			tooFrequent(c)
			return
		}

		c.Next()
	}
}
