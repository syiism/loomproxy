package gate

// 用量统计：口径一律「当日」= 平台时区零点起（`TZ_OFFSET_HOURS`，默认 8），
// **但管理员刷新过额度的用户从刷新时刻起算**
// （起算点只有 UsageSince 一处定义）；数据取自 quota_usage_logs 流水表（group_code 列存数据源码）。

import (
	"log"
	"time"

	"loomproxy/db"
	"loomproxy/models"
	"loomproxy/utils"
)

// StartOfDay 当日零点——**只是转调 `utils.DayStart(1)`**：时区与日界的算法都在那一处，
// 这里保留是因为它是额度口径的门面（`UsageSince` 用它，判定与读数都用它）。
func StartOfDay() time.Time {
	return utils.DayStart(1)
}

// UsageSince 这名用户的「当日用量」从哪一刻起算——**唯一的定义处**。
//
// 取「零点」与「管理员刷新时刻」里较晚的那个：刷新（`users.quota_reset_at`）之后消耗重新计，
// 而过了零点它就自然失效（新的一天本来就从零开始，不需要谁来把水印抹掉）。
// 判定（billing 的 429）与读数（dashboard、管理面额度弹窗）都走这里——
// 起算点要是长在两处，刷新后一处归零、另一处还挂着旧值，面板就成了「说不清哪边是真的地方」。
func UsageSince(user *models.User) time.Time {
	start := StartOfDay()
	if user == nil || user.QuotaResetAt == nil {
		return start
	}
	if user.QuotaResetAt.After(start) {
		return *user.QuotaResetAt
	}
	return start
}

// UsedToday 该用户当日（起算点见 UsageSince）在某数据源上已消耗的额度。
// 参数收 *User 而不是 userID 是刻意的：多传一个用户就必然带上起算点，
// 少传就编译不过——防止某个调用方悄悄回到「只认零点」的旧口径。
func UsedToday(user *models.User, sourceCode string) int64 {
	if user == nil {
		return 0
	}
	var used int64
	if err := db.DB.Model(&models.QuotaUsageLog{}).
		Where("user_id = ? AND group_code = ? AND created_at >= ?", user.ID, sourceCode, UsageSince(user)).
		Select("COALESCE(SUM(cost), 0)").Scan(&used).Error; err != nil {
		log.Printf("ERROR: usedToday query failed: %v", err)
	}
	return used
}

// UsedTodayAllSources 该用户当日（起算点同上）在**全部数据源**上合计消耗的额度。
// 只给「刷新额度」的日志与响应读数用：刷新要告诉管理员「这次抹掉了多少已用量」，
// 判定不看它（判定逐源比 limit）。
func UsedTodayAllSources(user *models.User) int64 {
	if user == nil {
		return 0
	}
	var used int64
	if err := db.DB.Model(&models.QuotaUsageLog{}).
		Where("user_id = ? AND created_at >= ?", user.ID, UsageSince(user)).
		Select("COALESCE(SUM(cost), 0)").Scan(&used).Error; err != nil {
		log.Printf("ERROR: UsedTodayAllSources query failed: %v", err)
	}
	return used
}

// UsedTodayAll 当日全站某数据源已消耗的额度（管理员视角）
func UsedTodayAll(sourceCode string) int64 {
	var used int64
	if err := db.DB.Model(&models.QuotaUsageLog{}).
		Where("group_code = ? AND created_at >= ?", sourceCode, StartOfDay()).
		Select("COALESCE(SUM(cost), 0)").Scan(&used).Error; err != nil {
		log.Printf("ERROR: UsedTodayAll query failed: %v", err)
	}
	return used
}

// ActiveUsersToday 当日有消耗的去重用户数；sourceCode 为空时统计全部数据源
func ActiveUsersToday(sourceCode string) int64 {
	var n int64
	q := db.DB.Model(&models.QuotaUsageLog{}).Where("created_at >= ?", StartOfDay())
	if sourceCode != "" {
		q = q.Where("group_code = ?", sourceCode)
	}
	if err := q.Distinct("user_id").Count(&n).Error; err != nil {
		log.Printf("ERROR: ActiveUsersToday query failed: %v", err)
	}
	return n
}

// callsToday 当日调用次数（流水行数）；sourceCode 为空时统计全部数据源
func callsToday(sourceCode string) int64 {
	var n int64
	q := db.DB.Model(&models.QuotaUsageLog{}).Where("created_at >= ?", StartOfDay())
	if sourceCode != "" {
		q = q.Where("group_code = ?", sourceCode)
	}
	if err := q.Count(&n).Error; err != nil {
		log.Printf("ERROR: callsToday query failed: %v", err)
	}
	return n
}
