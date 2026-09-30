// Package source 数据源请求进入源实现之前的准备环节：baseUrl 解析（含用户/平台配置回落）
// 与 SSRF 校验，以及调用监控计数。
//
// 两件事同包是因为它们同属「这条请求要打哪个上游、打得怎么样」，且都按 spec.Source 取参。
package source

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"loomproxy/base"
	"loomproxy/conf"
	"loomproxy/db"
	"loomproxy/middleware"
	"loomproxy/models"
	"loomproxy/utils"
)

func init() {
	middleware.Register(middleware.Def{
		Name:  "baseurl",
		Scope: middleware.Route,
		Order: middleware.OrderBaseURLCheck,
		// 全量挂载（等价原 app.go 的无条件 append）：控制面路由 spec.Source 为空时
		// 自然整段跳过，但请求显式带 baseUrl 参数时仍要过 SSRF 校验。
		Build: func(s middleware.Spec) gin.HandlerFunc { return baseURLCheckMiddleware(s.Source) },
	})
}

func baseURLCheckMiddleware(sourceName string) gin.HandlerFunc {
	return func(c *gin.Context) {
		baseURL := c.Query("baseUrl")
		// fromPlatform 标记 baseUrl 是否来自平台默认配置（管理员设置，可信）
		fromPlatform := false

		// 源声明 FixedBaseURL 的（上游地址写死在实现里）跳过 baseUrl 解析与 SSRF 校验
		meta, _ := base.GetSourceMeta(sourceName)
		skipBaseURLCheck := meta.FixedBaseURL

		if !skipBaseURLCheck && baseURL == "" && sourceName != "" {
			// 1) 用户个人配置
			if uid, exists := c.Get("user_id"); exists {
				if userID, ok := uid.(uint); ok && userID > 0 {
					var cfg models.UserSourceConfig
					err := db.DB.Where("user_id = ? AND source_name = ?", userID, sourceName).First(&cfg).Error
					if err == nil && cfg.BaseURL != "" {
						baseURL = cfg.BaseURL
					}
					//err == gorm.ErrRecordNotFound // 是正常情况，不记录日志
				}
			}
			// 2) 平台默认配置
			if baseURL == "" {
				var pcfg models.PlatformSourceConfig
				err := db.DB.Where("source_name = ?", sourceName).First(&pcfg).Error
				if err == nil && pcfg.BaseURL != "" {
					baseURL = pcfg.BaseURL
					fromPlatform = true
				}
			}
			if baseURL != "" {
				c.Set(middleware.CtxResolvedBaseURL, baseURL)
			}
		}

		// SSRF 校验：请求参数与用户个人配置的 baseUrl 一律严格校验（拒绝回环/私网）；
		// 平台默认配置由管理员设置，视为可信来源——允许指向部署在本机/内网的
		// 数据源项目（如 http://127.0.0.1:8080），无需暴露公网
		if !skipBaseURLCheck && baseURL != "" && !fromPlatform {
			if !utils.IsSafeURL(baseURL) {
				c.Error(base.ErrUnsafeBaseURL)
				c.JSON(http.StatusBadRequest, gin.H{
					"code": conf.Config.ErrorCode,
					"msg":  base.ErrUnsafeBaseURL.Error() + ": " + baseURL,
				})
				c.Abort()
				return
			}
		}

		c.Next()
	}
}
