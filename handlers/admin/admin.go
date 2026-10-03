package admin

import (
	"errors"
	"log"
	"net/http"
	"net/mail"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"loomproxy/db"
	"loomproxy/gate"
	"loomproxy/handlers/auth"
	"loomproxy/handlers/catalog"
	"loomproxy/models"
)

func RegisterRoutes(r *gin.Engine) {
	g := r.Group("/admin", auth.AdminRequired())
	{
		g.GET("/stats", Stats)
		g.GET("/users", ListUsers)
		g.POST("/users", CreateUser)
		g.GET("/users/:id", GetUser)
		g.PATCH("/users/:id", UpdateUser)
		g.DELETE("/users/:id", DeleteUser)
		g.PUT("/users/:id/plan", UpdateUserPlan)
		g.POST("/users/:id/restore", RestoreUser)
		g.POST("/users/:id/roles", UpdateUserRoles)
		g.POST("/users/:id/reset-password", ResetUserPassword)
		g.GET("/roles", ListRoles)
		g.POST("/roles", CreateRole)
		g.PATCH("/roles/:id", UpdateRole)
		g.DELETE("/roles/:id", DeleteRole)
		g.GET("/settings", ListSettings)
		g.POST("/settings", CreateSetting)
		g.PUT("/settings/:key", UpdateSetting)
		g.DELETE("/settings/:key", DeleteSetting)
		g.GET("/quotas/plans", ListQuotaPlans)
		g.POST("/quotas/plans", CreateQuotaPlan)
		g.PATCH("/quotas/plans/:id", UpdateQuotaPlan)
		g.DELETE("/quotas/plans/:id", DeleteQuotaPlan)
		g.GET("/quotas/plans/:id/limits", ListQuotaLimits)
		g.POST("/quotas/limits", CreateQuotaLimit)
		g.PUT("/quotas/limits/:id", UpdateQuotaLimit)
		g.DELETE("/quotas/limits/:id", DeleteQuotaLimit)
		g.GET("/usage-logs", ListUsageLogs)
		g.GET("/monitor", GetMonitor)
		g.GET("/monitor/trend", GetMonitorTrend)
		g.GET("/monitor/subjects", GetMonitorSubjects)
		g.GET("/pools", ListPools)
		g.GET("/monitor/history", GetMonitorHistory)
		g.POST("/monitor/reset", ResetMonitor)
		g.POST("/monitor/backfill-subjects", BackfillSubjectNames)
		g.POST("/redeem-codes", CreateRedeemCodes)
		g.GET("/redeem-codes", ListRedeemCodes)
		g.POST("/redeem-codes/:id/revoke", RevokeRedeemCode)
		g.POST("/redeem-codes/revoke-selected", RevokeRedeemCodes)
		g.DELETE("/redeem-codes/batch/:batch_no", RevokeRedeemCodeBatch)
		g.GET("/quota-costs", ListQuotaCosts)
		g.PUT("/quota-costs/:id", UpdateQuotaCost)
		g.GET("/quota-costs/plans", ListQuotaPlanCosts)
		g.PUT("/quota-costs/plans/:id", UpdateQuotaPlanCost)
		g.POST("/quota-costs/plans/upsert", UpsertQuotaPlanCost)
		g.GET("/source-configs", ListPlatformSourceConfigs)
		g.PUT("/source-configs", UpdatePlatformSourceConfigs)
		g.GET("/users/:id/quota", GetUserQuota)
		g.PUT("/users/:id/quota", UpdateUserQuota)
		// IP 黑名单
		g.GET("/blocked-ips", ListBlockedIPs)
		g.POST("/blocked-ips", AddBlockedIP)
		g.DELETE("/blocked-ips/:id", RemoveBlockedIP)
		// 数据源管理
		g.GET("/data-sources", ListDataSources)
		g.POST("/data-sources", CreateDataSource)
		g.PATCH("/data-sources/:id", UpdateDataSource)
		g.DELETE("/data-sources/:id", DeleteDataSource)
		// 套餐-数据源关联
		g.GET("/quotas/plans/:id/data-sources", ListPlanDataSources)
		g.POST("/quotas/plans/:id/data-sources", AddPlanDataSource)
		g.POST("/quotas/plans/:id/data-sources/batch", BatchAddPlanDataSources)
		g.DELETE("/quotas/plans/:id/data-sources/:ds_id", RemovePlanDataSource)
		// 数据源分组（视图与批量操作单位，不参与任何键控，见 models.SourceGroup）
		g.GET("/source-groups", ListSourceGroups)
		g.POST("/source-groups", CreateSourceGroup)
		g.PATCH("/source-groups/:id", UpdateSourceGroup)
		g.DELETE("/source-groups/:id", DeleteSourceGroup)
		g.PUT("/source-groups/:id/members", UpdateSourceGroupMembers)
		g.POST("/source-groups/:id/apply-limits", ApplySourceGroupLimits)
	}
}

