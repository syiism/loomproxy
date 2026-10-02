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

// Credential 一次凭证解析的结果。三种形态——JWT（`Authorization: Bearer` / `?token=` / HttpOnly Cookie）、
// 用户级 `lp_` 密钥、静态 env 键——在这里收敛成一个形状，**网关层与控制面共用同一份解析**
// （待办清单 P26：两处各写一遍「先 JWT 后密钥」的顺序与失败文案，迟早漂移）。
//
// EnvKey=true 表示管理员级静态键：它按设计**匿名、不归属任何账号**（AGENTS §7），
// 所以任何需要「你是谁」的端点都必须拒绝它，而不是把它当成一个用户。
type Credential struct {
	Method    string // "jwt" 或 "api_key"
	UserID    uint
	Username  string
	Token     string
	SessionID string
	EnvKey    bool
}

func VerifyAuth(c *gin.Context) error {
	if !conf.Config.AuthEnabled {
		return nil
	}

	path := c.Request.URL.Path
	if IsWhitelisted(path) {
		return nil
	}

	if err := resolveJWT(c); err == nil {
		return nil
	}

	keyErr := resolveAPIKey(c)
	if keyErr == nil {
		return nil
	}
	// 带了凭证却没通过时说清是哪一个：把「无效的 API Key」统一成「缺少鉴权凭证」，
	// 调用方会以为自己没传参，而真实原因可能是归属用户被禁用、密钥被撤销，甚至是一条 SQL 报错
	if keyErr != ErrNoCredential {
		return keyErr
	}

	return NewAuthException("缺少鉴权凭证（Authorization Bearer / Cookie / X-API-Key / api_key）", 401)
}

// AuthenticateAny 给「三形态都要能用」的控制面端点用：JWT 或**用户级**密钥。
// 与 VerifyAuth 的差别只在策略：这里不看 AUTH_ENABLED、不看白名单（控制面本来就 always 要凭证），
// 并且静态 env 键视为「没有身份」——控制面每个端点都要落到某个账号上。
func AuthenticateAny(c *gin.Context) error {
	jwtErr := resolveJWT(c)
	if jwtErr == nil {
		return nil
	}
	cred, keyErr := resolveAPIKeyWithCredential(c)
	if keyErr == nil {
		if cred.EnvKey || cred.UserID == 0 {
			return NewAuthException("该端点需要归属账号：管理员静态密钥是匿名凭证，请改用账号 API Key 或登录会话", 403)
		}
		return nil
	}
	// 两种凭证都试过了：带了密钥但密钥本身有问题时，报密钥的错；否则报会话的错
	if keyErr != ErrNoCredential {
		return keyErr
	}
	if jwtErr != ErrNoLoginCredential {
		return jwtErr
	}
	return NewAuthException("缺少鉴权凭证（Authorization Bearer / Cookie / X-API-Key / api_key）", 401)
}

// ErrNoLoginCredential 「压根没带 JWT」，与「带了但过期/失效」分开
var ErrNoLoginCredential = NewAuthException("缺少登录凭证", 401)

func resolveAPIKey(c *gin.Context) error {
	_, err := resolveAPIKeyWithCredential(c)
	return err
}

func resolveAPIKeyWithCredential(c *gin.Context) (*Credential, error) {
	queryKey := c.Query("api_key")
	headerKey := c.GetHeader("X-API-Key")

	apiKey := queryKey
	if apiKey == "" {
		apiKey = headerKey
	}

	if apiKey == "" {
		return nil, ErrNoCredential
	}

	// 静态 env 键（管理员级，历史行为不变：匿名不归属用户）
	for _, k := range conf.Config.APIKeys {
		if k == apiKey {
			return &Credential{Method: "api_key", EnvKey: true}, nil
		}
	}

	// 用户自助密钥：注入归属身份（计费/配额/监控按用户统计，套餐门控随用户生效）
	if id, err := LookupApiKeyIdentity(apiKey); err == nil {
		c.Set("user_id", id.UserID)
		c.Set("username", id.Username)
		c.Set("auth_via", "api_key")
		return &Credential{Method: "api_key", UserID: id.UserID, Username: id.Username}, nil
	}

	return nil, NewAuthException("无效的 API Key", 403)
}

func resolveJWT(c *gin.Context) error {
	tokenStr := TokenFromRequest(c)

	if tokenStr == "" {
		return ErrNoLoginCredential
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
