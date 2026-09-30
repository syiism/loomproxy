package transport

// logging 注册 Def：Name="logging"，Scope=Global，Order=30。
// 置于 recovery 之后，panic 兜底后的最终状态码才会进访问日志。

import (
	"log"
	"time"

	"github.com/gin-gonic/gin"

	"loomproxy/middleware"
)

func init() {
	middleware.Register(middleware.Def{
		Name:  "logging",
		Scope: middleware.Global,
		Order: middleware.OrderLogging,
		Build: func(middleware.Spec) gin.HandlerFunc { return loggingMiddleware() },
	})
}

func loggingMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path

		c.Next()

		latency := time.Since(start)
		statusCode := c.Writer.Status()
		clientIP := c.ClientIP()
		method := c.Request.Method

		log.Printf("%s %s %d %s %s",
			method,
			path,
			statusCode,
			latency,
			clientIP,
		)
	}
}