// Stats 仪表盘统计
func Stats(c *gin.Context) {
	var totalUsers, adminCount, vipCount int64
	var todayNew int64

	db.DB.Model(&models.User{}).Count(&totalUsers)
	db.DB.Model(&models.User{}).
		Joins("JOIN user_roles ON user_roles.user_id = users.id").
		Joins("JOIN roles ON roles.id = user_roles.role_id").
		Where("roles.code = ?", "admin").Count(&adminCount)
	db.DB.Model(&models.User{}).
		Joins("JOIN user_roles ON user_roles.user_id = users.id").
		Joins("JOIN roles ON roles.id = user_roles.role_id").
		Where("roles.code = ?", "vip").Count(&vipCount)

	loc := time.FixedZone("CST", 8*3600)
	now := time.Now().In(loc)
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	db.DB.Model(&models.User{}).Where("created_at >= ?", startOfDay).Count(&todayNew)

	skipped, keys, windowSec := gate.DedupeStats()
	auth.Ok(c, gin.H{
		"total_users":  totalUsers,
		"admin_count":  adminCount,
		"vip_count":    vipCount,
		"today_new":    todayNew,
		"normal_count": totalUsers - adminCount - vipCount,
		// 扣减冷却的自省读数（待办清单 P25）：没有这行，「冷却到底挡没挡」只能靠人肉比流水。
		"billing_dedupe": gin.H{
			"skipped":    skipped,
			"tracked":    keys,
			"window_sec": windowSec,
			"enabled":    windowSec > 0,
		},
	})
}

// ListUsers 用户列表（分页 + 搜索；with_deleted=1 时包含软删除用户，响应带 deleted_at）
func ListUsers(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	keyword := strings.TrimSpace(c.Query("keyword"))
	withDeleted := c.Query("with_deleted") == "1" || c.Query("with_deleted") == "true"
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	q := db.DB.Model(&models.User{})
	if withDeleted {
		q = q.Unscoped()
	}
	if keyword != "" {
		escaped := strings.NewReplacer(`%`, `\%`, `_`, `\_`).Replace(keyword)
		like := "%" + escaped + "%"
		q = q.Where("username LIKE ? OR email LIKE ? OR nickname LIKE ?", like, like, like)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}

	var users []models.User
	q.Preload("Roles").Preload("Plan").Order("id DESC").
		Offset((page - 1) * pageSize).Limit(pageSize).Find(&users)

	list := make([]map[string]interface{}, 0, len(users))
	for i := range users {
		m := users[i].Public()
		if users[i].DeletedAt.Valid {
			m["deleted_at"] = users[i].DeletedAt.Time
		}
		list = append(list, m)
	}

	auth.Ok(c, gin.H{
		"list":      list,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// GetUser 用户详情
func GetUser(c *gin.Context) {
	id := c.Param("id")
	var user models.User
	if err := db.DB.Preload("Roles").Preload("Plan").First(&user, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			auth.Fail(c, http.StatusNotFound, "用户不存在")
			return
		}
		auth.Fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}
	auth.Ok(c, user.Public())
}

type updateUserRequest struct {
	Nickname *string `json:"nickname"`
	Email    *string `json:"email"`
	Status   *int    `json:"status"`
}

// isDuplicateKeyErr 跨方言识别唯一约束冲突（MySQL 1062 / SQLite 与 PostgreSQL 的文案不同）。
// 预检挡不住并发窗口，所以写入侧也要能把它归到 409 而不是 500。
func isDuplicateKeyErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Duplicate entry") ||
		strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "duplicate key value violates")
}

