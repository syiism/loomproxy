package models

import (
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type User struct {
	ID                uint           `gorm:"primaryKey" json:"id"`
	Username          string         `gorm:"size:64;uniqueIndex;not null" json:"username"`
	Email             string         `gorm:"size:128;uniqueIndex;not null" json:"email"`
	PasswordHash      string         `gorm:"size:255;not null" json:"-"`
	Nickname          string         `gorm:"size:64" json:"nickname"`
	Avatar            string         `gorm:"size:512" json:"avatar"`
	Status            int            `gorm:"default:1" json:"status"`
	Roles             []Role         `gorm:"many2many:user_roles;" json:"roles,omitempty"`
	PlanID            *uint          `gorm:"index" json:"plan_id"`
	Plan              *QuotaPlan     `gorm:"foreignKey:PlanID" json:"plan,omitempty"`
	PlanExpireAt      *time.Time     `json:"plan_expire_at"` // 套餐到期时间；NULL=永久（存量用户兼容）
	LastLoginAt       *time.Time     `json:"last_login_at"`
	UsernameChangedAt *time.Time     `json:"username_changed_at"` // 上次修改用户名时间；NULL=从未修改（用户名每 30 天限改一次）
	TokenExpireHours  *int           `json:"token_expire_hours"`  // 个人登录 token 有效时长（小时）；NULL=跟随系统设置，对下次及以后登录生效
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
	DeletedAt         gorm.DeletedAt `gorm:"index" json:"-"`
}

// EmailStr 安全获取 email 字符串（向后兼容）
func (u *User) EmailStr() string {
	return u.Email
}

func (u *User) SetPassword(password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	u.PasswordHash = string(hash)
	return nil
}

func (u *User) CheckPassword(password string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password))
	return err == nil
}

func (u *User) Public() map[string]interface{} {
	seen := make(map[uint]bool)
	var roles []map[string]interface{}
	for _, r := range u.Roles {
		if seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		roles = append(roles, map[string]interface{}{
			"id":   r.ID,
			"code": r.Code,
			"name": r.Name,
		})
	}
	result := map[string]interface{}{
		"id":                  u.ID,
		"username":            u.Username,
		"email":               u.Email,
		"nickname":            u.Nickname,
		"avatar":              u.Avatar,
		"status":              u.Status,
		"roles":               roles,
		"plan_id":             u.PlanID,
		"plan_expire_at":      u.PlanExpireAt,
		"last_login_at":       u.LastLoginAt,
		"username_changed_at": u.UsernameChangedAt,
		"token_expire_hours":  u.TokenExpireHours,
		"created_at":          u.CreatedAt,
	}
	if u.Plan != nil {
		result["plan"] = map[string]interface{}{
			"code": u.Plan.Code,
			"name": u.Plan.Name,
		}
	}
	return result
}

func (u *User) HasRole(code string) bool {
	for _, r := range u.Roles {
		if r.Code == code {
			return true
		}
	}
	return false
}

func (u *User) IsAdmin() bool {
	return u.HasRole("admin")
}
