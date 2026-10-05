package transport

// recovery 注册 Def：Name="recovery"，Scope=Global，Order=20。
// 紧跟 requestid，使后续任何环节 panic 都能兜成统一 JSON 错误体而非断连。

import (
	"log"
	"net/http"
	"runtime/debug"

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
				// 栈要打出来：`PANIC: %v` 只有一行结论时，"哪一层炸的"仍然得靠复现——
				// 而这里炸的往往就是"复现不出来"的那一类（响应已经写出去了才炸）。
				log.Printf("PANIC: %v\n%s", r, debug.Stack())
				if c.Writer.Written() {
					// 响应头与正文已经发过：再写一份信封不会替换它，只会在同一个连接上
					// **接第二个 JSON**（`{503 信封}{500 信封}`），下游整体解析必炸。
					// 这时唯一能做的是不再出声，状态码已经定了。
					log.Printf("ERROR: 上面的 panic 发生在响应已写出之后，本次不再补错误信封（status=%d）", c.Writer.Status())
					c.Abort()
					return
				}
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
