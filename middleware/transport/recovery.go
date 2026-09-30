package transport

// recovery 注册 Def：Name="recovery"，Scope=Global，Order=20。
// 紧跟 requestid，使后续任何环节 panic 都能兜成统一 JSON 错误体而非断连。

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"

	"loomproxy/conf"
	"loomproxy/middleware"
)

func init() {
	middleware.Register(middleware.Def{
		Name:  "recovery",
		Scope: middleware.Global,
		Order: middleware.OrderRecovery,
		Build: func(middleware.Spec) gin.HandlerFunc { return recoveryMiddleware() },
	})
}

func recoveryMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("PANIC: %v", r)
				c.JSON(http.StatusInternalServerError, gin.H{
					"code": conf.Config.ErrorCode,
					"msg":  "内部服务异常",
				})
				c.Abort()
			}
		}()
		c.Next()
	}
}
