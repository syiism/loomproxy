package gate

// 额度计费：请求前查当日剩余额度（不足 429），上游成功后按 QuotaCost 扣减并写流水。
//
// 注册 Def：Name="billing"，Scope=Route，Order=500。
// 必须晚于 monitor(200)——被计费拦下的 429 要先进监控明细；也必须晚于 access(400)，
// 否则套餐根本不含该源的用户会先被扣一次额度检查。

import (
	"bufio"
	"fmt"
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
