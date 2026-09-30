package db

import (
	"sync"
	"time"

	"loomproxy-go/models"
)

// IP 黑名单读取缓存：避免全局中间件每请求查库。
// 管理后台增删后通过 InvalidateBlockedIPCache 立即生效，否则最长 10s 生效。
var blockedIPCache struct {
	mu  sync.RWMutex
	ips map[string]struct{}
	exp time.Time
}

const blockedIPCacheTTL = 10 * time.Second

// IsIPBlocked 判断 IP 是否在黑名单中（带 10s 内存缓存；DB 未初始化时视为未拉黑）
func IsIPBlocked(ip string) bool {
	blockedIPCache.mu.RLock()
	if time.Now().Before(blockedIPCache.exp) {
		_, ok := blockedIPCache.ips[ip]
		blockedIPCache.mu.RUnlock()
		return ok
	}
	blockedIPCache.mu.RUnlock()

	refreshBlockedIPCache()

	blockedIPCache.mu.RLock()
	_, ok := blockedIPCache.ips[ip]
	blockedIPCache.mu.RUnlock()
	return ok
}

func refreshBlockedIPCache() {
	blockedIPCache.mu.Lock()
	defer blockedIPCache.mu.Unlock()
	// double-check：并发下可能已被其他 goroutine 刷新
	if time.Now().Before(blockedIPCache.exp) {
		return
	}
	ips := make(map[string]struct{})
	if DB != nil {
		var list []models.BlockedIP
		if err := DB.Find(&list).Error; err == nil {
			for _, b := range list {
				ips[b.IP] = struct{}{}
			}
		}
	}
	blockedIPCache.ips = ips
	blockedIPCache.exp = time.Now().Add(blockedIPCacheTTL)
}

// InvalidateBlockedIPCache 使黑名单缓存失效（管理后台增删后调用，立即生效）
func InvalidateBlockedIPCache() {
	blockedIPCache.mu.Lock()
	blockedIPCache.exp = time.Time{}
	blockedIPCache.mu.Unlock()
}