// UpdateUser 更新用户信息
func UpdateUser(c *gin.Context) {
	id := c.Param("id")
	var req updateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		auth.Fail(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}

	updates := map[string]interface{}{}
	if req.Nickname != nil {
		updates["nickname"] = strings.TrimSpace(*req.Nickname)
	}
	if req.Email != nil {
		cleaned := strings.ToLower(strings.TrimSpace(*req.Email))
		if cleaned == "" {
			// users.email 声明为 not null，写 NULL 会撞约束变 500；历史 NULL 行只读不改
			auth.Fail(c, http.StatusBadRequest, "邮箱不能清空，请保留原邮箱或填写新邮箱")
			return
		}
		if _, err := mail.ParseAddress(cleaned); err != nil {
			auth.Fail(c, http.StatusBadRequest, "邮箱格式不正确")
			return
		}
		updates["email"] = cleaned
	}
	if req.Status != nil {
		updates["status"] = *req.Status
	}

	if len(updates) == 0 {
		auth.Fail(c, http.StatusBadRequest, "无更新字段")
		return
	}

	// 邮箱唯一性预检（与建用户、注册同一口径）：users.email 的唯一索引**覆盖软删除行**，
	// 注销账号仍占着邮箱。缺这一步时约束冲突会一路撞到驱动，变成没头没尾的 500。
	// 占用者 id 写进文案：管理员据此决定是换邮箱还是释放注销账号的占用。
	uid, _ := strconv.ParseUint(id, 10, 64)
	if email, ok := updates["email"]; ok {
		var holder models.User
		err := db.DB.Unscoped().Where("email = ? AND id <> ?", email, uid).First(&holder).Error
		if err == nil {
			if holder.DeletedAt.Valid {
				auth.Fail(c, http.StatusConflict,
					"该邮箱已被注销账号占用（用户 #"+strconv.FormatUint(uint64(holder.ID), 10)+"），不可分配")
			} else {
				auth.Fail(c, http.StatusConflict,
					"邮箱已被用户 #"+strconv.FormatUint(uint64(holder.ID), 10)+" 使用")
			}
			return
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			auth.Fail(c, http.StatusInternalServerError, "数据库错误")
			return
		}
	}

	if err := db.DB.Model(&models.User{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		if isDuplicateKeyErr(err) { // 预检与写入之间被人抢注，兜底成 409 而不是 500
			auth.Fail(c, http.StatusConflict, "用户名或邮箱已被占用")
			return
		}
		log.Printf("ERROR: 更新用户 %s 失败: %v", id, err)
		auth.Fail(c, http.StatusInternalServerError, "更新失败")
		return
	}

	// 如果将用户禁用（status=0），吊销该用户全部登录会话，阻止已登录的禁用用户继续使用 API
	if req.Status != nil && *req.Status == 0 {
		if uid, err := strconv.ParseUint(id, 10, 64); err == nil {
			db.RevokeOtherSessions(uint(uid), "")
		}
	}

	auth.Ok(c, gin.H{"message": "更新成功"})
}

// DeleteUser 删除用户（软删除，同时吊销该用户全部登录会话）
func DeleteUser(c *gin.Context) {
	id := c.Param("id")

	currentID, _ := c.Get("user_id")
	if currentUserID, ok := currentID.(uint); ok {
		if reqID, err := strconv.ParseUint(id, 10, 64); err == nil && uint(reqID) == currentUserID {
			auth.Fail(c, http.StatusBadRequest, "不能删除自己")
			return
		}
	}

	if err := db.DB.Delete(&models.User{}, id).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "删除失败")
		return
	}
	// 吊销被删用户的全部会话，防止其持有未过期 JWT 继续调用
	if uid, err := strconv.ParseUint(id, 10, 64); err == nil {
		db.RevokeOtherSessions(uint(uid), "")
	}
	auth.Ok(c, gin.H{"message": "已删除"})
}

// RestoreUser 恢复软删除的用户
func RestoreUser(c *gin.Context) {
	id := c.Param("id")

	var user models.User
	if err := db.DB.Unscoped().First(&user, id).Error; err != nil {
		auth.Fail(c, http.StatusNotFound, "用户不存在")
		return
	}
	if !user.DeletedAt.Valid {
		auth.Fail(c, http.StatusBadRequest, "该用户未被删除")
		return
	}
	if err := db.DB.Unscoped().Model(&user).Update("deleted_at", nil).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "恢复失败")
		return
	}
	auth.Ok(c, gin.H{"message": "已恢复"})
}

type updateRolesRequest struct {
	RoleCode  string   `json:"role_code"`  // 角色单选；空字符串=清空角色
	RoleCodes []string `json:"role_codes"` // 已废弃的多选负载：非空时拒绝，防止旧调用方静默清空角色
}

