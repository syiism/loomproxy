// Package gate 数据源请求链路上的管控环节：访问控制、额度计费、速率限制，
// 以及限额解析（用户覆盖 > 套餐限额 > 不限）。
//
// 与 handlers/ 的区别是方向性的：handlers 是被调用的端点，gate 是每条数据源请求
// 都要穿过的闸门。数据源包因此不需要（也不应）自己实现计费或限流。
package gate

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	"loomproxy-go/conf"
	"loomproxy-go/db"
	"loomproxy-go/models"
	"loomproxy-go/utils"
)

// billingEnabled 计费开关：AUTH_ENABLED=false 的开放部署下不做用户级计费
func billingEnabled() bool {
	return conf.Config.AuthEnabled
}

// StartOfDay 北京时区当日零点（与 dashboard 的重置时间口径一致）
func StartOfDay() time.Time {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Now().In(loc)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
}

// UsedToday 当日某用户在某数据源上已消耗的额度
func UsedToday(userID uint, sourceCode string) int64 {
	var used int64
	if err := db.DB.Model(&models.QuotaUsageLog{}).
		Where("user_id = ? AND group_code = ? AND created_at >= ?", userID, sourceCode, StartOfDay()).
		Select("COALESCE(SUM(cost), 0)").Scan(&used).Error; err != nil {
		log.Printf("ERROR: usedToday query failed: %v", err)
	}
	return used
}

// UsedTodayAll 当日全站某数据源已消耗的额度（管理员视角）
func UsedTodayAll(sourceCode string) int64 {
	var used int64
	if err := db.DB.Model(&models.QuotaUsageLog{}).
		Where("group_code = ? AND created_at >= ?", sourceCode, StartOfDay()).
		Select("COALESCE(SUM(cost), 0)").Scan(&used).Error; err != nil {
		log.Printf("ERROR: UsedTodayAll query failed: %v", err)
	}
	return used
}

// ActiveUsersToday 当日有消耗的去重用户数；sourceCode 为空时统计全部数据源
func ActiveUsersToday(sourceCode string) int64 {
	var n int64
	q := db.DB.Model(&models.QuotaUsageLog{}).Where("created_at >= ?", StartOfDay())
	if sourceCode != "" {
		q = q.Where("group_code = ?", sourceCode)
	}
	if err := q.Distinct("user_id").Count(&n).Error; err != nil {
		log.Printf("ERROR: ActiveUsersToday query failed: %v", err)
	}
	return n
}

// callsToday 当日调用次数（流水行数）；sourceCode 为空时统计全部数据源
func callsToday(sourceCode string) int64 {
	var n int64
	q := db.DB.Model(&models.QuotaUsageLog{}).Where("created_at >= ?", StartOfDay())
	if sourceCode != "" {
		q = q.Where("group_code = ?", sourceCode)
	}
	if err := q.Count(&n).Error; err != nil {
		log.Printf("ERROR: callsToday query failed: %v", err)
	}
	return n
}

// ResolvePlan 返回用户生效的套餐：优先用户绑定套餐，未绑定则使用免费版（code=free）
func ResolvePlan(user *models.User) models.QuotaPlan {
	if user.PlanID != nil && user.Plan != nil {
		// 套餐到期惰性回退 free（不回写数据库；PlanExpireAt 为空=永久，兼容存量）
		if user.PlanExpireAt == nil || time.Now().Before(*user.PlanExpireAt) {
			return *user.Plan
		}
	}
	var plan models.QuotaPlan
	if err := db.DB.Where("code = ?", "free").First(&plan).Error; err == nil {
		return plan
	}
	return models.QuotaPlan{}
}

// ResolvePlanForUser 导出供外部使用
func ResolvePlanForUser(user *models.User) models.QuotaPlan {
	return ResolvePlan(user)
}

// PlanSourceLimits 套餐 source 维度的额度覆盖表（仅 limit >= 0 生效）
func PlanSourceLimits(planID uint) map[string]int64 {
	m := make(map[string]int64)
	if planID == 0 {
		return m
	}
	var limits []models.QuotaLimit
	if err := db.DB.Where("plan_id = ? AND scope = ?", planID, "source").Find(&limits).Error; err != nil {
		log.Printf("ERROR: PlanSourceLimits query failed: %v", err)
		return m
	}
	for _, l := range limits {
		m[l.Target] = l.Limit
	}
	return m
}

