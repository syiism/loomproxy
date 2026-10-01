package db

import (
	"sync"
	"time"

	"loomproxy/models"
)

// 系统设置读取缓存：避免热路径（如 base.Fetch 的按数据源代理判定）每请求查库。
// 管理后台修改设置后最长 10s 生效，可接受。
type settingCacheEntry struct {
	value string
	exp   time.Time
}

var settingCache sync.Map // key -> settingCacheEntry

const settingCacheTTL = 10 * time.Second

// GetSetting 读取系统设置（带 10s 内存缓存，含空值负缓存）；不存在或出错返回 ""
func GetSetting(key string) string {
	if v, ok := settingCache.Load(key); ok {
		if e := v.(settingCacheEntry); time.Now().Before(e.exp) {
			return e.value
		}
	}
	value := ""
	if DB != nil {
		var s models.SystemSetting
		// 条件用 map 形式让 GORM 按方言加引号：`key` 在 MySQL 是保留字，而反引号写死会让
		// PostgreSQL 直接语法报错、错误被吞掉后所有设置都读成空串（见待办清单 P1）
		if err := DB.Where(map[string]interface{}{"key": key}).First(&s).Error; err == nil {
			value = s.Value
		}
	}
	settingCache.Store(key, settingCacheEntry{value: value, exp: time.Now().Add(settingCacheTTL)})
	return value
}

// InvalidateSettingCache 使设置缓存失效：管理后台修改后调用，立即生效
// （不再等 10s TTL）；key 为空时清空全部
func InvalidateSettingCache(key string) {
	if key == "" {
		settingCache.Range(func(k, _ interface{}) bool {
			settingCache.Delete(k)
			return true
		})
		return
	}
	settingCache.Delete(key)
}
