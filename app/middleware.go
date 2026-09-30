package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"loomproxy-go/base"
	"loomproxy-go/conf"
	"loomproxy-go/db"
	"loomproxy-go/models"
	"loomproxy-go/utils"
)

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

func authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !conf.Config.AuthEnabled {
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

// ipBlockMiddleware IP 黑名单：被拉黑的 IP 拒绝一切请求（登录/注册/接口调用），返回 403
func ipBlockMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if db.IsIPBlocked(c.ClientIP()) {
			c.JSON(http.StatusForbidden, gin.H{
				"code": conf.Config.ErrorCode,
				"msg":  "当前 IP 已被限制访问",
			})
			c.Abort()
			return
		}
		c.Next()
	}
}

func corsMiddleware() gin.HandlerFunc {
	// CORS 策略：面向开放阅读器 API 场景，允许所有来源。
	// 生产环境如需收紧，请修改 Access-Control-Allow-Origin 为具体域名列表。
	return func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-API-Key, Authorization")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
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
				c.Set("_resolved_baseUrl", baseURL)
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
		RecordAutoBlockFailure(c.ClientIP(), status)
	}
}

func cacheControlMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Cache-Control", "max-age=0, no-cache, no-store, must-revalidate")
		c.Writer.Header().Set("Pragma", "no-cache")

		c.Next()
	}
}

func requestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := c.GetHeader("X-Request-ID")
		if requestID == "" {
			requestID = generateRequestID()
		}
		c.Writer.Header().Set("X-Request-ID", requestID)
		c.Set("request_id", requestID)

		// 存入请求 context，供 handler 传播到上游
		ctx := context.WithValue(c.Request.Context(), base.RequestIDKey, requestID)
		c.Request = c.Request.WithContext(ctx)

		c.Next()
	}
}

func generateRequestID() string {
	return "req_" + time.Now().Format("20060102150405") + "_" + strings.ToLower(randomString(8))
}

func randomString(n int) string {
	// n 个 hex 字符需要 ceil(n/2) 字节
	b := make([]byte, (n+1)/2)
	if _, err := rand.Read(b); err != nil {
		for i := range b {
			b[i] = byte(i*7 + 13)
		}
	}
	return hex.EncodeToString(b)[:n]
}
