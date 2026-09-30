package source

// monitor 注册 Def：Name="monitor"，Scope=Route，Order=200。
// 必须早于 access/billing/ratelimit（400/500/600）——它们是 403/429 的来源，
// 监控要覆盖这些被拦掉的请求；也晚于 apiauth，否则明细里拿不到 username。

import (
	"time"

	"github.com/gin-gonic/gin"

	"loomproxy/base"
	"loomproxy/middleware"
	"loomproxy/middleware/ipblock"
)

func init() {
	middleware.Register(middleware.Def{
		Name:  "monitor",
		Scope: middleware.Route,
		Order: middleware.OrderMonitor,
		// 只统计数据源路由（等价原 app.go 的 if source != ""）
		Applies: func(s middleware.Spec) bool { return s.Source != "" },
		Build:   func(s middleware.Spec) gin.HandlerFunc { return monitorMiddleware(s.Source, s.Action) },
	})
}

// monitorMiddleware 数据源接口调用监控（内存计数，与额度计费无关）：
// 统计到达路由的全部请求（含访问控制 403、计费 429、上游错误等），
// 需置于访问控制与计费中间件之前才能覆盖这些失败响应。
func monitorMiddleware(source, action string) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		// JWT 调用可拿到用户名；API Key / 匿名调用为空
		username, _ := c.Get("username")
		uname, _ := username.(string)
		status := c.Writer.Status()
		base.RecordCall(source, action, uname, c.ClientIP(), status, time.Since(start))
		ipblock.RecordFailure(c.ClientIP(), status)
	}
}
