package models

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// HashRedemptionCode SHA-256 hex。2026-09-29 起卡密改存明文（小用户量，换取
// 码可随时查回），此函数仅剩两个用途：验证码仍哈希落库；存量哈希卡密兑换时回落匹配
func HashRedemptionCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// RedemptionCode 套餐兑换卡密（见 docs/套餐升级方案.md）。
// Code 存明文（2026-09-29 起，小用户量取舍：管理列表可随时查码）；存量行可能仍为
// 历史 SHA-256 哈希，兑换时明文未命中则按哈希回落匹配（见 handlers/userconfig/redeem.go）。
type RedemptionCode struct {
	ID           uint       `gorm:"primaryKey" json:"id"`
	Code         string     `gorm:"size:64;uniqueIndex;not null" json:"code"` // SHA-256 hex
	PlanID       uint       `gorm:"index;not null" json:"plan_id"`
	DurationDays int        `gorm:"not null" json:"duration_days"`         // 有效天数，0=永久
	Status       int        `gorm:"default:1;index" json:"status"`         // 1=未使用, 2=已使用, 0=已作废
	BatchNo      string     `gorm:"size:32;index" json:"batch_no"`         // 生成批次号
	Note         string     `gorm:"size:255" json:"note"`                  // 批次备注
	Channel      string     `gorm:"size:16;default:manual" json:"channel"` // manual/payment(预留)
	UsedBy       *uint      `json:"used_by"`                               // 兑换用户 ID
	UsedAt       *time.Time `json:"used_at"`                               // 兑换时间
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

func (RedemptionCode) TableName() string {
	return "redemption_codes"
}

// RedemptionLog 卡密兑换尝试日志（含失败，防爆破审计）
type RedemptionLog struct {
	ID     uint `gorm:"primaryKey" json:"id"`
	UserID uint `gorm:"index" json:"user_id"`
	// Code 记用户输入的原文（已归一为大写去空格）：卡码明文是 20 位以内，但粘贴进来
	// 的历史哈希是 64 位——这列以前只有 32，严格模式下整条 insert 直接失败、
	// 且调用侧不检查错误，等于「最想留档的那次尝试恰好没留档」
	Code       string    `gorm:"size:64" json:"code"`
	Success    bool      `json:"success"`
	FailReason string    `gorm:"size:255" json:"fail_reason"`
	IP         string    `gorm:"size:64" json:"ip"`
	CreatedAt  time.Time `gorm:"index" json:"created_at"`
}

func (RedemptionLog) TableName() string {
	return "redemption_logs"
}