// UpdateUserRoles 修改用户角色（单选模型，整体替换为单个角色）
func UpdateUserRoles(c *gin.Context) {
	id := c.Param("id")
	var req updateRolesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		auth.Fail(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}
	if len(req.RoleCodes) > 0 {
		auth.Fail(c, http.StatusBadRequest, "角色已改为单选，请传 role_code（单个角色标识）")
		return
	}

	var user models.User
	if err := db.DB.First(&user, id).Error; err != nil {
		auth.Fail(c, http.StatusNotFound, "用户不存在")
		return
	}

	var roleID uint
	if req.RoleCode != "" {
		var role models.Role
		if err := db.DB.Where("code = ?", req.RoleCode).First(&role).Error; err != nil {
			auth.Fail(c, http.StatusBadRequest, "角色不存在")
			return
		}
		roleID = role.ID
	}

	if err := db.ReplaceUserRole(db.DB, user.ID, roleID); err != nil {
		auth.Fail(c, http.StatusInternalServerError, "角色更新失败")
		return
	}
	auth.Ok(c, gin.H{"message": "角色已更新"})
}

type resetPasswordRequest struct {
	NewPassword string `json:"new_password" binding:"required,min=8,max=16"`
}

// ResetUserPassword 管理员重置用户密码
func ResetUserPassword(c *gin.Context) {
	id := c.Param("id")
	var req resetPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		auth.Fail(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}

	if !auth.ValidatePassword(req.NewPassword) {
		auth.Fail(c, http.StatusBadRequest, "密码必须为 8-16 位且包含字母和数字")
		return
	}

	var user models.User
	if err := db.DB.First(&user, id).Error; err != nil {
		auth.Fail(c, http.StatusNotFound, "用户不存在")
		return
	}

	if err := user.SetPassword(req.NewPassword); err != nil {
		auth.Fail(c, http.StatusInternalServerError, "密码加密失败")
		return
	}
	if err := db.DB.Save(&user).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "保存失败")
		return
	}
	auth.Ok(c, gin.H{"message": "密码已重置"})
}

// ListRoles 角色列表
func ListRoles(c *gin.Context) {
	var roles []models.Role
	db.DB.Order("id ASC").Find(&roles)
	auth.Ok(c, roles)
}

// ListSettings 系统设置列表
func ListSettings(c *gin.Context) {
	var settings []models.SystemSetting
	db.DB.Find(&settings)
	// 排序放内存里做：`key` 在 MySQL 是保留字要加引号，而反引号是 MySQL 方言、
	// PostgreSQL 只认双引号——ORDER BY 里怎么写都不跨方言，交给 GORM 又只支持条件形式
	sort.Slice(settings, func(i, j int) bool { return settings[i].Key < settings[j].Key })
	auth.Ok(c, settings)
}

type updateSettingRequest struct {
	// 不用 required：空值是合法设置（如撤下公告 announcement、清空按源白名单）
	Value string `json:"value"`
}

// UpdateSetting 更新设置项。type='json' 的 key 在校验通过后压缩为单行落库，
// 校验失败直接 400——坏值留在库里只会等到运行时才炸。
func UpdateSetting(c *gin.Context) {
	key := c.Param("key")
	var req updateSettingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		auth.Fail(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}

	var setting models.SystemSetting
	if err := db.DB.Where(map[string]interface{}{"key": key}).First(&setting).Error; err != nil {
		auth.Fail(c, http.StatusNotFound, "设置项不存在")
		return
	}
	value := req.Value
	if setting.Type == "json" {
		normalized, err := normalizeJSONValue(value)
		if err != nil {
			auth.Fail(c, http.StatusBadRequest, err.Error())
			return
		}
		value = normalized
	}

	result := db.DB.Model(&models.SystemSetting{}).Where(map[string]interface{}{"key": key}).Update("value", value)
	if result.Error != nil {
		auth.Fail(c, http.StatusInternalServerError, "更新失败")
		return
	}
	db.InvalidateSettingCache(key)
	auth.Ok(c, gin.H{"message": "已更新", "value": value})
}

// ListQuotaPlans 额度套餐列表
func ListQuotaPlans(c *gin.Context) {
	var plans []models.QuotaPlan
	db.DB.Preload("Role").Order("id ASC").Find(&plans)
	auth.Ok(c, plans)
}

// ListQuotaLimits 某套餐下的额度限制
func ListQuotaLimits(c *gin.Context) {
	id := c.Param("id")
	var limits []models.QuotaLimit
	db.DB.Where("plan_id = ?", id).Order("id ASC").Find(&limits)
	auth.Ok(c, limits)
}

type updateQuotaCostRequest struct {
	Cost       *int64 `json:"cost"`
	Status     *int   `json:"status"`
	Interval   *int64 `json:"interval"`
	LimitCount *int64 `json:"limit_count"`
	WindowSec  *int64 `json:"window_sec"`
}

