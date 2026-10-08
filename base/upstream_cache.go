package base

import (
	"sync"
	"time"

	"loomproxy/conf"
)

// 上游响应短 TTL 缓存（纯内存，自包含实现，避免 base → utils → legado → base 的循环依赖）。
// 用于 detail/chapter 等幂等接口，短期内重复请求直接命中，降低上游 QPS、缓解限流。
// 由 conf 的 UPSTREAM_CACHE_TTL / UPSTREAM_CACHE_MAXSIZE 控制，TTL<=0 视为禁用。

type upstreamCacheEntry struct {
	value   interface{}
	expires time.Time
}

var (
	upstreamCacheMu    sync.Mutex
	upstreamCacheItems = make(map[string]upstreamCacheEntry)
)

// getUpstreamCache 读取未过期的缓存项
func getUpstreamCache(key string) (interface{}, bool) {
	upstreamCacheMu.Lock()
	defer upstreamCacheMu.Unlock()
	entry, ok := upstreamCacheItems[key]
	if !ok {
		return nil, false
	}
	// 过期项不在这里删除（保留供降级读取），统一由写入路径的清扫处理
	if time.Now().After(entry.expires) {
		return nil, false
	}
	return entry.value, true
}

// getUpstreamCacheStale 读取缓存项（含已过期），用于上游故障时的降级返回。
//
// maxStale 限制"过期多久以内"的旧值才允许降级（P112 出路②）：<=0 表示不限，
// 保持旧行为（无限期端出）。这一道闸是为了避免把几小时前的坏信封（HTTP 200 但
// body 是业务失败）在长期故障期间一直端给下游；刚写进去的坏值它挡不住（那条要等
// ResponseFilter 回灌到 GET 通路，见 P112 出路①）。
func getUpstreamCacheStale(key string, maxStale time.Duration) (interface{}, bool) {
	upstreamCacheMu.Lock()
	defer upstreamCacheMu.Unlock()
	entry, ok := upstreamCacheItems[key]
	if !ok {
		return nil, false
	}
	if maxStale > 0 && time.Since(entry.expires) > maxStale {
		return nil, false
	}
	return entry.value, true
}

// setUpstreamCache 写入缓存项；超容量时先清理过期项，仍超则淘汰最早过期的一项
func setUpstreamCache(key string, value interface{}, ttl time.Duration) {
	maxSize := 512
	if conf.Config != nil && conf.Config.UpstreamCacheMaxSize > 0 {
		maxSize = conf.Config.UpstreamCacheMaxSize
	}

	upstreamCacheMu.Lock()
	defer upstreamCacheMu.Unlock()

	if len(upstreamCacheItems) >= maxSize {
		now := time.Now()
		for k, e := range upstreamCacheItems {
			if now.After(e.expires) {
				delete(upstreamCacheItems, k)
			}
		}
	}
	if len(upstreamCacheItems) >= maxSize {
		var oldestKey string
		var oldestExp time.Time
		first := true
		for k, e := range upstreamCacheItems {
			if first || e.expires.Before(oldestExp) {
				oldestKey, oldestExp, first = k, e.expires, false
			}
		}
		delete(upstreamCacheItems, oldestKey)
	}

	upstreamCacheItems[key] = upstreamCacheEntry{
		value:   value,
		expires: time.Now().Add(ttl),
	}
}