// userOverrideLimit 查询用户级覆盖额度（code 为数据源码）。
// ok=false 表示无覆盖或覆盖值为 0（回退套餐）；limit<0 表示不限。
func userOverrideLimit(userID uint, code string) (int64, bool) {
	var override models.UserQuotaOverride
	if err := db.DB.Where("user_id = ? AND group_code = ?", userID, code).First(&override).Error; err != nil {
		return 0, false
	}
	if override.Limit == 0 {
		return 0, false
	}
	return override.Limit, true
}

// EffectiveSourceLimit 用户在某数据源的有效日额度。
// 有效额度 = 套餐限额 + 用户覆盖（覆盖为空视为 0，正值追加、负值扣减，结果下限 0）：
// 优先用户级数据源覆盖，无覆盖时按套餐限额；套餐未限额则为 -1（不限，覆盖不再生效）。
func EffectiveSourceLimit(user *models.User, sourceName string, planLimits map[string]int64) int64 {
	if user != nil && user.ID > 0 {
		// 1) 用户级数据源覆盖：单数据源套餐限额 + 覆盖
		if l, ok := userOverrideLimit(user.ID, sourceName); ok {
			base, hasPlan := planLimits[sourceName]
			if !hasPlan || base < 0 {
				return -1 // 套餐不限（或未限额），覆盖不生效
			}
			return clampLimit(base + l)
		}
	}
	// 2) 套餐限额
	if l, ok := planLimits[sourceName]; ok {
		return l
	}
	return -1 // 无套餐限制则不限
}