// rateLimitUpdates 校验并构造限流字段的更新集（三个字段均可选）。
// 返回的 msg 非空表示校验失败；三个字段全空时返回空更新集且 msg 为空，由调用方决定是否算错误。
func rateLimitUpdates(interval, limitCount, windowSec *int64) (map[string]interface{}, string) {
	updates := map[string]interface{}{}
	if interval != nil {
		if *interval < 0 {
			return nil, "interval 不能为负数"
		}
		updates["interval"] = *interval
	}
	if limitCount != nil {
		if *limitCount < 0 {
			return nil, "limit_count 不能为负数"
		}
		updates["limit_count"] = *limitCount
	}
	if windowSec != nil {
		if *windowSec <= 0 {
			return nil, "window_sec 必须大于 0"
		}
		updates["window_sec"] = *windowSec
	}
	return updates, ""
}

func ListQuotaCosts(c *gin.Context) {
	// group_code 列存数据源码；前端传 source_code，group 为历史参数名兼容
	code := c.Query("source_code")
	if code == "" {
		code = c.Query("group")
	}
	q := db.DB.Model(&models.QuotaCost{}).Order("group_code ASC, interface ASC")
	if code != "" {
		q = q.Where("group_code = ?", code)
	}
	var costs []models.QuotaCost
	q.Find(&costs)
	auth.Ok(c, costs)
}

func UpdateQuotaCost(c *gin.Context) {
	id := c.Param("id")
	var req updateQuotaCostRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		auth.Fail(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}
	updates := map[string]interface{}{}
	if req.Cost != nil {
		updates["cost"] = *req.Cost
	}
	if req.Status != nil {
		updates["status"] = *req.Status
	}
	rateUpdates, msg := rateLimitUpdates(req.Interval, req.LimitCount, req.WindowSec)
	if msg != "" {
		auth.Fail(c, http.StatusBadRequest, msg)
		return
	}
	for k, v := range rateUpdates {
		updates[k] = v
	}
	if len(updates) == 0 {
		auth.Fail(c, http.StatusBadRequest, "无更新字段")
		return
	}
	result := db.DB.Model(&models.QuotaCost{}).Where("id = ?", id).Updates(updates)
	if result.Error != nil {
		auth.Fail(c, http.StatusInternalServerError, "更新失败")
		return
	}
	if result.RowsAffected == 0 {
		auth.Fail(c, http.StatusNotFound, "记录不存在")
		return
	}
	auth.Ok(c, gin.H{"message": "已更新"})
}

// ListQuotaPlanCosts 获取套餐级别的速率限制配置
func ListQuotaPlanCosts(c *gin.Context) {
	var planCosts []models.QuotaCostPlan
	db.DB.Order("plan_id ASC, group_code ASC, interface ASC").Find(&planCosts)
	auth.Ok(c, planCosts)
}

type updateQuotaPlanCostRequest struct {
	Interval   *int64 `json:"interval"`
	LimitCount *int64 `json:"limit_count"`
	WindowSec  *int64 `json:"window_sec"`
}

// UpdateQuotaPlanCost 更新套餐级别速率限制配置（interval / limit_count / window_sec 至少一项）
func UpdateQuotaPlanCost(c *gin.Context) {
	id := c.Param("id")
	var req updateQuotaPlanCostRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		auth.Fail(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}
	updates, msg := rateLimitUpdates(req.Interval, req.LimitCount, req.WindowSec)
	if msg != "" {
		auth.Fail(c, http.StatusBadRequest, msg)
		return
	}
	if len(updates) == 0 {
		auth.Fail(c, http.StatusBadRequest, "无更新字段")
		return
	}
	result := db.DB.Model(&models.QuotaCostPlan{}).Where("id = ?", id).Updates(updates)
	if result.Error != nil {
		auth.Fail(c, http.StatusInternalServerError, "更新失败")
		return
	}
	if result.RowsAffected == 0 {
		auth.Fail(c, http.StatusNotFound, "记录不存在")
		return
	}
	auth.Ok(c, gin.H{"message": "已更新"})
}

type upsertQuotaPlanCostRequest struct {
	PlanID     uint   `json:"plan_id" binding:"required"`
	GroupCode  string `json:"group_code" binding:"required"`
	Interface  string `json:"interface" binding:"required"`
	Interval   *int64 `json:"interval" binding:"required"`
	LimitCount *int64 `json:"limit_count"`
	WindowSec  *int64 `json:"window_sec"`
}

