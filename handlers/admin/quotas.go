package admin

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"loomproxy/db"
	"loomproxy/gate"
	"loomproxy/handlers/auth"
	"loomproxy/models"
)

var quotaScopes = map[string]bool{
	"global": true,
	"source": true,
}

var quotaPeriods = map[string]bool{
	"day":   true,
	"week":  true,
	"month": true,
}

type createQuotaPlanRequest struct {
	Code        string `json:"code" binding:"required"`
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
	Status      *int   `json:"status"`
	Level       *int   `json:"level"`   // 套餐等级：0=免费，数值越大等级越高，用于卡密兑换升级/降级判定
	RoleID      *uint  `json:"role_id"` // 绑定角色：用户套餐变更时自动切换为该角色；空/0=不绑定
}

// resolvePlanRole 校验角色绑定请求：返回要写入的 role_id（nil=不绑定/解除绑定）。
// role_id 为 0 或未传表示不绑定；update 时同样以 0 表示解除绑定。
func resolvePlanRole(roleID *uint) (*uint, string) {
	if roleID == nil || *roleID == 0 {
		return nil, ""
	}
	var role models.Role
	if err := db.DB.First(&role, *roleID).Error; err != nil {
		return nil, "绑定的角色不存在"
	}
	if role.Status != 1 {
		return nil, "绑定的角色已禁用"
	}
	id := *roleID
	return &id, ""
}

// CreateQuotaPlan 创建额度套餐
func CreateQuotaPlan(c *gin.Context) {
	var req createQuotaPlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		auth.Fail(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}

	var count int64
	if err := db.DB.Model(&models.QuotaPlan{}).Where("code = ? OR name = ?", req.Code, req.Name).Count(&count).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}
	if count > 0 {
		auth.Fail(c, http.StatusConflict, "套餐标识或名称已存在")
		return
	}

	status := 1
	if req.Status != nil {
		status = *req.Status
	}
	level := 0
	if req.Level != nil {
		if *req.Level < 0 {
			auth.Fail(c, http.StatusBadRequest, "等级不能为负")
			return
		}
		level = *req.Level
	}
	roleID, roleErr := resolvePlanRole(req.RoleID)
	if roleErr != "" {
		auth.Fail(c, http.StatusBadRequest, roleErr)
		return
	}
	plan := models.QuotaPlan{
		Code:        req.Code,
		Name:        req.Name,
		Description: req.Description,
		Level:       level,
		RoleID:      roleID,
		Status:      status,
	}
	if err := db.DB.Create(&plan).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "创建套餐失败")
		return
	}
	auth.Ok(c, plan)
}

type updateQuotaPlanRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Status      *int    `json:"status"`
	Level       *int    `json:"level"`
	RoleID      *uint   `json:"role_id"` // 0=解除绑定，>0=绑定该角色，null=不更新
}

// UpdateQuotaPlan 更新额度套餐
func UpdateQuotaPlan(c *gin.Context) {
	id := c.Param("id")
	var req updateQuotaPlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		auth.Fail(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}

	var plan models.QuotaPlan
	if err := db.DB.First(&plan, id).Error; err != nil {
		auth.Fail(c, http.StatusNotFound, "套餐不存在")
		return
	}

	updates := map[string]interface{}{}
	if req.Name != nil {
		updates["name"] = *req.Name
	}
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	if req.Status != nil {
		updates["status"] = *req.Status
	}
	if req.Level != nil {
		if *req.Level < 0 {
			auth.Fail(c, http.StatusBadRequest, "等级不能为负")
			return
		}
		updates["level"] = *req.Level
	}
	if req.RoleID != nil {
		roleID, roleErr := resolvePlanRole(req.RoleID)
		if roleErr != "" {
			auth.Fail(c, http.StatusBadRequest, roleErr)
			return
		}
		if roleID == nil {
			updates["role_id"] = nil
		} else {
			updates["role_id"] = *roleID
		}
	}
	if len(updates) == 0 {
		auth.Fail(c, http.StatusBadRequest, "无更新字段")
		return
	}

	if err := db.DB.Model(&plan).Updates(updates).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "更新失败")
		return
	}
	if err := db.DB.First(&plan, plan.ID).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}
	auth.Ok(c, plan)
}

