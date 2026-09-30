package transport

// cachecontrol 注册 Def：Name="cachecontrol"，Scope=Global，Order=50。
// 网关响应随用户套餐/额度而变，统一禁缓存以免中间层把 A 用户的额度视图发给 B。

import (
	"github.com/gin-gonic/gin"

	"loomproxy/middleware"
)

func init() {
	middleware.Register(middleware.Def{
		Name:  "cachecontrol",
		Scope: middleware.Global,
		Order: middleware.OrderCacheControl,
		Build: func(middleware.Spec) gin.HandlerFunc { return cacheControlMiddleware() },
	})
}

func cacheControlMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Cache-Control", "max-age=0, no-cache, no-store, must-revalidate")
		c.Writer.Header().Set("Pragma", "no-cache")

		c.Next()
	}
}
