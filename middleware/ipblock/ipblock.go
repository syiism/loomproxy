// Package ipblock IP 维度的拦截：黑名单命中即拒一切请求，以及自动拉黑的滑动窗口计数。
//
// 计数信号来自 source/monitor（数据源路由的最终响应状态码），拦截发生在链上更靠前的
// 全局位置，因此本包不依赖任何管控逻辑，只依赖 db 与 models。
package ipblock

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"loomproxy/conf"
	"loomproxy/db"
	"loomproxy/middleware"
)

func init() {
	middleware.Register(middleware.Def{
		Name:  "ipblock",
		Scope: middleware.Global,
		Order: middleware.OrderIPBlock,
		Build: func(middleware.Spec) gin.HandlerFunc { return ipBlockMiddleware() },
	})
}

// ipBlockMiddleware IP 黑名单：被拉黑的 IP 拒绝一切请求（登录/注册/接口调用），返回 403
func ipBlockMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if db.IsIPBlocked(c.ClientIP()) {
			c.JSON(http.StatusForbidden, gin.H{
				"code": conf.Config.ErrorCode,
				"msg":  "当前 IP 已被限制访问",
			})
			c.Abort()
			return
		}
		c.Next()
	}
}
