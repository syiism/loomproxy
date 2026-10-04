package admin

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"loomproxy/db"
	"loomproxy/handlers/auth"
	"loomproxy/handlers/catalog"
	"loomproxy/models"
)

type createUserRequest struct {
	Username  string   `json:"username" binding:"required,min=3,max=64"`
	Password  string   `json:"password" binding:"required,min=8,max=16"`
	Nickname  string   `json:"nickname"`
	Email     string   `json:"email" binding:"required,email"`
	RoleCode  string   `json:"role_code"`  // 角色单选；套餐绑定角色时以套餐绑定为准
	RoleCodes []string `json:"role_codes"` // 已废弃的多选负载：非空时拒绝
	PlanID    *uint    `json:"plan_id"`
}

// CreateUser 管理员创建用户
func CreateUser(c *gin.Context) {
	var req createUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		auth.Fail(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}
	if len(req.RoleCodes) > 0 {
		auth.Fail(c, http.StatusBadRequest, "角色已改为单选，请传 role_code（单个角色标识）")
		return
	}

	if !auth.ValidatePassword(req.Password) {
		auth.Fail(c, http.StatusBadRequest, "密码必须为 8-16 位且包含字母和数字")
		return
	}

	username := strings.TrimSpace(strings.ToLower(req.Username))

	var existing models.User
	// 用 Unscoped 查重：软删除用户的用户名/邮箱进入黑名单，不可复用
	err := db.DB.Unscoped().Where("username = ?", username).First(&existing).Error
	if err == nil {
		if existing.DeletedAt.Valid {
			auth.Fail(c, http.StatusConflict, "该用户名已被注销账号占用，不可使用")
		} else {
			auth.Fail(c, http.StatusConflict, "用户名已存在")
		}
		return
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		auth.Fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}

	email := strings.ToLower(strings.TrimSpace(req.Email))

	err = db.DB.Unscoped().Where("email = ?", email).First(&existing).Error
	if err == nil {
		if existing.DeletedAt.Valid {
			auth.Fail(c, http.StatusConflict, "该邮箱已被注销账号占用，不可使用")
		} else {
			auth.Fail(c, http.StatusConflict, "邮箱已被使用")
		}
		return
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		auth.Fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}

	planID := req.PlanID
	var boundPlan *models.QuotaPlan
	if req.PlanID == nil {
		// 未指定套餐时默认绑定免费版
		var freePlan models.QuotaPlan
		if err := db.DB.Where("code = ?", "free").First(&freePlan).Error; err == nil {
			planID = &freePlan.ID
			boundPlan = &freePlan
		}
	} else {
		var plan models.QuotaPlan
		if err := db.DB.First(&plan, *req.PlanID).Error; err != nil {
			auth.Fail(c, http.StatusBadRequest, "套餐不存在")
			return
		}
		boundPlan = &plan
	}

	user := &models.User{
		Username: username,
		Email:    email,
		Nickname: strings.TrimSpace(req.Nickname),
		Status:   1,
		PlanID:   planID,
	}
	if err := user.SetPassword(req.Password); err != nil {
		auth.Fail(c, http.StatusInternalServerError, "密码加密失败")
		return
	}
	if err := db.DB.Create(user).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "创建用户失败")
		return
	}

	// 角色单选：先落显式选择的角色；套餐绑定角色时以套餐绑定为准（与套餐变更同步语义一致）
	var roleID uint
	if req.RoleCode != "" {
		var role models.Role
		if err := db.DB.Where("code = ?", req.RoleCode).First(&role).Error; err != nil {
			auth.Fail(c, http.StatusBadRequest, "角色不存在")
			return
		}
		roleID = role.ID
	}
	if boundPlan != nil && boundPlan.RoleID != nil {
		roleID = *boundPlan.RoleID
	}
	if err := db.ReplaceUserRole(db.DB, user.ID, roleID); err != nil {
		auth.Fail(c, http.StatusInternalServerError, "角色分配失败")
		return
	}

	if err := db.DB.Preload("Roles").Preload("Plan").First(user, user.ID).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}
	auth.Ok(c, user.Public(db.DisplayAliasesFor(user.ID)))
}

type updateUserPlanRequest struct {
	PlanID *uint `json:"plan_id"`
}

// UpdateUserPlan 设置用户套餐（plan_id 为 null 时清除）。套餐实际变更时自动把用户
// 角色切换为新套餐的绑定角色（清除套餐时回退 free 套餐的绑定角色；套餐未绑定角色
// 则不动角色）。已有 admin 角色的用户不被同步降级（新套餐绑定 admin 角色时才写入），
// 防止误改套餐把唯一管理员锁在门外；降级请走显式的「角色」操作。
func UpdateUserPlan(c *gin.Context) {
	id := c.Param("id")
	var req updateUserPlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		auth.Fail(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}

	var user models.User
	if err := db.DB.Preload("Roles").First(&user, id).Error; err != nil {
		auth.Fail(c, http.StatusNotFound, "用户不存在")
		return
	}

	planChanged := (user.PlanID == nil) != (req.PlanID == nil) ||
		(req.PlanID != nil && user.PlanID != nil && *user.PlanID != *req.PlanID)

	var targetPlan models.QuotaPlan
	if req.PlanID != nil {
		if err := db.DB.First(&targetPlan, *req.PlanID).Error; err != nil {
			auth.Fail(c, http.StatusBadRequest, "套餐不存在")
			return
		}
	}

	if err := db.DB.Model(&user).Update("plan_id", req.PlanID).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "更新失败")
		return
	}

	if planChanged {
		// 解析同步目标套餐：清除套餐时按免费版回退（与创建用户未指定套餐的语义一致）
		var target *models.QuotaPlan
		if req.PlanID != nil {
			target = &targetPlan
		} else {
			var freePlan models.QuotaPlan
			if err := db.DB.Where("code = ?", "free").First(&freePlan).Error; err == nil {
				target = &freePlan
			}
		}
		if target != nil && target.RoleID != nil {
			var newRole models.Role
			if err := db.DB.First(&newRole, *target.RoleID).Error; err == nil &&
				!(user.HasRole("admin") && newRole.Code != "admin") {
				if err := db.ReplaceUserRole(db.DB, user.ID, newRole.ID); err != nil {
					auth.Fail(c, http.StatusInternalServerError, "角色同步失败")
					return
				}
			}
		}
		// 套餐决定 /datasources 的可见数据源列表，变更后需立即失效其缓存视图
		catalog.InvalidateDatasourcesCache()
	}
	auth.Ok(c, gin.H{"message": "套餐已更新"})
}
