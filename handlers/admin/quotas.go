package admin

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"loomproxy/db"
	"loomproxy/gate"
	"loomproxy/handlers/auth"
	"loomproxy/handlers/catalog"
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

// periodNotEnforced 报告这一行的 period 是不是一个「被接受、被存储、被面板当单位显示，却没有任何判定读它」的值。
// **判据只在这一处**：额度窗口只有一个口径「当日」（`gate.UsageSince` = max(零点, 管理员刷新时刻)），
// 全仓没有任何判定读 `quota_limits.period`（`gate/plan.go` 按 plan_id+scope 取行、`gate/usage.go` 按日累计）。
// 于是库里一行 `limit=100, period=month` 在面板读作「100 / 每月」，闸门放的却是「100 / 每日」。
// `limit<0` 是不限额，窗口多长都不成立，那种行不必喊；day 是播种与授权口的既有值、也是唯一与判定吻合的值，也不喊。
// 这里只把不一致说出口，不代替拍板（三个选项见待办清单 P70）。
func periodNotEnforced(period string, limit int64) bool {
	return limit >= 0 && period != "" && period != "day"
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
	// 删套餐会连带删掉它的授权行（限额即授权），缓存视图要跟着失效
	catalog.InvalidateDatasourcesCache()
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

	// 授权判定现在就靠这一行（gate/grant.go），写错名字不会报错、只会静默不发源，所以在入口炸掉
	if req.Scope == "source" {
		var n int64
		if err := db.DB.Model(&models.DataSource{}).Where("name = ?", req.Target).Count(&n).Error; err != nil {
			auth.Fail(c, http.StatusInternalServerError, "数据库错误")
			return
		}
		if n == 0 {
			auth.Fail(c, http.StatusBadRequest, "数据源不存在: "+req.Target)
			return
		}
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
	if quotaLimit.Scope == "source" {
		// 新增一行 source 限额 = 该套餐多了一个可用源，/datasources 的缓存视图要跟着变
		catalog.InvalidateDatasourcesCache()
	}
	if periodNotEnforced(quotaLimit.Period, quotaLimit.Limit) {
		// 不带 period 的调用（脚本、直连 API）落的就是这个不参与判定的默认值 month
		log.Printf("ERROR: 新建限额行 period=%q（scope=%s target=%s plan_id=%d limit=%d），而没有任何判定读这一列——额度窗口只有一个口径「当日」（gate.UsageSince）。面板读作「%d / %s」，闸门放的是每日 %d 次（待办清单 P70）",
			quotaLimit.Period, quotaLimit.Scope, quotaLimit.Target, quotaLimit.PlanID, quotaLimit.Limit, quotaLimit.Limit, quotaLimit.Period, quotaLimit.Limit)
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
	if periodNotEnforced(quotaLimit.Period, quotaLimit.Limit) {
		log.Printf("ERROR: 限额行 id=%d 改完是 period=%q（scope=%s target=%s limit=%d），而没有任何判定读这一列——额度窗口只有一个口径「当日」（gate.UsageSince）。面板读作「%d / %s」，闸门放的是每日 %d 次（待办清单 P70）",
			quotaLimit.ID, quotaLimit.Period, quotaLimit.Scope, quotaLimit.Target, quotaLimit.Limit, quotaLimit.Limit, quotaLimit.Period, quotaLimit.Limit)
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
	if quotaLimit.Scope == "source" {
		// 删一行 = 回收该源（授权与限额同一行），不失效缓存就会让旧视图多留一个点
		catalog.InvalidateDatasourcesCache()
	}
	auth.Ok(c, gin.H{"message": "已删除"})
}

type QuotaOverrideItem struct {
	SourceCode string `json:"source_code"` // 数据源标识
	SourceName string `json:"source_name"` // 显示名称
	Category   string `json:"category"`    // 所属组，如 fq
	Granted    bool   `json:"granted"`     // 该套餐是否授权了这个源（= 有没有那行限额，P34）
	PlanLimit  *int64 `json:"plan_limit"`  // 套餐限额（-1=不限；granted=false 时为 nil=未授权，不是「不限」）
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
	aliases := db.DisplayAliasesFor(user.ID)

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
	// 套餐轴的限额与授权是同一张表同一批行（gate/grant.go），这里只读一份：
	// map 里有这个源 = 授权了；值 -1 = 授权且不限额；没有这一项 = 无权限（不是不限）。
	planLimits := gate.PlanSourceLimits(ptrOrZero(planID))

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
			item.Granted = true
		}
		if l, ok := overrideMap[ds.Name]; ok {
			v := l
			item.UserLimit = &v
		}
		item.Used = gate.UsedToday(&user, ds.Name)
		// 生效额度 = 计划额度 + 用户覆盖（覆盖为空视为 0，可加可减，下限 0；
		// 计划额度为负（不限）时覆盖不生效，仍是不限）。
		// **未授权的源不参与这条算式**：访问控制（order 400）先于计费（500），
		// 该源对这名用户就是 403，所以这里给 -1 会被读成「不限」——面板按 granted 显示「无权限」。
		if !item.Granted {
			item.Effective = -1
		} else if item.PlanLimit != nil && *item.PlanLimit >= 0 {
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
		// 起算点与刷新时刻一起下发：面板的「已用」是按它算的，不带上这两个值，
		// 刷新过的用户就会读成「今天还没用」而不是「今天 13:40 之后没用」
		"quota_reset_at": user.QuotaResetAt,
		"usage_since":    gate.UsageSince(&user),
		// 别名作为**第二栏**下发，plan_name 仍是默认名：管理员的读数不被被观察者修饰（P43）
		"plan_alias": aliases[models.DisplayAliasKey(models.DisplayKindPlan, ptrOrZero(planID))],
		"items":      items,
	})
}

// RefreshUserQuota 手动刷新某用户的单日额度（待办清单 P41）：把用量起算点推到此刻。
//
// 刻意**不删也不冲正** quota_usage_logs：那张表是只追加的账本，删行等于毁掉
// 「今天到底用了多少」的证据，写负数行等于在总和里掺假账。要改的是起算点这一份事实，
// 而它只有 gate.UsageSince 一处定义，所以判定与读数会一起跟着走。
// 过了零点它自然失效（新的一天本来就从零开始），不需要任何定时任务去抹掉这个水印。
//
// 可以反复点，一次点击等于再给一天的量——这是「管理员手工放行」本来的语义。
// 代价是单日额度对这名用户暂时不构成约束，所以每次都在服务端留一行 journal：
// 谁刷的、刷掉了多少已用量。事后要回答「今天这人到底用了多少」，流水里查得到。
func RefreshUserQuota(c *gin.Context) {
	id := c.Param("id")
	var user models.User
	if err := db.DB.Preload("Roles").First(&user, id).Error; err != nil {
		auth.Fail(c, http.StatusNotFound, "用户不存在")
		return
	}

	before := gate.UsedTodayAllSources(&user)
	now := time.Now()
	if err := db.DB.Model(&models.User{}).Where(map[string]interface{}{"id": user.ID}).
		Update("quota_reset_at", now).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "刷新失败")
		return
	}

	actor, _ := c.Get("username")
	log.Printf("ADMIN: 刷新单日额度 user=%s operator=%v 刷前当日已用=%d 新起点=%s（流水未删）",
		user.Username, actor, before, now.Format("2006-01-02 15:04:05"))

	auth.Ok(c, gin.H{
		"user_id":        user.ID,
		"username":       user.Username,
		"quota_reset_at": now,
		"used_before":    before,
		"usage_since":    gate.UsageSince(&user),
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
		// 同形状第三处（待办清单 P88）：覆盖值没写进去仍回「已更新」，
		// 而限额判定读的就是这张表——管理员会以为自己刚给这个人放开/收紧过额度。
		q := db.DB.Where("user_id = ? AND group_code = ?", user.ID, o.GroupCode)
		var err error
		if o.Limit == 0 {
			err = q.Delete(&models.UserQuotaOverride{}).Error
		} else {
			err = q.Assign(models.UserQuotaOverride{UserID: user.ID, GroupCode: o.GroupCode, Limit: o.Limit}).
				FirstOrCreate(&models.UserQuotaOverride{}).Error
		}
		if err != nil {
			log.Printf("ERROR: 用户 %d 的额度覆盖写入失败（组 %s / 限额 %d）：%v", user.ID, o.GroupCode, o.Limit, err)
			auth.Fail(c, http.StatusInternalServerError, "保存失败")
			return
		}
	}
	auth.Ok(c, gin.H{"message": "额度覆盖已更新"})
}

// ptrOrZero 取可选套餐 ID 的值；nil（用户没绑套餐）返回 0，让 gate 那侧失败关闭。
func ptrOrZero(p *uint) uint {
	if p == nil {
		return 0
	}
	return *p
}
