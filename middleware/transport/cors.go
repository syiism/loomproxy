package transport

// cors 注册 Def：Name="cors"，Scope=Global，Order=40。
// 必须早于 ipblock：预检请求（OPTIONS）不带业务语义，不能被黑名单拦死。

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"loomproxy/middleware"
)

func init() {
	middleware.Register(middleware.Def{
		Name:  "cors",
		Scope: middleware.Global,
		Order: middleware.OrderCORS,
		Build: func(middleware.Spec) gin.HandlerFunc { return corsMiddleware() },
	})
}

// CORS 策略：面向开放阅读器 API 场景，允许所有来源。
// 生产环境如需收紧，请修改 Access-Control-Allow-Origin 为具体域名列表。
func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-API-Key, Authorization")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}
