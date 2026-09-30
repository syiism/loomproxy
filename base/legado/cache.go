package legado

import (
	"time"

	"loomproxy-go/conf"
)

// upstreamCacheTTL 读取全局配置的上游响应缓存时长（UPSTREAM_CACHE_TTL，秒）。
// search/detail/chapter/explore 等幂等基础处理器使用；content 不缓存。
func upstreamCacheTTL() time.Duration {
	return time.Duration(conf.Config.UpstreamCacheTTL * float64(time.Second))
}
