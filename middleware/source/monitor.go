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
	"loomproxy/models"
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
		// 内容维度由 app 挂在 context 上、handler 返回后回填；被管控拦下的请求从没进过
		// handler，取到 nil 就是全空——各维度留空而不是编造一个值
		raw, _ := c.Get(middleware.CtxCallSubject)
		subject, _ := raw.(*base.CallSubject)
		// 隐私协议（同意位见 models/user.go）：用户不同意留存搜索词与阅读记录时，
		// 这里是**最后一道闸**——维度在写进环形缓冲之前就被抹掉，此后进程外没有任何一份副本，
		// 不是「先落库、展示时再遮」。匿名/env 键调用没有用户对象，按默认档（同意）走，
		// 那一档本来就只有 IP 可数。
		if v, ok := c.Get(middleware.CtxCurrentUser); ok {
			if user, isUser := v.(*models.User); isUser && !user.KeepsContentData() {
				subject.WithholdContent()
			}
		}
		base.RecordCall(source, action, uname, c.ClientIP(), status, time.Since(start), subject)
		ipblock.RecordFailure(c.ClientIP(), status)
	}
}
