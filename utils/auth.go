package utils

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"loomproxy/conf"
	"loomproxy/db"
)

type AuthException struct {
	Detail     string
	StatusCode int
}

func (e *AuthException) Error() string {
	return e.Detail
}

func NewAuthException(detail string, statusCode int) *AuthException {
	return &AuthException{Detail: detail, StatusCode: statusCode}
}

func IsWhitelisted(path string) bool {
	for _, wl := range conf.Config.AuthWhitelist {
		if wl != "" && (path == wl || strings.HasPrefix(path, wl+"/")) {
			return true
		}
	}
	return false
}

// ErrNoCredential 表示「本次请求压根没带 API Key」，用于把「没带」与「带了但无效」分开：
// 前者归到统一的缺失提示，后者原样回给调用方。
var ErrNoCredential = NewAuthException("缺少鉴权凭证（X-API-Key 或 api_key）", 401)

func VerifyAuth(c *gin.Context) error {
	if !conf.Config.AuthEnabled {
		return nil
	}

	path := c.Request.URL.Path
	if IsWhitelisted(path) {
		return nil
	}

	if err := verifyJWT(c); err == nil {
		return nil
	}

	keyErr := verifyAPIKey(c)
	if keyErr == nil {
		return nil
	}
	// 带了凭证却没通过时说清是哪一个：把「无效的 API Key」统一成「缺少鉴权凭证」，
	// 调用方会以为自己没传参，而真实原因可能是归属用户被禁用、密钥被撤销，甚至是一条 SQL 报错
	if keyErr != ErrNoCredential {
		return keyErr
	}

	return NewAuthException("缺少鉴权凭证（X-API-Key / api_key / Authorization Bearer）", 401)
}

func verifyAPIKey(c *gin.Context) error {
	queryKey := c.Query("api_key")
	headerKey := c.GetHeader("X-API-Key")

	apiKey := queryKey
	if apiKey == "" {
		apiKey = headerKey
	}

	if apiKey == "" {
		return ErrNoCredential
	}

	// 静态 env 键（管理员级，历史行为不变：匿名不归属用户）
	for _, k := range conf.Config.APIKeys {
		if k == apiKey {
			return nil
		}
	}

	// 用户自助密钥：注入归属身份（计费/配额/监控按用户统计，套餐门控随用户生效）
	if id, err := LookupApiKeyIdentity(apiKey); err == nil {
		c.Set("user_id", id.UserID)
		c.Set("username", id.Username)
		c.Set("auth_via", "api_key")
		return nil
	}

	return NewAuthException("无效的 API Key", 403)
}

func verifyJWT(c *gin.Context) error {
	tokenStr := TokenFromRequest(c)

	if tokenStr == "" {
		return NewAuthException("缺少登录凭证", 401)
	}

	claims, err := ParseToken(tokenStr)
	if err != nil {
		if errors.Is(err, ErrTokenExpired) {
			return NewAuthException("登录已过期，请重新登录", 401)
		}
		return NewAuthException("无效的登录凭证", 401)
	}

	// 会话校验：无 jti 的旧 token 或已吊销/过期的会话一律拒绝
	if !db.ValidateSession(claims.ID, claims.UserID) {
		return NewAuthException("登录已失效，请重新登录", 401)
	}

	c.Set("user_id", claims.UserID)
	c.Set("username", claims.Username)
	c.Set("token", tokenStr)
	c.Set("session_id", claims.ID)
	return nil
}

var ErrAuthFailed = errors.New("authentication failed")
