// Package utils —— apikey.go：用户自助 API 密钥的生成与网关校验。
// 密钥**明文落库**：校验时要按呈现的明文反查归属，这一版选了存明文而不是 SHA-256
// （待办清单 P10 的判定：维持保留明文；代价是库被读走等于密钥被读走）。
// **列表明文可查回**（`handlers/apikey` 的 `List` 原样返回 `models.ApiKey`，`test/apikey_test.go` 第 3 步钉的就是这个语义）——
// 别按「明文只返回一次」的假设往下写代码，那是旧版注释留下的错印象，两处注释本轮已按代码改掉。
// 校验走 utils.DefaultCache 短缓存 + 查库，命中身份注入请求上下文（计费/配额/监控按归属用户统计）；
// 缓存只在**建键那一次**查过归属，所以禁用/删除用户必须显式失效它
// （见 `InvalidateUserApiKeys`，待办清单 P47）。LastUsedAt 节流写（5 分钟），避免每请求写库。
package utils

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
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

// apiKeyCachePrefix 用户自助密钥的归属缓存前缀（与密钥本身的前缀 `lp_` 不是一回事）。
const apiKeyCachePrefix = "apikey:"

// apiKeyCacheKey 密钥归属缓存的键。**拼法只在这一处**：读侧、撤销单把、按用户批量失效都调它。
// 长出第二份拼法的后果是「撤销照样返回成功、缓存照样命中」——那是 P39 那一族的同一个错。
func apiKeyCacheKey(plain string) string { return apiKeyCachePrefix + plain }

// LookupApiKeyIdentity 按明文查用户密钥归属（缓存 + 查库；禁用/已删除用户视为无效）。
// 静态 env 键不经此函数（verifyAPIKey 先行短路，保持匿名行为不变）。
func LookupApiKeyIdentity(plain string) (*ApiKeyIdentity, error) {
	cacheKey := apiKeyCacheKey(plain)
	if v, ok := DefaultCache().Get(cacheKey); ok {
		if id, ok := v.(*ApiKeyIdentity); ok && id != nil {
			touchApiKeyLastUsed(plain, id)
			return id, nil
		}
	}
	var ak models.ApiKey
	// 条件用 map 形式：GORM 会按方言给列名加引号。裸写 "key = ?" 会被原样下发，
	// 而 KEY 是 MySQL 保留字 → Error 1064 → 所有 API Key 在 MySQL 部署上直接判成「不存在」
	// （SQLite 容忍裸写，所以用例全绿、只有生产暴露，见 AGENTS §10）
	if err := db.DB.Where(map[string]interface{}{"key": plain}).First(&ak).Error; err != nil {
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

// InvalidateApiKeyCache 撤销单把密钥后清除缓存（下次请求立即失效）。
func InvalidateApiKeyCache(plain string) {
	DefaultCache().Del(apiKeyCacheKey(plain))
}

// InvalidateUserApiKeys 清掉某用户名下**全部**密钥的归属缓存，返回清掉的把数。
//
// 为什么需要它（待办清单 P47）：归属缓存的**命中分支不复查 `users.status`**，
// 所以管理员把人禁用或删除之后，他手里的密钥在缓存过期之前仍按原归属通过鉴权——
// 而「禁用这个账号」的语义是**立刻**失去访问。禁用与删除都吊销了会话，会话那条路是立刻断的；
// 密钥这条路要在这里断。
// 缓存值是按明文密钥建的，撤销一处只能删一键，所以这里必须按 user_id 把键捞出来逐个删。
func InvalidateUserApiKeys(userID uint) int {
	var plains []string
	if err := db.DB.Model(&models.ApiKey{}).
		Where("user_id = ?", userID).Pluck("key", &plains).Error; err != nil {
		// 不吞错：失效没做成就是「禁用了却还能用」，这条必须自己在日志里出声
		log.Printf("ERROR: 查用户 %d 的密钥失败，归属缓存未能失效（禁用不会立即影响其密钥）: %v", userID, err)
		return 0
	}
	for _, p := range plains {
		DefaultCache().Del(apiKeyCacheKey(p))
	}
	return len(plains)
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