// UpsertQuotaPlanCost 按 (plan_id, group_code, interface) 唯一键新建或更新套餐级速率限制
// 用于套餐尚无覆盖行时的首次配置（UpdateQuotaPlanCost 只能更新已有行）
func UpsertQuotaPlanCost(c *gin.Context) {
	var req upsertQuotaPlanCostRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		auth.Fail(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}
	if _, msg := rateLimitUpdates(req.Interval, req.LimitCount, req.WindowSec); msg != "" {
		auth.Fail(c, http.StatusBadRequest, msg)
		return
	}
	pc := models.QuotaCostPlan{
		PlanID:    req.PlanID,
		GroupCode: req.GroupCode,
		Interface: req.Interface,
		Interval:  *req.Interval,
		WindowSec: 60,
	}
	if req.LimitCount != nil {
		pc.LimitCount = *req.LimitCount
	}
	if req.WindowSec != nil {
		pc.WindowSec = *req.WindowSec
	}
	if err := db.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "plan_id"}, {Name: "group_code"}, {Name: "interface"}},
		DoUpdates: clause.AssignmentColumns([]string{"interval", "limit_count", "window_sec", "updated_at"}),
	}).Create(&pc).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "保存失败")
		return
	}
	auth.Ok(c, pc)
}

// ListPlatformSourceConfigs 平台级数据源默认配置：清单来自 data_sources 表
// （数据源由各自源包声明播种，管理端亦可手工新增），底座项目无源时返回空列表
func ListPlatformSourceConfigs(c *gin.Context) {
	var configs []models.PlatformSourceConfig
	db.DB.Find(&configs)
	configMap := make(map[string]string)
	for _, cfg := range configs {
		configMap[cfg.SourceName] = cfg.BaseURL
	}

	var sources []models.DataSource
	db.DB.Order("sort_order, id").Find(&sources)

	items := make([]map[string]interface{}, 0, len(sources))
	for _, s := range sources {
		items = append(items, map[string]interface{}{
			"source_name":    s.Name,
			"source_display": s.DisplayName,
			"base_url":       configMap[s.Name],
		})
	}
	auth.Ok(c, items)
}

type platformSourceConfigItem struct {
	SourceName string `json:"source_name"`
	BaseURL    string `json:"base_url"`
}

type updatePlatformSourceConfigsRequest struct {
	Configs []platformSourceConfigItem `json:"configs" binding:"required"`
}

// UpdatePlatformSourceConfigs 批量更新平台级数据源默认配置
func UpdatePlatformSourceConfigs(c *gin.Context) {
	var req updatePlatformSourceConfigsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		auth.Fail(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}

	for _, cfg := range req.Configs {
		if cfg.BaseURL == "" {
			db.DB.Where("source_name = ?", cfg.SourceName).Delete(&models.PlatformSourceConfig{})
		} else {
			db.DB.Where("source_name = ?", cfg.SourceName).
				Assign(models.PlatformSourceConfig{SourceName: cfg.SourceName, BaseURL: cfg.BaseURL}).
				FirstOrCreate(&models.PlatformSourceConfig{})
		}
	}
	auth.Ok(c, gin.H{"message": "已更新"})
}

// ===== 数据源管理 =====

type createDataSourceRequest struct {
	Name        string `json:"name" binding:"required"`
	DisplayName string `json:"display_name" binding:"required"`
	Category    string `json:"category" binding:"required"`
	Description string `json:"description"`
	Status      *int   `json:"status"`
	SortOrder   *int   `json:"sort_order"`
}

// ListDataSources 获取数据源列表
func ListDataSources(c *gin.Context) {
	var dataSources []models.DataSource
	db.DB.Order("sort_order ASC, id ASC").Find(&dataSources)
	public := rankPublicSet()
	views := make([]dataSourceView, 0, len(dataSources))
	for _, ds := range dataSources {
		views = append(views, dataSourceView{DataSource: ds, RankPublic: public[ds.Name]})
	}
	auth.Ok(c, views)
}

// CreateDataSource 创建数据源
func CreateDataSource(c *gin.Context) {
	var req createDataSourceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		auth.Fail(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}

	// 检查名称是否已存在
	var count int64
	db.DB.Model(&models.DataSource{}).Where("name = ?", req.Name).Count(&count)
	if count > 0 {
		auth.Fail(c, http.StatusConflict, "数据源标识已存在")
		return
	}

	status := 1
	if req.Status != nil {
		status = *req.Status
	}
	sortOrder := 0
	if req.SortOrder != nil {
		sortOrder = *req.SortOrder
	}

	ds := models.DataSource{
		Name:        req.Name,
		DisplayName: req.DisplayName,
		Category:    req.Category,
		Description: req.Description,
		Status:      status,
		SortOrder:   sortOrder,
	}
	if err := db.DB.Create(&ds).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "创建数据源失败")
		return
	}
	catalog.InvalidateDatasourcesCache()
	auth.Ok(c, ds)
}