// clampLimit 额度下限为 0（覆盖扣减超过套餐限额时视为 0，即当日不可用）
func clampLimit(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

// captureWriter 捕获响应状态码，用于判断上游调用是否成功。
// 数据源处理器成功时直接输出 Legado 格式数据（非 code/msg/data 包装），
// 失败统一走 handleError 返回非 200，因此 HTTP 200 即为成功。
type captureWriter struct {
	gin.ResponseWriter
	status int
}

func (w *captureWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *captureWriter) Write(data []byte) (int, error) {
	return w.ResponseWriter.Write(data)
}

func (w *captureWriter) WriteString(s string) (int, error) {
	return w.ResponseWriter.WriteString(s)
}

func (w *captureWriter) Flush() {
	w.ResponseWriter.Flush()
}

func (w *captureWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(interface {
		Hijack() (net.Conn, *bufio.ReadWriter, error)
	})
	if !ok {
		return nil, nil, nil
	}
	return hijacker.Hijack()
}

func (w *captureWriter) succeeded() bool {
	return w.status == http.StatusOK
}

// cacheTTL 计费配置缓存周期（配置项极少变动，长期缓存安全）
const cacheTTL = 5 * time.Minute

// getCachedCost 从缓存读取 QuotaCost，未命中时查库并填充缓存
func getCachedCost(groupCode, action string) (models.QuotaCost, error) {
	cacheKey := fmt.Sprintf("quota:cost:%s:%s", groupCode, action)
	if cached, ok := utils.DefaultCache().Get(cacheKey); ok {
		if cost, ok := cached.(models.QuotaCost); ok {
			return cost, nil
		}
	}

	var cost models.QuotaCost
	if err := db.DB.Where("group_code = ? AND interface = ?", groupCode, action).First(&cost).Error; err != nil {
		return cost, err
	}

	utils.DefaultCache().Set(cacheKey, cost)
	return cost, nil
}

// BillingMiddleware 数据源调用计费中间件：
//   - 未配置消耗 / 接口禁用 / 消耗为 0 → 直接放行
//   - 无有效 JWT（匿名调用）→ 不计费直接放行
//   - admin 角色 → 不检查不记录
//   - 请求前检查当日剩余额度，不足返回 429
//   - 上游成功后按 QuotaCost 扣减并写入 QuotaUsageLog（并发下允许少量超扣）
func BillingMiddleware(sourceName, action string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 接口消耗配置（带缓存，group_code 列存数据源码）
		cost, err := getCachedCost(sourceName, action)
		if err != nil {
			// 未配置：放行且不计费
			c.Next()
			return
		}

		// 禁用 = 接口停用，对所有调用方生效（与计费开关无关）
		if cost.Status != 1 {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"code": conf.Config.ErrorCode,
				"msg":  "该接口已被管理员禁用",
			})
			return
		}

		// 免费接口、或计费未开启：放行不计费
		if cost.Cost <= 0 || !billingEnabled() {
			c.Next()
			return
		}

		// 用户身份：优先从 context 获取（authMiddleware 已解析 JWT）
		var user models.User
		var userID uint
		if uid, exists := c.Get("user_id"); exists {
			if id, ok := uid.(uint); ok {
				userID = id
			} else {
				c.Next()
				return
			}
		} else {
			// 未通过 authMiddleware（非 AuthRequired 路由），自行解析 JWT
			tokenStr := utils.TokenFromRequest(c)
			if tokenStr == "" {
				c.Next()
				return
			}
			claims, err := utils.ParseToken(tokenStr)
			if err != nil {
				c.Next()
				return
			}
			userID = claims.UserID
		}

		if err := db.DB.Preload("Roles").Preload("Plan").First(&user, userID).Error; err != nil {
			c.Next()
			return
		}

		// 管理员不限不记
		if user.HasRole("admin") {
			c.Next()
			return
		}

		// 请求前额度检查（带缓存）
		plan := ResolvePlan(&user)
		limit := EffectiveSourceLimit(&user, sourceName, PlanSourceLimits(plan.ID))
		if limit >= 0 {
			used := UsedToday(user.ID, sourceName)
			if used+cost.Cost > limit {
				c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
					"code": conf.Config.ErrorCode,
					"msg":  "今日" + sourceName + "数据源额度已用完，请明日再来或联系管理员调整套餐",
				})
				return
			}
		}

		// 上游成功后扣减
		cw := &captureWriter{ResponseWriter: c.Writer, status: 0}
		c.Writer = cw
		c.Next()

		if cw.succeeded() {
			db.DB.Create(&models.QuotaUsageLog{
				UserID:    user.ID,
				GroupCode: sourceName,
				Interface: strings.ToLower(action),
				Cost:      cost.Cost,
			})
		}
	}
}

// DataSourceAccessMiddleware 检查数据源访问权限：
// 1. 数据源是否启用（DataSource.Status = 1）
// 2. 用户套餐是否包含该数据源（QuotaPlanDataSource 关联）
// 管理员跳过检查
func DataSourceAccessMiddleware(sourceName string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 跳过非数据源路由（如 /datasources, /data, /panel 等）
		if sourceName == "" || sourceName == "datasources" || sourceName == "data" || sourceName == "panel" {
			c.Next()
			return
		}

		// 1. 检查数据源是否存在且启用
		var ds models.DataSource
		if err := db.DB.Where("name = ?", sourceName).First(&ds).Error; err != nil {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{
				"code": conf.Config.ErrorCode,
				"msg":  "数据源不存在",
			})
			return
		}
		if ds.Status != 1 {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"code": conf.Config.ErrorCode,
				"msg":  "该数据源已被管理员禁用",
			})
			return
		}

		// 2. 获取用户身份（优先从 context 获取）
		var userID uint
		if uid, exists := c.Get("user_id"); exists {
			if id, ok := uid.(uint); ok {
				userID = id
			}
		} else {
			tokenStr := utils.TokenFromRequest(c)
			if tokenStr == "" {
				// 匿名用户：只允许访问免费套餐包含的数据源
				if !isDataSourceInFreePlan(sourceName) {
					c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
						"code": conf.Config.ErrorCode,
						"msg":  "请登录后访问该数据源",
					})
					return
				}
				c.Next()
				return
			}
			claims, err := utils.ParseToken(tokenStr)
			if err != nil {
				c.Next()
				return
			}
			userID = claims.UserID
		}

		if userID > 0 {
			var user models.User
			if err := db.DB.Preload("Roles").Preload("Plan").First(&user, userID).Error; err != nil {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
					"code": conf.Config.ErrorCode,
					"msg":  "用户不存在",
				})
				return
			}

			// 管理员跳过检查
			if user.HasRole("admin") {
				c.Next()
				return
			}

			// 3. 获取用户生效套餐
			plan := ResolvePlan(&user)
			if plan.ID == 0 {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
					"code": conf.Config.ErrorCode,
					"msg":  "无可用套餐，请联系管理员",
				})
				return
			}

			// 4. 检查套餐是否包含该数据源
			var count int64
			db.DB.Model(&models.QuotaPlanDataSource{}).
				Where("plan_id = ? AND data_source_id = ?", plan.ID, ds.ID).
				Count(&count)
			if count == 0 {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
					"code": conf.Config.ErrorCode,
					"msg":  "当前套餐不包含该数据源，请升级套餐",
				})
				return
			}
		}

		c.Next()
	}
}

