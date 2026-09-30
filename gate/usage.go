package gate

// 用量统计：口径一律「当日」= 北京时间零点起（与面板的重置时间一致），
// 数据取自 quota_usage_logs 流水表（group_code 列存数据源码）。

import (
	"log"
	"time"

	"loomproxy/db"
	"loomproxy/models"
)

// StartOfDay 北京时区当日零点（与 dashboard 的重置时间口径一致）
func StartOfDay() time.Time {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Now().In(loc)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
}

// UsedToday 当日某用户在某数据源上已消耗的额度
func UsedToday(userID uint, sourceCode string) int64 {
	var used int64
	if err := db.DB.Model(&models.QuotaUsageLog{}).
		Where("user_id = ? AND group_code = ? AND created_at >= ?", userID, sourceCode, StartOfDay()).
		Select("COALESCE(SUM(cost), 0)").Scan(&used).Error; err != nil {
		log.Printf("ERROR: usedToday query failed: %v", err)
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
