package db

import (
	"sync"
	"time"

	"loomproxy/models"
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
// blockedIPCacheLookup 一次读锁里同时问两件事（缓存在不在有效期、这个 IP 在不在表里）。
// 原来这个临界区是 `RLock() … RUnlock()` 的尾解锁，而且**中间有一支 `return`**——
// 那种形状只要中间 panic，读锁就被永久持有，此后每个请求的 IP 检查都卡在这把锁上（待办清单 P93）。
// 合并成一次加锁也保住了原来的语义：缓存有效期内**不**回库刷新（未命中就直接返回 false）。
func blockedIPCacheLookup(ip string) (fresh bool, hit bool) {
	blockedIPCache.mu.RLock()
	defer blockedIPCache.mu.RUnlock()
	fresh = time.Now().Before(blockedIPCache.exp)
	_, hit = blockedIPCache.ips[ip]
	return fresh, hit
}

func IsIPBlocked(ip string) bool {
	if fresh, hit := blockedIPCacheLookup(ip); fresh {
		return hit
	}

	refreshBlockedIPCache()

	_, hit := blockedIPCacheLookup(ip)
	return hit
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
	defer blockedIPCache.mu.Unlock()
	blockedIPCache.exp = time.Time{}
}
