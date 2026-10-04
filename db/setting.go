package db

import (
	"log"
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

// settingNotices 记录每个设置键上一次出声时的样子（原因 + 填的值），同一种替换只说一次。
// 键的个数就是设置项里被这么读的键的个数，不回收。
var settingNotices sync.Map // key -> string

// NoticeReplacedSetting 在"设置里填的值没生效、系统按兜底默认走"时说一次话。
//
// 合法域的判断留在调用方（各键对取值域的理解不同），这里只管"要出声"这一件事。
// 去重（同 P36 号池 `ErrCapacityReached` 的判据）是必需的：读设置在热路径上，
// 每请求一条日志会把真正该看的读数泡坏。
func NoticeReplacedSetting(key, raw string, effective int, reason string) {
	sig := reason + "\x00" + raw
	if v, ok := settingNotices.Load(key); ok && v.(string) == sig {
		return
	}
	settingNotices.Store(key, sig)
	log.Printf("ERROR: 设置 %s 填的值没生效（%s）：填的是 %q，实际按 %d 走", key, reason, raw, effective)
}
