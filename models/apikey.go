package models

import "time"

// ApiKey 用户自助 API 密钥：网关数据源接口的程序化身份（归属创建者，计费/配额/监控
// 按归属用户统计）。Key 存明文（2026-09-29 小用户量取舍：列表可随时查回）；撤销为物理删除。
type ApiKey struct {
	ID         uint       `gorm:"primaryKey" json:"id"`
	UserID     uint       `gorm:"index;not null" json:"user_id"`
	Key        string     `gorm:"size:64;uniqueIndex;not null" json:"key"` // lp_ + 48 位 hex
	Name       string     `gorm:"size:64;not null;default:default" json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

func (ApiKey) TableName() string { return "api_keys" }