// isDataSourceInFreePlan 检查数据源是否在免费套餐中
func isDataSourceInFreePlan(sourceName string) bool {
	var ds models.DataSource
	if err := db.DB.Where("name = ?", sourceName).First(&ds).Error; err != nil {
		return false
	}
	var freePlan models.QuotaPlan
	if err := db.DB.Where("code = ?", "free").First(&freePlan).Error; err != nil {
		return false
	}
	var count int64
	db.DB.Model(&models.QuotaPlanDataSource{}).
		Where("plan_id = ? AND data_source_id = ?", freePlan.ID, ds.ID).
		Count(&count)
	return count > 0
}

// userHasDataSourceAccess 检查用户套餐是否包含该数据源
func userHasDataSourceAccess(user *models.User, sourceName string) bool {
	var ds models.DataSource
	if err := db.DB.Where("name = ?", sourceName).First(&ds).Error; err != nil {
		return false
	}

	plan := ResolvePlan(user)
	if plan.ID == 0 {
		return false
	}

	var count int64
	db.DB.Model(&models.QuotaPlanDataSource{}).
		Where("plan_id = ? AND data_source_id = ?", plan.ID, ds.ID).
		Count(&count)
	return count > 0
}

// ===== 速率限制（窗口计数 / 固定间隔） =====

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
				rateLimiterMu.Lock()
				for k, l := range rateLimiters {
					if l.idleExpired(now) {
						delete(rateLimiters, k)
					}
				}
				rateLimiterMu.Unlock()

				windowLimiterMu.Lock()
				for k, l := range windowLimiters {
					if l.idleExpired(now) {
						delete(windowLimiters, k)
					}
				}
				windowLimiterMu.Unlock()
			}
		}()
	})
}

// idleExpired 是否可回收：闲置时长超过 max(limiterIdleTTL, 2×interval)。
// 闲置超过一个 interval 后令牌已回满，丢弃与保留等价（2× 是安全余量）。
func (rl *rateLimiter) idleExpired(now time.Time) bool {
	rl.mu.Lock()
	ttl := limiterIdleTTL
	if d := time.Duration(rl.interval) * time.Second * 2; d > ttl {
		ttl = d
	}
	rl.mu.Unlock()
	return now.Sub(time.Unix(0, rl.lastSeen.Load())) > ttl
}

// idleExpired 是否可回收：闲置时长超过 max(limiterIdleTTL, 2×window)。
// 闲置超过一个窗口后所有记录都已滑出，丢弃与保留等价。
func (wl *windowLimiter) idleExpired(now time.Time) bool {
	wl.mu.Lock()
	ttl := limiterIdleTTL
	if d := wl.window * 2; d > ttl {
		ttl = d
	}
	wl.mu.Unlock()
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
	rateLimiterMu.RLock()
	limiter, ok := rateLimiters[key]
	rateLimiterMu.RUnlock()
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
	windowLimiterMu.RLock()
	limiter, ok := windowLimiters[key]
	windowLimiterMu.RUnlock()
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

// RateLimitMiddleware 基于 QuotaCost/QuotaCostPlan 的速率限制中间件（导出供 app.go 使用）
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
			// 未通过 authMiddleware 的路由：自行解析 JWT
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
