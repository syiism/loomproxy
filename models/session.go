package models

import "time"

// AuthSession 登录会话：用于登录设备管理与“退出其他设备”。
// SessionID 与 JWT 的 jti 声明一一对应。
type AuthSession struct {
	ID           uint       `gorm:"primaryKey" json:"id"`
	SessionID    string     `gorm:"size:64;uniqueIndex;not null" json:"-"`
	UserID       uint       `gorm:"index:idx_session_user_active,priority:1;not null" json:"user_id"`
	Device       string     `gorm:"size:128" json:"device"`
	UserAgent    string     `gorm:"size:512" json:"-"`
	IP           string     `gorm:"size:64" json:"ip"`
	LastActiveAt time.Time  `gorm:"index:idx_session_user_active,priority:2" json:"last_active_at"`
	ExpiresAt    time.Time  `json:"expires_at"`
	RevokedAt    *time.Time `json:"-"`
	CreatedAt    time.Time  `json:"created_at"`
}

func (AuthSession) TableName() string {
	return "auth_sessions"
}
