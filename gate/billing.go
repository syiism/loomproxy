package gate

// 额度计费：请求前查当日剩余额度（不足 429），上游成功后按 QuotaCost 扣减并写流水。
//
// 注册 Def：Name="billing"，Scope=Route，Order=500。
// 必须晚于 monitor(200)——被计费拦下的 429 要先进监控明细；也必须晚于 access(400)，
// 否则套餐根本不含该源的用户会先被扣一次额度检查。

import (
	"bufio"
	"log"
	"net"
	"net/http"
	"strings"
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
		Name:    "billing",
		Scope:   middleware.Route,
		Order:   middleware.OrderBilling,
		Applies: func(s middleware.Spec) bool { return s.Source != "" },
		Build:   func(s middleware.Spec) gin.HandlerFunc { return BillingMiddleware(s.Source, s.Action) },
	})
}

// billingEnabled 计费开关：AUTH_ENABLED=false 的开放部署下不做用户级计费
func billingEnabled() bool {
	return conf.Config.AuthEnabled
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

// costCachePrefix 接口消耗缓存键的唯一前缀。
//
// 待办清单 P39：计费读的是这份带 TTL 的缓存，而全仓**只有这里生成键、没有任何一处删除它**——
// 面板改完单价仍按旧价扣，最长 300 秒；同一行里的「禁用接口」同理，**禁用不是立刻停用**。
// 代价不是算错钱（缓存只会晚生效），是运维直觉的反面：改完看不见效果，人就再点一次保存、
// 或者重启进程去「确保生效」（那次重启会连带清掉所有人的限流锁与在途请求）。
//
// 键格式只许出现在这一处：读侧用 `CostCacheKey`、写侧调 `InvalidateCostCache`，
// 测试也走这两个函数——键拼法长出第二份，失效就会打空而构建照样绿。
const costCachePrefix = "quota:cost:"

// CostCacheKey 某个「数据源 × 动作」的计费配置缓存键。
func CostCacheKey(sourceCode, action string) string {
	return costCachePrefix + sourceCode + ":" + action
}

// InvalidateCostCache 让某个「数据源 × 动作」的计费配置立即失效（改价、改开关之后调用）。
func InvalidateCostCache(sourceCode, action string) {
	utils.DefaultCache().Del(CostCacheKey(sourceCode, action))
}

// recordUsage 写一行「今日消耗」流水——它是 UsedToday 的**读对象**，不是留档。
// 所以写失败绝不能静默：请求已经放行、监控记的是成功、面板显示的量偏小，
// 唯一的症状是「这个人的额度怎么永远用不完」，而那句话在日志里没有任何对应行。
// 两处扣减（中间件与聚合扇出）共用这一个出口，形状与文案只有一份。
func recordUsage(userID uint, sourceName, action string, cost int64) {
	if err := db.DB.Create(&models.QuotaUsageLog{
		UserID:    userID,
		GroupCode: sourceName,
		Interface: strings.ToLower(action),
		Cost:      cost,
	}).Error; err != nil {
		log.Printf("ERROR: 额度流水写入失败（用户 %d / 源 %s / 接口 %s / %d 点）：%v"+
			"——这格用量今天没扣上，UsedToday 会偏小",
			userID, sourceName, action, cost, err)
	}
}

// getCachedCost 从缓存读取 QuotaCost，未命中时查库并填充缓存
func getCachedCost(groupCode, action string) (models.QuotaCost, error) {
	cacheKey := CostCacheKey(groupCode, action)
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
//   - 上游成功后按 QuotaCost 扣减并写入 QuotaUsageLog（并发下允许少量超扣）；
//     同接口同内容在 BILLING_DEDUPE_SEC 窗口内只扣一次（见 dedupe.go，待办清单 P25）
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

		// 用户身份：优先从 context 获取（apiauth 已解析 JWT）
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
			// 未通过 apiauth（非 AuthRequired 路由），自行解析 JWT
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
			used := UsedToday(&user, sourceName)
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

		// 冷却期内同一篇内容不重复扣（待办清单 P25）：请求照常放行、明细照常记，
		// 只是不再写第二条流水。标识取不到时 alreadyDeducted 返回 false，等于按请求扣。
		if cw.succeeded() && !alreadyDeducted(c, user.ID, sourceName, action) {
			recordUsage(user.ID, sourceName, action, cost.Cost)
		}
	}
}

// AggregateTargetVerdict 扇出到某个**非路由源**之前的一次额度判定，用的是与
// BillingMiddleware 完全同一套原语（getCachedCost / ResolvePlan / EffectiveSourceLimit / UsedToday），
// 判定顺序也照抄——为的是「聚合调某个源」与「直接调某个源」得出同一个答案。
//
// 为什么必须在这里判一遍：聚合搜索在 handler 内部调其他源，那条路不经过中间件链，
// access/billing/ratelimit 三道闸门对它是盲的。不补这一刀，「一次请求换 N 个上游」
// 就是额度上的空头支票——免费档 200 次/日能打出 200×N 次上游调用。
//
// reason 是给 sources_status 用的枚举，**不带上游的错误文案**（对外文案必须过脱敏，
// 而这里连累一个失败目标就不该把它的 URL 吐出去）。
func AggregateTargetVerdict(user *models.User, sourceName, action string) (allowed bool, cost int64, reason string) {
	stored, err := getCachedCost(sourceName, action)
	if err != nil {
		return true, 0, "" // 未配置成本：与中间件一致，放行且不计费
	}
	if stored.Status != 1 {
		return false, 0, "disabled" // 接口被管理员停用，对所有调用方生效
	}
	if stored.Cost <= 0 || !billingEnabled() {
		return true, 0, "" // 免费接口或计费总开关关闭
	}
	if user == nil || user.HasRole("admin") {
		return true, stored.Cost, "" // 匿名与 admin：中间件里也是不记账
	}
	plan := ResolvePlan(user)
	limit := EffectiveSourceLimit(user, sourceName, PlanSourceLimits(plan.ID))
	if limit >= 0 && UsedToday(user, sourceName)+stored.Cost > limit {
		return false, stored.Cost, "limit_exceeded"
	}
	return true, stored.Cost, ""
}

// DeductAggregateTarget 为一个扇出目标扣一次用量，写入与中间件同形状的一行流水。
//
// 已知偏差（写清楚，别让它长成「没人知道的第二套口径」）：内容维度是 handler 返回之后
// 才由 ObserveCall 回填的，所以聚合请求在扣减这一刻 subject 还是空的，
// P25 的「同一篇内容冷却期内不重复扣」对扇出目标**不生效**——
// 结果是偏多扣而不是偏少扣，与「聚合不得比直连更便宜」一致。
// 要抹平这条得把 subject 的生命周期提前，动到计费主路径，风险大于收益。
func DeductAggregateTarget(c *gin.Context, user *models.User, sourceName, action string, cost int64) {
	if user == nil || cost <= 0 || user.HasRole("admin") {
		return
	}
	if alreadyDeducted(c, user.ID, sourceName, action) {
		return
	}
	recordUsage(user.ID, sourceName, action, cost)
}
