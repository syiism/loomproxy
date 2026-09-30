package admin

import (
	"net/http"
	"regexp"

	"github.com/gin-gonic/gin"

	"loomproxy-go/db"
	"loomproxy-go/handlers/auth"
	"loomproxy-go/models"
)

var roleCodePattern = regexp.MustCompile(`^[a-z0-9_]+$`)

// 内置角色不允许删除
var builtinRoleCodes = map[string]bool{
	"admin": true,
	"user":  true,
	"vip":   true,
}

type createRoleRequest struct {
	Code        string `json:"code" binding:"required,max=32"`
	Name        string `json:"name" binding:"required,max=64"`
	Description string `json:"description"`
	Status      *int   `json:"status"`
}

// CreateRole 创建角色
func CreateRole(c *gin.Context) {
	var req createRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		auth.Fail(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}
	if !roleCodePattern.MatchString(req.Code) {
		auth.Fail(c, http.StatusBadRequest, "角色标识只能包含小写字母、数字和下划线")
		return
	}

	var count int64
	if err := db.DB.Model(&models.Role{}).Where("code = ? OR name = ?", req.Code, req.Name).Count(&count).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}
	if count > 0 {
		auth.Fail(c, http.StatusConflict, "角色标识或名称已存在")
		return
	}

	status := 1
	if req.Status != nil {
		status = *req.Status
	}
	role := models.Role{
		Code:        req.Code,
		Name:        req.Name,
		Description: req.Description,
		Status:      status,
	}
	if err := db.DB.Create(&role).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "创建角色失败")
		return
	}
	auth.Ok(c, role)
}

type updateRoleRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Status      *int    `json:"status"`
}

// UpdateRole 更新角色
func UpdateRole(c *gin.Context) {
	id := c.Param("id")
	var req updateRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		auth.Fail(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}

	var role models.Role
	if err := db.DB.First(&role, id).Error; err != nil {
		auth.Fail(c, http.StatusNotFound, "角色不存在")
		return
	}

	if role.Code == "admin" && req.Status != nil && *req.Status != 1 {
		auth.Fail(c, http.StatusBadRequest, "内置管理员角色不可禁用")
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
	if len(updates) == 0 {
		auth.Fail(c, http.StatusBadRequest, "无更新字段")
		return
	}

	if err := db.DB.Model(&role).Updates(updates).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "更新失败")
		return
	}
	if err := db.DB.First(&role, role.ID).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}
	auth.Ok(c, role)
}

// DeleteRole 删除角色（软删除，内置角色与有关联用户的角色禁止删除）
func DeleteRole(c *gin.Context) {
	id := c.Param("id")

	var role models.Role
	if err := db.DB.First(&role, id).Error; err != nil {
		auth.Fail(c, http.StatusNotFound, "角色不存在")
		return
	}

	if builtinRoleCodes[role.Code] {
		auth.Fail(c, http.StatusBadRequest, "内置角色不可删除")
		return
	}

	var count int64
	if err := db.DB.Model(&models.UserRole{}).Where("role_id = ?", role.ID).Count(&count).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}
	if count > 0 {
		auth.Fail(c, http.StatusBadRequest, "角色仍有关联用户，不可删除")
		return
	}

	// 被套餐绑定（quota_plans.role_id）的角色不可删：删除后套餐角色同步会失效，
	// 甚至让卡密兑换事务报错回滚；提示先在「额度套餐」中解绑
	var planCount int64
	if err := db.DB.Model(&models.QuotaPlan{}).Where("role_id = ?", role.ID).Count(&planCount).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}
	if planCount > 0 {
		auth.Fail(c, http.StatusBadRequest, "角色已被套餐绑定，请先在额度套餐中解绑")
		return
	}

	if err := db.DB.Delete(&role).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "删除失败")
		return
	}
	auth.Ok(c, gin.H{"message": "已删除"})
}