// DeleteQuotaPlan 删除额度套餐（软删除，同时硬删其额度限制）
func DeleteQuotaPlan(c *gin.Context) {
	id := c.Param("id")

	var plan models.QuotaPlan
	if err := db.DB.First(&plan, id).Error; err != nil {
		auth.Fail(c, http.StatusNotFound, "套餐不存在")
		return
	}

	var userCount, quotaCount int64
	if err := db.DB.Model(&models.User{}).Where("plan_id = ?", plan.ID).Count(&userCount).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}
	if err := db.DB.Model(&models.UserQuota{}).Where("plan_id = ?", plan.ID).Count(&quotaCount).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}
	if userCount > 0 || quotaCount > 0 {
		auth.Fail(c, http.StatusBadRequest, "套餐仍被用户引用")
		return
	}

	// 事务：先删限制再删套餐，确保原子性
	if err := db.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("plan_id = ?", plan.ID).Delete(&models.QuotaLimit{}).Error; err != nil {
			return err
		}
		return tx.Delete(&plan).Error
	}); err != nil {
		auth.Fail(c, http.StatusInternalServerError, "删除失败")
		return
	}
	auth.Ok(c, gin.H{"message": "已删除"})
}

type createQuotaLimitRequest struct {
	PlanID uint   `json:"plan_id" binding:"required"`
	Scope  string `json:"scope" binding:"required"`
	Target string `json:"target" binding:"required"`
	Limit  *int64 `json:"limit"`
	Period string `json:"period"`
}

// CreateQuotaLimit 创建额度限制（limit 默认 0，-1 表示无限）
func CreateQuotaLimit(c *gin.Context) {
	var req createQuotaLimitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		auth.Fail(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}
	if !quotaScopes[req.Scope] {
		auth.Fail(c, http.StatusBadRequest, "scope 必须是 global/source 之一")
		return
	}

	period := req.Period
	if period == "" {
		period = "month"
	}
	if !quotaPeriods[period] {
		auth.Fail(c, http.StatusBadRequest, "period 必须是 day/week/month 之一")
		return
	}

	var plan models.QuotaPlan
	if err := db.DB.First(&plan, req.PlanID).Error; err != nil {
		auth.Fail(c, http.StatusBadRequest, "套餐不存在")
		return
	}

	var count int64
	if err := db.DB.Model(&models.QuotaLimit{}).
		Where("plan_id = ? AND scope = ? AND target = ?", req.PlanID, req.Scope, req.Target).
		Count(&count).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}
	if count > 0 {
		auth.Fail(c, http.StatusConflict, "相同套餐与目标的限制已存在")
		return
	}

	limit := int64(0)
	if req.Limit != nil {
		limit = *req.Limit
	}
	quotaLimit := models.QuotaLimit{
		PlanID: req.PlanID,
		Scope:  req.Scope,
		Target: req.Target,
		Limit:  limit,
		Period: period,
	}
	if err := db.DB.Create(&quotaLimit).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "创建额度限制失败")
		return
	}
	auth.Ok(c, quotaLimit)
}

type updateQuotaLimitRequest struct {
	Limit  *int64  `json:"limit"`
	Period *string `json:"period"`
}

// UpdateQuotaLimit 更新额度限制
func UpdateQuotaLimit(c *gin.Context) {
	id := c.Param("id")
	var req updateQuotaLimitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		auth.Fail(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}

	var quotaLimit models.QuotaLimit
	if err := db.DB.First(&quotaLimit, id).Error; err != nil {
		auth.Fail(c, http.StatusNotFound, "额度限制不存在")
		return
	}

	updates := map[string]interface{}{}
	if req.Limit != nil {
		updates["limit"] = *req.Limit
	}
	if req.Period != nil {
		if !quotaPeriods[*req.Period] {
			auth.Fail(c, http.StatusBadRequest, "period 必须是 day/week/month 之一")
			return
		}
		updates["period"] = *req.Period
	}
	if len(updates) == 0 {
		auth.Fail(c, http.StatusBadRequest, "无更新字段")
		return
	}

	if err := db.DB.Model(&quotaLimit).Updates(updates).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "更新失败")
		return
	}
	if err := db.DB.First(&quotaLimit, quotaLimit.ID).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}
	auth.Ok(c, quotaLimit)
}

// DeleteQuotaLimit 删除额度限制
func DeleteQuotaLimit(c *gin.Context) {
	id := c.Param("id")

	var quotaLimit models.QuotaLimit
	if err := db.DB.First(&quotaLimit, id).Error; err != nil {
		auth.Fail(c, http.StatusNotFound, "额度限制不存在")
		return
	}
	if err := db.DB.Delete(&quotaLimit).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "删除失败")
		return
	}
	auth.Ok(c, gin.H{"message": "已删除"})
}

