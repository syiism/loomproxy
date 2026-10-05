// Package apiauth API 网关层鉴权（JWT / API Key），单路由作用域。
//
// 与 handlers/auth 的区别：那边是「登录换 token」的端点，这里是「带 token 的请求」
// 在每条数据源/受保护路由上被校验的一次。与用户层鉴权（AuthRequired/AdminRequired）
// 是两层，见 AGENTS.md §7。
package apiauth

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"loomproxy/conf"
	"loomproxy/middleware"
	"loomproxy/utils"
)

func init() {
	middleware.Register(middleware.Def{
		Name:  "apiauth",
		Scope: middleware.Route,
		Order: middleware.OrderAPIAuth,
		// 只挂在声明了 AuthRequired 的路由上（等价原 app.go 的 if h.AuthRequired()）
		Applies: func(s middleware.Spec) bool { return s.AuthRequired },
		Build:   func(middleware.Spec) gin.HandlerFunc { return authMiddleware() },
	})
}

func authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !conf.Config.AuthEnabled {
			// 可选鉴权（待办清单 P64）：网关关着也解析凭证，解析成功就把身份挂上——
			// /endpoints 这类自读 user_id 的端点过去把「网关关了」当「没登录」，
			// 带着合法 JWT 也拿 401。解析失败按匿名放行：不强制是这条开关的全部语义。
			_ = utils.VerifyAuth(c)
			c.Next()
			return
		}

		err := utils.VerifyAuth(c)
		if err != nil {
			statusCode := http.StatusUnauthorized
			if ae, ok := err.(*utils.AuthException); ok {
				statusCode = ae.StatusCode
			}
			c.JSON(statusCode, gin.H{
				"code": conf.Config.ErrorCode,
				"msg":  err.Error(),
			})
			c.Abort()
			return
		}

		c.Next()
	}
}
