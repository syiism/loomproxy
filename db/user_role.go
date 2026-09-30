package db

import (
	"log"

	"gorm.io/gorm"

	"loomproxy/models"
)

// ReplaceUserRole 将用户角色整体替换为单个角色（角色单选模型，user_roles 表仍保留多行能力）。
// roleID 为 0 时清空该用户全部角色。直接操作 user_roles 表而非 GORM many2many
// Association，规避 Create 后重复 Append 触发唯一索引冲突的历史坑。
func ReplaceUserRole(tx *gorm.DB, userID uint, roleID uint) error {
	if err := tx.Where("user_id = ?", userID).Delete(&models.UserRole{}).Error; err != nil {
		return err
	}
	if roleID == 0 {
		return nil
	}
	return tx.Create(&models.UserRole{UserID: userID, RoleID: roleID}).Error
}

// SyncUserPlanRole 套餐变更时把用户角色同步为套餐绑定角色。
// 套餐未绑定角色（plan 为 nil 或 RoleID 为空）时不做任何变更；
// 绑定角色已不存在（悬空引用，DeleteRole 已拦截新建，此处兜底存量脏数据）时跳过并告警；
// skipAdminRole 为 true 时若绑定的是内置 admin 角色则跳过（自助兑换路径防越权，
// 管理后台的显式分配不受此限制）。
func SyncUserPlanRole(tx *gorm.DB, userID uint, plan *models.QuotaPlan, skipAdminRole bool) error {
	if plan == nil || plan.RoleID == nil {
		return nil
	}
	var role models.Role
	if err := tx.First(&role, *plan.RoleID).Error; err != nil {
		log.Printf("WARN: 套餐 %s 绑定的角色不存在，已跳过角色同步（user_id=%d）", plan.Code, userID)
		return nil
	}
	if skipAdminRole && role.Code == "admin" {
		log.Printf("WARN: 套餐 %s 绑定了 admin 角色，自助路径已跳过角色同步（user_id=%d）", plan.Code, userID)
		return nil
	}
	return ReplaceUserRole(tx, userID, role.ID)
}
