package transport

// requestid 注册 Def：Name="requestid"，Scope=Global，Order=10。
// 必须全链最前——其后 recovery 与 logging 的输出都要带上同一个请求标识。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"loomproxy/base"
	"loomproxy/middleware"
)

func init() {
	middleware.Register(middleware.Def{
		Name:  "requestid",
		Scope: middleware.Global,
		Order: middleware.OrderRequestID,
		Build: func(middleware.Spec) gin.HandlerFunc { return requestIDMiddleware() },
	})
}

func requestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := c.GetHeader("X-Request-ID")
		if requestID == "" {
			requestID = generateRequestID()
		}
		c.Writer.Header().Set("X-Request-ID", requestID)
		c.Set(middleware.CtxRequestID, requestID)

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
