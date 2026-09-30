package utils

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"loomproxy-go/conf"
	"loomproxy-go/models"
)

// TokenCookieName 登录态 Cookie 名，值为 JWT（与 Authorization Bearer 相同）
const TokenCookieName = "loomproxy_token"

var (
	ErrTokenInvalid = errors.New("invalid token")
	ErrTokenExpired = errors.New("token expired")
)

type JWTClaims struct {
	UserID   uint   `json:"uid"`
	Username string `json:"username"`
	jwt.RegisteredClaims
}

// GenerateToken 生成 JWT；sessionID 作为 jti 写入，用于会话（登录设备）管理。
// expireHours 由调用方按「用户个人设置 > 系统设置 > 环境变量 > 默认」解析后传入：
// -1 = 永不过期（不写 exp 声明，返回的 expiresAt 为零值）；<=0（除 -1）回退默认 7 天
func GenerateToken(user *models.User, sessionID string, expireHours int) (string, time.Time, error) {
	claims := JWTClaims{
		UserID:   user.ID,
		Username: user.Username,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:       sessionID,
			IssuedAt: jwt.NewNumericDate(time.Now()),
			Issuer:   "loomproxy",
			Subject:  "user",
		},
	}

	var expiresAt time.Time
	if expireHours == -1 {
		// 永不过期：省略 exp 声明，expiresAt 返回零值表示“无到期时间”
	} else {
		if expireHours <= 0 {
			expireHours = 24 * 7
		}
		expiresAt = time.Now().Add(time.Duration(expireHours) * time.Hour)
		claims.ExpiresAt = jwt.NewNumericDate(expiresAt)
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(conf.Config.JWTSecret))
	if err != nil {
		return "", time.Time{}, err
	}

	return signed, expiresAt, nil
}

// SetTokenCookie 将 JWT 写入 HttpOnly Cookie；-1（永不过期）写 10 年，<=0 回退默认 7 天
func SetTokenCookie(c *gin.Context, token string, expireHours int) {
	maxAge := expireHours * 3600
	if expireHours == -1 {
		maxAge = 10 * 365 * 24 * 3600 // 十年，工程上的“永不”
	} else if expireHours <= 0 {
		maxAge = 24 * 7 * 3600
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     TokenCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearTokenCookie 清除登录态 Cookie
func ClearTokenCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     TokenCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// TokenFromRequest 按优先级从请求中提取 JWT：Authorization Bearer → ?token= → Cookie
func TokenFromRequest(c *gin.Context) string {
	if authHeader := c.GetHeader("Authorization"); strings.HasPrefix(authHeader, "Bearer ") {
		return strings.TrimPrefix(authHeader, "Bearer ")
	}
	if t := c.Query("token"); t != "" {
		return t
	}
	if cookie, err := c.Cookie(TokenCookieName); err == nil {
		return cookie
	}
	return ""
}

func ParseToken(tokenString string) (*JWTClaims, error) {
	claims := &JWTClaims{}

	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, ErrTokenInvalid
		}
		return []byte(conf.Config.JWTSecret), nil
	})

	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrTokenExpired
		}
		return nil, ErrTokenInvalid
	}

	if !token.Valid {
		return nil, ErrTokenInvalid
	}

	return claims, nil
}
