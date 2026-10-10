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
	"loomproxy/utils"
)

var quotaScopes = map[string]bool{
	"global": true,
	"source": true,
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

	// `limit` 必须显式给（待办清单 P69 选 A 的后半）。过去缺省静默落成 0，
	// 而同样的"授权一个源"在 helper 与播种那两条路上落的是套餐默认档（free 100 / vip 1000 / -1）——
	// 一次授权按通路不同得到 `100` 或 `0`，而 `0` 在限额语义上既不是"不限"也不是"不给用"，是个没人定义的数。
	// 现在"没给"就是 400，不是 0；`-1`（不限）仍然是合法值，所以这条校验只管形态、不管语义。
	if req.Limit == nil {
		auth.Fail(c, http.StatusBadRequest, "必须显式给出 limit（-1 = 不限额；不给不等于 0）")
		return
	}
	quotaLimit := db.NewExplicitGrant(req.PlanID, req.Scope, req.Target, *req.Limit)
	if err := db.DB.Create(&quotaLimit).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "创建额度限制失败")
		return
	}
	if quotaLimit.Scope == "source" {
		// 新增一行 source 限额 = 该套餐多了一个可用源，/datasources 的缓存视图要跟着变
		catalog.InvalidateDatasourcesCache()
	}
	auth.Ok(c, quotaLimit)
}

type updateQuotaLimitRequest struct {
	Limit *int64 `json:"limit"`
	// 没有 `period` 字段是刻意的（待办清单 P70②）：这一列没有任何判定读它，
	// 收下一个被忽略的字段就是面板那个假控件的 API 版本——调用者以为改了，库里也留了值，谁都不动。
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
	if err := db.DB.Where("user_id = ?", user.ID).Find(&overrides).Error; err != nil {
		db.LogReadFail("admin_user_overrides:user_quota_overrides", err)
	}
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
	if err := db.DB.Order("category ASC, sort_order ASC, id ASC").Find(&sources).Error; err != nil {
		db.LogReadFail("admin_plan_sources:data_sources", err)
	}

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

// TransferUserQuota 管理员替某个用户在其两个源之间转移额度（待办清单 P97 落地）。
// 边界与本人端**完全同一条**：都走 `gate.TransferQuota`，这里不重算、不放宽。
// 审计行里的 `via="admin"` 与 `operator_id` 就是这一条与本人那条唯一的区别——
// 没有它，"这个人的额度分布被谁改过"在记录里分不出来。
func TransferUserQuota(c *gin.Context) {
	var req struct {
		From   string `json:"from"`
		To     string `json:"to"`
		Amount int64  `json:"amount"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		log.Printf("参数绑定失败（%s）: %v", c.Request.URL.Path, err)
		auth.Fail(c, http.StatusBadRequest, "参数错误")
		return
	}
	var user models.User
	if err := db.DB.First(&user, c.Param("id")).Error; err != nil {
		auth.Fail(c, http.StatusNotFound, "用户不存在")
		return
	}
	opID, _ := c.Get("user_id")
	operator, _ := opID.(uint)
	res, err := gate.TransferQuota(user.ID, operator, req.From, req.To, req.Amount, "admin")
	if err != nil {
		status, msg := gate.TransferErrorStatus(err)
		if status == 500 {
			log.Printf("ERROR: 管理员发起的额度转移失败 target=%d operator=%d: %v", user.ID, operator, err)
		} else {
			log.Printf("WARN: 管理员额度转移被拒 target=%d %s→%s n=%d: %v", user.ID, req.From, req.To, req.Amount, err)
		}
		auth.Fail(c, status, msg)
		return
	}
	actor, _ := c.Get("username")
	log.Printf("ADMIN: 额度转移 target=%s(%d) %s→%s n=%d operator=%v", user.Username, user.ID, req.From, req.To, req.Amount, actor)
	auth.Ok(c, gin.H{
		// 回的是**被操作的那个人**：面板改完读数要靠它确认"我挪的是这个人的额度"。
		// （这里一度写成了 `res.FromBefore`——一个复制粘贴出来的错，类型还正好对得上，
		// 编译器不会拦、用例拦得住，所以用例里钉了这一格。）
		"user_id":  user.ID,
		"username": user.Username,
		"amount":   res.Amount,
		"from":     gin.H{"code": res.FromCode, "before": res.FromBefore, "after": res.FromAfter},
		"to":       gin.H{"code": res.ToCode, "before": res.ToBefore, "after": res.ToAfter},
	})
}

// ListUserQuotaTransfers 管理端读某个用户的转移历史（待办清单 P97）。
// 与本人那条的**唯一**区别是这里带上操作者账号名——管理面要追责到具体的人，
// 而本人面只给 `via`（见 handlers/quota/transfers.go 那句）。两处都不重算额度，只回记录。
func ListUserQuotaTransfers(c *gin.Context) {
	var user models.User
	if err := db.DB.First(&user, c.Param("id")).Error; err != nil {
		auth.Fail(c, http.StatusNotFound, "用户不存在")
		return
	}
	page, pageSize := utils.Paginate(c.DefaultQuery("page", "1"), c.DefaultQuery("page_size", "20"), 20)
	q := db.DB.Model(&models.QuotaTransferLog{}).Where("user_id = ?", user.ID)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "读取失败")
		return
	}
	var rows []models.QuotaTransferLog
	if err := q.Order("id desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "读取失败")
		return
	}
	// 操作者名字一次查齐：逐条查就是 N+1，而这一屏的记录条数不会小到让批量查询显得多余
	operatorNames := map[uint]string{}
	var ops []models.User
	ids := make([]uint, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.OperatorID)
	}
	if len(ids) > 0 {
		if err := db.DB.Select("id", "username").Where("id IN ?", ids).Find(&ops).Error; err != nil {
			log.Printf("ERROR: 读转移操作者失败 user=%d: %v", user.ID, err)
		}
		for _, o := range ops {
			operatorNames[o.ID] = o.Username
		}
	}
	list := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		list = append(list, gin.H{
			"id":            r.ID,
			"from":          r.FromCode,
			"to":            r.ToCode,
			"amount":        r.Amount,
			"from_before":   r.FromBefore,
			"from_after":    r.FromAfter,
			"to_before":     r.ToBefore,
			"to_after":      r.ToAfter,
			"via":           r.Via,
			"operator_id":   r.OperatorID,
			"operator":      operatorNames[r.OperatorID],
			"from_override": r.FromOverride,
			"to_override":   r.ToOverride,
			"created_at":    r.CreatedAt,
			// 管理端**不过滤** cleared_at（P119 ④ 的 C 档：审计面留着），但要把"这个人自己清过没有"说出来——
			// 不然管理员看到的是一份已经被本人动过的列表却以为它是原始记录，那比过滤更糟。
			"cleared":    r.ClearedAt != nil,
			"cleared_at": r.ClearedAt,
		})
	}
	auth.Ok(c, gin.H{"list": list, "total": total, "page": page, "page_size": pageSize,
		"user_id": user.ID, "username": user.Username})
}