type updateDataSourceRequest struct {
	DisplayName *string `json:"display_name"`
	Category    *string `json:"category"`
	Description *string `json:"description"`
	Status      *int    `json:"status"`
	SortOrder   *int    `json:"sort_order"`
	GroupID     *uint   `json:"group_id"`    // 归入某组（一源至多一组）
	ClearGroup  bool    `json:"clear_group"` // 显式摘出分组：JSON 里 null 与「不带该字段」无法区分，故单列一个开关
	RankPublic  *bool   `json:"rank_public"` // 公开榜单可见性：写的是设置项 rank_public_sources 的名单，不落源行
}

// dataSourceView 数据源行 + 两个「存在别处但列表要看」的派生字段
type dataSourceView struct {
	models.DataSource
	RankPublic bool `json:"rank_public"`
}

// UpdateDataSource 更新数据源
func UpdateDataSource(c *gin.Context) {
	id := c.Param("id")
	var req updateDataSourceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		auth.Fail(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}

	var ds models.DataSource
	if err := db.DB.First(&ds, id).Error; err != nil {
		auth.Fail(c, http.StatusNotFound, "数据源不存在")
		return
	}

	updates := map[string]interface{}{}
	if req.DisplayName != nil {
		updates["display_name"] = *req.DisplayName
	}
	if req.Category != nil {
		updates["category"] = *req.Category
	}
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	if req.Status != nil {
		updates["status"] = *req.Status
	}
	if req.SortOrder != nil {
		updates["sort_order"] = *req.SortOrder
	}
	if req.GroupID != nil && req.ClearGroup {
		auth.Fail(c, http.StatusBadRequest, "group_id 与 clear_group 不能同时给")
		return
	}
	if req.ClearGroup {
		updates["group_id"] = nil
	} else if req.GroupID != nil {
		var group models.SourceGroup
		if err := db.DB.First(&group, *req.GroupID).Error; err != nil {
			auth.Fail(c, http.StatusNotFound, "分组不存在")
			return
		}
		updates["group_id"] = group.ID
	}
	if len(updates) == 0 && req.RankPublic == nil {
		auth.Fail(c, http.StatusBadRequest, "无更新字段")
		return
	}

	if len(updates) > 0 {
		if err := db.DB.Model(&ds).Updates(updates).Error; err != nil {
			auth.Fail(c, http.StatusInternalServerError, "更新失败")
			return
		}
		if err := db.DB.First(&ds, ds.ID).Error; err != nil {
			auth.Fail(c, http.StatusInternalServerError, "数据库错误")
			return
		}
		catalog.InvalidateDatasourcesCache()
	}

	// 榜单可见性存的是设置名单，不是源行——回读一次保证响应与实际名单一致
	rankPublic := rankPublicSet()[ds.Name]
	if req.RankPublic != nil {
		value, err := setSourceRankPublic(ds.Name, *req.RankPublic)
		if err != nil {
			auth.Fail(c, http.StatusInternalServerError, "榜单可见性更新失败")
			return
		}
		rankPublic = *req.RankPublic
		log.Printf("公开榜单放行名单变更: source=%s enable=%v 现名单=%s", ds.Name, rankPublic, value)
	}
	auth.Ok(c, dataSourceView{DataSource: ds, RankPublic: rankPublic})
}

// DeleteDataSource 删除数据源（软删除）
func DeleteDataSource(c *gin.Context) {
	id := c.Param("id")

	var ds models.DataSource
	if err := db.DB.First(&ds, id).Error; err != nil {
		auth.Fail(c, http.StatusNotFound, "数据源不存在")
		return
	}

	// 检查是否被套餐授权（授权现在就是一行限额，见 gate/grant.go）
	var count int64
	db.DB.Model(&models.QuotaLimit{}).
		Where("scope = ? AND target = ?", "source", ds.Name).Count(&count)
	if count > 0 {
		auth.Fail(c, http.StatusBadRequest, "该数据源被套餐引用，无法删除")
		return
	}

	if err := db.DB.Delete(&ds).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "删除失败")
		return
	}
	// 顺手把它从公开榜单放行名单里摘掉：留着不会放行任何东西（源已不在），但会让名单越长越脏
	if _, err := setSourceRankPublic(ds.Name, false); err != nil {
		log.Printf("WARNING: 从公开榜单名单摘除 %s 失败: %v", ds.Name, err)
	}
	catalog.InvalidateDatasourcesCache()
	auth.Ok(c, gin.H{"message": "已删除"})
}

// ===== 套餐-数据源授权（授权与限额同一行，见 gate/grant.go；待办清单 P34）=====

