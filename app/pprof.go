package app

import (
	"net/http/pprof"

	"github.com/gin-gonic/gin"

	"loomproxy-go/handlers/auth"
)

// registerPprofRoutes 挂载 Go 运行时 profiling 端点，整组由 AdminRequired 保护——
// pprof 暴露内存/goroutine 等内部状态，必须视为管理接口。
// 用法：go tool pprof -H "Authorization: Bearer <token>" http://host:8081/debug/pprof/heap
// （block/mutex profile 默认未开启采样，需要时在代码中 runtime.SetBlockProfileRate 启用）
func registerPprofRoutes(r *gin.Engine) {
	g := r.Group("/debug/pprof", auth.AdminRequired())
	g.GET("/", gin.WrapF(pprof.Index))
	g.GET("/cmdline", gin.WrapF(pprof.Cmdline))
	g.GET("/profile", gin.WrapF(pprof.Profile))
	g.POST("/symbol", gin.WrapF(pprof.Symbol))
	g.GET("/symbol", gin.WrapF(pprof.Symbol))
	g.GET("/trace", gin.WrapF(pprof.Trace))
	for _, name := range []string{"allocs", "block", "goroutine", "heap", "mutex", "threadcreate"} {
		g.GET("/"+name, gin.WrapH(pprof.Handler(name)))
	}
}
