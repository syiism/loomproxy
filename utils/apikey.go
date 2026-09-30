// Package utils —— apikey.go：用户自助 API 密钥的生成与网关校验。
// 明文仅创建时返回一次，库内只存 SHA-256（同卡密惯例）；校验走 utils.DefaultCache
// 短缓存 + 查库，命中身份注入请求上下文（计费/配额/监控按归属用户统计）。
// LastUsedAt 节流写（5 分钟），避免每请求写库。
package utils

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync/atomic"
	"time"

	"loomproxy/db"
	"loomproxy/models"
)

const ApiKeyPrefix = "lp_"

// apiKeyTouchIntervalMs LastUsedAt 节流写间隔。
const apiKeyTouchIntervalMs = int64(5 * 60 * 1000)

// GenerateApiKey 生成新密钥：lp_ + 24 字节 crypto/rand hex（明文落库，列表可随时查回）。
func GenerateApiKey() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成随机数失败: %w", err)
	}
	return ApiKeyPrefix + hex.EncodeToString(b), nil
}

// ApiKeyIdentity 密钥归属身份（缓存值；lastWriteMs 供 LastUsedAt 节流）。
type ApiKeyIdentity struct {
	ApiKeyID    uint
	UserID      uint
	Username    string
	lastWriteMs int64
}

// LookupApiKeyIdentity 按明文查用户密钥归属（缓存 + 查库；禁用/已删除用户视为无效）。
// 静态 env 键不经此函数（verifyAPIKey 先行短路，保持匿名行为不变）。
func LookupApiKeyIdentity(plain string) (*ApiKeyIdentity, error) {
	cacheKey := "apikey:" + plain
	if v, ok := DefaultCache().Get(cacheKey); ok {
		if id, ok := v.(*ApiKeyIdentity); ok && id != nil {
			touchApiKeyLastUsed(plain, id)
			return id, nil
		}
	}
	var ak models.ApiKey
	if err := db.DB.Where("key = ?", plain).First(&ak).Error; err != nil {
		return nil, fmt.Errorf("API Key 不存在")
	}
	var user models.User
	if err := db.DB.First(&user, ak.UserID).Error; err != nil || user.Status == 0 {
		return nil, fmt.Errorf("API Key 归属用户不可用")
	}
	id := &ApiKeyIdentity{ApiKeyID: ak.ID, UserID: user.ID, Username: user.Username}
	DefaultCache().Set(cacheKey, id)
	touchApiKeyLastUsed(plain, id)
	return id, nil
}

// InvalidateApiKeyCache 撤销后清除缓存（下次请求立即失效）。
func InvalidateApiKeyCache(plain string) {
	DefaultCache().Del("apikey:" + plain)
}

// touchApiKeyLastUsed LastUsedAt 节流写：距上次落库超 5 分钟才异步更新（CAS 防并发重复）。
func touchApiKeyLastUsed(hash string, id *ApiKeyIdentity) {
	now := time.Now().UnixMilli()
	if now-atomic.LoadInt64(&id.lastWriteMs) < apiKeyTouchIntervalMs {
		return
	}
	if !atomic.CompareAndSwapInt64(&id.lastWriteMs, atomic.LoadInt64(&id.lastWriteMs), now) {
		return
	}
	go func(apiKeyID uint, ts time.Time) {
		if db.DB == nil {
			return
		}
		_ = db.DB.Model(&models.ApiKey{}).Where("id = ?", apiKeyID).Update("last_used_at", ts).Error
	}(id.ApiKeyID, time.Now())
}
