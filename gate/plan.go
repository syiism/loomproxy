package gate

// 限额与套餐解析：有效额度 = 套餐限额 + 用户覆盖（追加语义），优先级
// 用户数据源级覆盖 > 套餐限额 > 不限。本文件只做「读库算限额」，不挂中间件。

import (
	"log"
	"time"

	"loomproxy/db"
	"loomproxy/models"
)

// ResolvePlan 返回用户生效的套餐：优先用户绑定套餐，未绑定则使用免费版（code=free）
func ResolvePlan(user *models.User) models.QuotaPlan {
	if user.PlanID != nil && user.Plan != nil {
		// 套餐到期惰性回退 free（不回写数据库；PlanExpireAt 为空=永久，兼容存量）
		if user.PlanExpireAt == nil || time.Now().Before(*user.PlanExpireAt) {
			return *user.Plan
		}
	}
	var plan models.QuotaPlan
	if err := db.DB.Where("code = ?", "free").First(&plan).Error; err == nil {
		return plan
	}
	return models.QuotaPlan{}
}

// ResolvePlanForUser 导出供外部使用
func ResolvePlanForUser(user *models.User) models.QuotaPlan {
	return ResolvePlan(user)
}

// PlanSourceLimits 套餐 source 维度的额度覆盖表（仅 limit >= 0 生效）
func PlanSourceLimits(planID uint) map[string]int64 {
	m := make(map[string]int64)
	if planID == 0 {
		return m
	}
	var limits []models.QuotaLimit
	if err := db.DB.Where("plan_id = ? AND scope = ?", planID, "source").Find(&limits).Error; err != nil {
		log.Printf("ERROR: PlanSourceLimits query failed: %v", err)
		return m
	}
	for _, l := range limits {
		m[l.Target] = l.Limit
	}
	return m
}

// userOverrideLimit 查询用户级覆盖额度（code 为数据源码）。
// ok=false 表示无覆盖或覆盖值为 0（回退套餐）；limit<0 表示不限。
func userOverrideLimit(userID uint, code string) (int64, bool) {
	var override models.UserQuotaOverride
	if err := db.DB.Where("user_id = ? AND group_code = ?", userID, code).First(&override).Error; err != nil {
		return 0, false
	}
	if override.Limit == 0 {
		return 0, false
	}
	return override.Limit, true
}

// EffectiveSourceLimit 用户在某数据源的有效日额度。**只管额度轴**：
// 有效额度 = 套餐限额 + 用户覆盖（覆盖为空视为 0，正值追加、负值扣减，结果下限 0）：
// 优先用户级数据源覆盖，无覆盖时按套餐限额；套餐未限额则为 -1（不限，覆盖不再生效）。
// 注意「没有这一行」在 P34 之后还多了一层意思——**该套餐没这个源的权限**，
// 那条由 access(order 400) 判，不在这里；能走到这里的请求都已经有权限了。
func EffectiveSourceLimit(user *models.User, sourceName string, planLimits map[string]int64) int64 {
	if user != nil && user.ID > 0 {
		// 1) 用户级数据源覆盖：单数据源套餐限额 + 覆盖
		if l, ok := userOverrideLimit(user.ID, sourceName); ok {
			base, hasPlan := planLimits[sourceName]
			if !hasPlan || base < 0 {
				return -1 // 套餐不限（或未限额），覆盖不生效
			}
			return clampLimit(base + l)
		}
	}
	// 2) 套餐限额
	if l, ok := planLimits[sourceName]; ok {
		return l
	}
	return -1 // 套餐没限额行：额度轴是不限（有没有权限由 access 轴判，见上方注释）
}

// clampLimit 额度下限为 0（覆盖扣减超过套餐限额时视为 0，即当日不可用）
func clampLimit(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}