// ListPlanDataSources 获取套餐已授权的数据源（= 有 scope=source 限额行的源）
func ListPlanDataSources(c *gin.Context) {
	planID := c.Param("id")

	var plan models.QuotaPlan
	if err := db.DB.First(&plan, planID).Error; err != nil {
		auth.Fail(c, http.StatusNotFound, "套餐不存在")
		return
	}

	dataSources := grantedDataSources(plan.ID)
	auth.Ok(c, gin.H{
		"plan_id":      plan.ID,
		"plan_name":    plan.Name,
		"data_sources": dataSources,
	})
}

// grantedDataSources 授权源的数据源行（按名排序）；限额行指向已下线的源时自然查不到行。
func grantedDataSources(planID uint) []models.DataSource {
	out := []models.DataSource{}
	ids := gate.PlanAllowedSourceIDs(planID)
	if len(ids) == 0 {
		return out
	}
	idList := make([]uint, 0, len(ids))
	for id := range ids {
		idList = append(idList, id)
	}
	db.DB.Where("id IN ?", idList).Order("name").Find(&out)
	return out
}

type addPlanDataSourceRequest struct {
	DataSourceID uint `json:"data_source_id" binding:"required"`
}

type batchAddPlanDataSourcesRequest struct {
	DataSourceIDs []uint `json:"data_source_ids" binding:"required,min=1"`
}

// planDataSourceName 校验数据源存在并返回其数据源码（授权判定认名字，不认 ID）
func planDataSourceName(dsID uint) (string, bool) {
	var ds models.DataSource
	if err := db.DB.First(&ds, dsID).Error; err != nil {
		return "", false
	}
	return ds.Name, true
}

// AddPlanDataSource 授予套餐一个数据源（= 保证有一行 limit=-1 的限额）
func AddPlanDataSource(c *gin.Context) {
	var plan models.QuotaPlan
	if err := db.DB.First(&plan, c.Param("id")).Error; err != nil {
		auth.Fail(c, http.StatusNotFound, "套餐不存在")
		return
	}

	var req addPlanDataSourceRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		auth.Fail(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}
	name, ok := planDataSourceName(req.DataSourceID)
	if !ok {
		auth.Fail(c, http.StatusNotFound, "数据源不存在")
		return
	}

	created, err := gate.GrantPlanSource(plan.ID, name)
	if err != nil {
		auth.Fail(c, http.StatusInternalServerError, "授权失败")
		return
	}
	if !created {
		auth.Fail(c, http.StatusConflict, "该套餐已包含此数据源")
		return
	}
	catalog.InvalidateDatasourcesCache()
	auth.Ok(c, gin.H{"plan_id": plan.ID, "data_source_id": req.DataSourceID, "limit": -1, "period": "day"})
}

// BatchAddPlanDataSources 批量授权（跳过不存在的源与已授权的源）
func BatchAddPlanDataSources(c *gin.Context) {
	var plan models.QuotaPlan
	if err := db.DB.First(&plan, c.Param("id")).Error; err != nil {
		auth.Fail(c, http.StatusNotFound, "套餐不存在")
		return
	}

	var req batchAddPlanDataSourcesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		auth.Fail(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}

	granted := 0
	for _, dsID := range req.DataSourceIDs {
		name, ok := planDataSourceName(dsID)
		if !ok {
			continue
		}
		created, err := gate.GrantPlanSource(plan.ID, name)
		if err != nil || !created {
			continue
		}
		granted++
	}
	if granted > 0 {
		catalog.InvalidateDatasourcesCache()
	}
	auth.Ok(c, gin.H{"created": granted})
}

// RemovePlanDataSource 回收一个数据源：**同时取消该源在此套餐下的限额**——它们是同一行。
func RemovePlanDataSource(c *gin.Context) {
	var plan models.QuotaPlan
	if err := db.DB.First(&plan, c.Param("id")).Error; err != nil {
		auth.Fail(c, http.StatusNotFound, "套餐不存在")
		return
	}
	dsID, err := strconv.ParseUint(c.Param("ds_id"), 10, 64)
	if err != nil {
		auth.Fail(c, http.StatusBadRequest, "参数错误: data source id")
		return
	}
	name, ok := planDataSourceName(uint(dsID))
	if !ok {
		auth.Fail(c, http.StatusNotFound, "数据源不存在")
		return
	}

	if err := gate.UngrantPlanSource(plan.ID, name); err != nil {
		auth.Fail(c, http.StatusInternalServerError, "移除失败")
		return
	}
	catalog.InvalidateDatasourcesCache()
	auth.Ok(c, gin.H{"message": "已移除"})
}
