package models

import "time"

// BlockedIP IP 黑名单：被拉黑的 IP 无法登录/注册，也无法调用任何接口
type BlockedIP struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	IP        string    `gorm:"size:64;uniqueIndex;not null" json:"ip"`
	Note      string    `gorm:"size:255" json:"note"`
	Source    string    `gorm:"size:16;default:manual" json:"source"` // manual=管理员手动 / auto=自动拉黑
	CreatedAt time.Time `json:"created_at"`
}