type QuotaOverrideItem struct {
	SourceCode string `json:"source_code"` // 数据源标识
	SourceName string `json:"source_name"` // 显示名称
	Category   string `json:"category"`    // 所属组，如 fq
	PlanLimit  *int64 `json:"plan_limit"`  // 套餐限额（-1=不限，nil=未配置）
	Used       int64  `json:"used"`        // 当日该用户在此数据源已消耗
	UserLimit  *int64 `json:"user_limit"`  // 用户覆盖（调整量，可加可减）
	Effective  int64  `json:"effective"`   // 生效额度 = 计划 + 覆盖（-1=不限）
}

// GetUserQuota 返回用户在各数据源的额度详情（含覆盖）。
func GetUserQuota(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		auth.Fail(c, http.StatusBadRequest, "无效的用户 ID")
		return
	}
	var user models.User
	if err := db.DB.Preload("Plan").First(&user, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			auth.Fail(c, http.StatusNotFound, "用户不存在")
			return
		}
		auth.Fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}

	// 覆盖值 0 视为无效（不回显）
	var overrides []models.UserQuotaOverride
	db.DB.Where("user_id = ?", user.ID).Find(&overrides)
	overrideMap := make(map[string]int64)
	for _, o := range overrides {
		if o.Limit != 0 {
			overrideMap[o.GroupCode] = o.Limit
		}
	}

	// 用户绑定的套餐，未绑定时回退免费版
	planID := user.PlanID
	planName := ""
	planCode := ""
	if user.Plan != nil {
		planName = user.Plan.Name
		planCode = user.Plan.Code
	} else {
		var freePlan models.QuotaPlan
		if err := db.DB.Where("code = ?", "free").First(&freePlan).Error; err == nil {
			planID = &freePlan.ID
			planName = freePlan.Name
			planCode = freePlan.Code
		}
	}
	planLimits := make(map[string]int64)
	if planID != nil {
		var limits []models.QuotaLimit
		db.DB.Where("plan_id = ? AND scope = ?", *planID, "source").Find(&limits)
		for _, l := range limits {
			planLimits[l.Target] = l.Limit
		}
	}

	var sources []models.DataSource
	db.DB.Order("category ASC, sort_order ASC, id ASC").Find(&sources)

	items := make([]QuotaOverrideItem, 0, len(sources))
	for _, ds := range sources {
		item := QuotaOverrideItem{
			SourceCode: ds.Name,
			SourceName: ds.DisplayName,
			Category:   ds.Category,
		}
		if l, ok := planLimits[ds.Name]; ok {
			v := l
			item.PlanLimit = &v
		}
		if l, ok := overrideMap[ds.Name]; ok {
			v := l
			item.UserLimit = &v
		}
		item.Used = gate.UsedToday(user.ID, ds.Name)
		// 生效额度 = 计划额度 + 用户覆盖（覆盖为空视为 0，可加可减，下限 0；
		// 计划未配置或为负（不限）时生效额度为不限）
		if item.PlanLimit != nil && *item.PlanLimit >= 0 {
			var adj int64
			if item.UserLimit != nil {
				adj = *item.UserLimit
			}
			eff := *item.PlanLimit + adj
			if eff < 0 {
				eff = 0
			}
			item.Effective = eff
		} else {
			item.Effective = -1
		}
		items = append(items, item)
	}
	if items == nil {
		items = []QuotaOverrideItem{}
	}

	auth.Ok(c, gin.H{
		"user_id":   user.ID,
		"username":  user.Username,
		"plan_name": planName,
		"plan_code": planCode,
		"items":     items,
	})
}

type updateUserQuotaRequest struct {
	Overrides []updateQuotaOverride `json:"overrides" binding:"required"`
}

type updateQuotaOverride struct {
	GroupCode string `json:"group_code" binding:"required"`
	Limit     int64  `json:"limit"`
}

// UpdateUserQuota 设置用户在某数据源的额度覆盖。
func UpdateUserQuota(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		auth.Fail(c, http.StatusBadRequest, "无效的用户 ID")
		return
	}
	var user models.User
	if err := db.DB.First(&user, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			auth.Fail(c, http.StatusNotFound, "用户不存在")
			return
		}
		auth.Fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}

	var req updateUserQuotaRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		auth.Fail(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}

	for _, o := range req.Overrides {
		if o.Limit == 0 {
			db.DB.Where("user_id = ? AND group_code = ?", user.ID, o.GroupCode).Delete(&models.UserQuotaOverride{})
		} else {
			db.DB.Where("user_id = ? AND group_code = ?", user.ID, o.GroupCode).
				Assign(models.UserQuotaOverride{UserID: user.ID, GroupCode: o.GroupCode, Limit: o.Limit}).
				FirstOrCreate(&models.UserQuotaOverride{})
		}
	}
	auth.Ok(c, gin.H{"message": "额度覆盖已更新"})
}
