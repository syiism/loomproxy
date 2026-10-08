package app

// 正文类响应的跨进程缓存适配器：接到接口缓存那一层（Redis 可用则跨重启，否则纯内存）。
//
// 为什么落在 app：AGENTS §4 那条硬边界——`base` 不得导入 `utils`。
// 于是 base 只声明端口（`base.SharedCache`），"接哪一层"这个决定放在同时看得见两边的装配层。
//
// 两个实现细节不是风格问题：
//   - 值存成 `string` 而不是 `[]byte`：Redis 那一段走 `json.Marshal`，`[]byte` 会被编成
//     base64 字符串、读回来是 `string`——**存取类型不一致会让命中永远失效**，
//     症状恰好是"缓存好像没在工作"。这里统一按 string 存，两种类型都认。
//   - 用 `SetTTL` 而不是 `Set`：正文的存活时间由调用侧决定（上游改了这章要多快让读者看到，
//     是产品口径不是全局常数），不能拿 `CACHE_TTL` 当默认。
//
// 与计费/列表缓存共用 `DefaultCache()` 的取舍：内存模式下那条 LRU 只有 `CACHE_MAXSIZE` 格，
// 正文条目会挤掉它们、反之亦然（Redis 模式按 TTL 各自过期，无此问题）。
// 现网是 Redis 模式；接受这个耦合，是为了不再多开一条连接与一份配置（要拆开就是另一个决定）。

import (
	"time"

	"loomproxy/base"
	"loomproxy/utils"
)

type sharedContentCache struct{}

func (sharedContentCache) Get(key string) ([]byte, bool) {
	v, ok := utils.DefaultCache().Get(key)
	if !ok {
		return nil, false
	}
	switch s := v.(type) {
	case string:
		if s == "" {
			return nil, false
		}
		return []byte(s), true
	case []byte:
		if len(s) == 0 {
			return nil, false
		}
		return s, true
	}
	return nil, false
}

func (sharedContentCache) Set(key string, val []byte, ttl time.Duration) {
	if len(val) == 0 || ttl <= 0 {
		return // 空响应与零 TTL 都不该占一格（前者是"没有"，后者是调用方明确不缓存）
	}
	utils.DefaultCache().SetTTL(key, string(val), ttl)
}

var _ base.SharedCache = sharedContentCache{}
