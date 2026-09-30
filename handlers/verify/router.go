package verify

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"loomproxy/conf"
)

// ok/fail 与 handlers/auth.Ok/Fail 同构的响应信封；verify 不能反向 import auth
// （auth 的 Register/ForgotPassword 会调用本包 Check/Consume，构成环）。

func ok(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, gin.H{"code": 0, "msg": "ok", "data": data})
}

func fail(c *gin.Context, status int, msg string) {
	c.JSON(status, gin.H{"code": conf.Config.ErrorCode, "msg": msg})
}

// RegisterRoutes 挂载公开验证码端点（免 JWT；受全局 ipBlock 中间件保护）。
// /verify/config 给前端决定是否渲染验证码输入框；/verify/send 发码。
func RegisterRoutes(r *gin.Engine) {
	r.GET("/verify/config", func(c *gin.Context) {
		ok(c, gin.H{
			"scenes":        EnabledScenes(),
			"ttl":           int(ttl().Seconds()),
			"send_interval": int(interval().Seconds()),
		})
	})

	r.POST("/verify/send", func(c *gin.Context) {
		var req struct {
			Scene  string `json:"scene" binding:"required"`
			Target string `json:"target" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, "参数错误: 需要 scene 与 target")
			return
		}
		expires, err := Issue(c.ClientIP(), req.Scene, req.Target)
		if err != nil {
			// 用 errors.Is 匹配：冷却等错误带 %w 包装（附加重试秒数）
			switch {
			case errors.Is(err, ErrSendTooFrequent), errors.Is(err, ErrSendLimitDaily):
				fail(c, http.StatusTooManyRequests, err.Error())
			case errors.Is(err, ErrSceneUnknown), errors.Is(err, ErrSceneDisabled), errors.Is(err, ErrTargetInvalid):
				fail(c, http.StatusBadRequest, err.Error())
			default:
				// 发送失败等带包装信息的错误统一 400（msg 已面向用户脱敏）
				fail(c, http.StatusBadRequest, err.Error())
			}
			return
		}
		ok(c, gin.H{
			"message":    "验证码已发送，请查收",
			"expires_at": expires.Unix(),
		})
	})
}
