package auth

// 多设备登录监控（待办清单 P44）：VIP 借号与滥发密钥的可见性与自动处置。
//
// 三条边界写在这里，别到实现完之后才发现语义没定：
//  1. **处置只看会话数，绝不看 IP 数**。一个出口 IP 后面可能是整家公司或一整个家庭，
//     按 IP 踢人必然大面积误伤；IP 数在面板上只用来标红。
//  2. **默认关**（`device_watch_enabled=false`）。读数先上线、踢人后上线——
//     没人看过自己系统的分布就去限制别人登录，是拿现网用户试错。
//  3. **它只是提高借号成本，不是阻断**。被移出的设备重新登录就能回来（重新登录不受限，
//     本轮没选「拦新登录」）。别在文档或提示里把它写成「已阻止共享账号」。
//
// 密钥侧只统计数量与账号维度的不同 IP 数，不做冻结：`api_keys` 没有来源 IP/设备列，
// 定位不到是哪把 key 异常，"定位不到就冻" 等于整账号冻。

import (
	"log"
	"strconv"
	"time"

	"loomproxy/db"
)

// 监控相关的设置键（面板「系统设置」可改，`db.GetSetting` 有 10 秒内存缓存）
const (
	settingDeviceWatchEnabled = "device_watch_enabled"
	settingMaxActiveSessions  = "max_active_sessions"
	settingWatchWindowDays    = "device_watch_window_days"
	settingSuspectDistinctIPs = "suspect_distinct_ips"
)

const (
	defaultMaxActiveSessions  = 5
	defaultWatchWindowDays    = 7
	defaultSuspectDistinctIPs = 8
)

// deviceWatchEnabled 自动处置的开关。判定用字符串比较而不是布尔解析：
// 与 `auto_block_enabled` 同一条形状（设置项存的是文本，面板写 "true"/"false"）
func deviceWatchEnabled() bool {
	// 判据走 `db.SettingBool`（一处定义）：这里曾经是精确 `== "true"`，
	// 而 `register_enabled` 那一路去空白又转小写——同一个值在两个开关上结果相反
	return db.SettingBool(settingDeviceWatchEnabled, false)
}

// MaxActiveSessions 同时允许的活跃会话数（导出给管理端视图复用，两处判据必须同一个数）
//
// 上限类设置的读取一律走 `db.SettingInt`：本文件原来有一份自己的（P48 给它加了出声），
// 而 verify 与 ipblock 各有一份静默的——同一个填错的值在三个开关上是三种悄悄替换（待办清单 P61）。
func MaxActiveSessions() int {
	return db.SettingInt(settingMaxActiveSessions, defaultMaxActiveSessions)
}

// WatchWindowStart 设备/密钥读数的统计窗口起点
func WatchWindowStart() time.Time {
	days := db.SettingInt(settingWatchWindowDays, defaultWatchWindowDays)
	return time.Now().AddDate(0, 0, -days)
}

// SuspectDistinctIPs 窗口内不同 IP 达到这个数就标红（只标红，不处置）
func SuspectDistinctIPs() int {
	return db.SettingInt(settingSuspectDistinctIPs, defaultSuspectDistinctIPs)
}

// pruneExcessSessions 登录之后收一次口子：保留最近活跃的 N 个（含刚签发的这个），其余移出。
//
// 日志那行是这条唯一能分清「系统踢的」还是「用户自己点的登出其他设备」的地方——
// 两种都只体现为 `revoked_at` 被写上一个时刻，库里分不出来。
func pruneExcessSessions(userID uint, keepSessionID string) int64 {
	if !deviceWatchEnabled() {
		return 0
	}
	keep := MaxActiveSessions()
	n := db.RevokeExcessSessions(userID, keepSessionID, keep)
	if n > 0 {
		log.Printf("SECURITY: 多设备监控自动移出会话 user_id=%d 移出=%d 保留=%d（判据=活跃会话数超上限；IP 数不参与处置）",
			userID, n, keep)
	}
	return n
}

// deviceLoginNotice 登录响应里那句移出提示。
//
// 用的是**上一次**登录时刻（这次登录之后发生的移出不该在这次提示里报，那已经是下一次的事了），
// 所以调用方必须在 `last_login_at` 被覆盖之前把旧值取出来——顺序写反就是「提示永远为空」。
// 不建处置日志表、也不建站内信：`revoked_at` 加一个「上次登录时间」就够算出这句话。
//
// 文案不写成「多设备监控踢了你几台」：改密、点「登出其他设备」同样会产生这些记录，
// 归错因比不归因更糟。
func deviceLoginNotice(userID uint, prevLoginAt *time.Time) string {
	if prevLoginAt == nil {
		return ""
	}
	n := db.RevokedSince(userID, *prevLoginAt)
	if n <= 0 {
		return ""
	}
	return "自上次登录以来，该账号有 " + strconv.FormatInt(n, 10) +
		" 台设备被移出登录（改密、登出其他设备或多设备监控都会如此）。如非本人操作，请尽快修改密码。"
}
