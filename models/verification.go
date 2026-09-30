package models

import (
	"time"
)

// VerificationCode 验证码（场景化：注册/找回密码等，后期可扩展登录等场景）。
// 只存哈希不存明文（与卡密同策略）；验证通过由业务侧显式消费，失败计次达上限作废。
// 同一 (scene,target) 保留发码历史行（限频按行统计），验证时取最新未消费行。
type VerificationCode struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// Scene 场景：register / forgot_password（预留 login 等）
	Scene string `gorm:"size:32;index:idx_vc_scene_target,priority:1" json:"scene"`
	// Target 接收目标：邮箱（小写）/ 手机号
	Target     string     `gorm:"size:128;index:idx_vc_scene_target,priority:2;index:idx_vc_target_time,priority:1" json:"target"`
	CodeHash   string     `gorm:"size:64;not null" json:"-"`
	Attempts   int        `gorm:"default:0" json:"attempts"`                         // 验证失败次数（达上限作废）
	IP         string     `gorm:"size:64;index:idx_vc_ip_time,priority:1" json:"ip"` // 发码请求来源 IP（审计 + 限频）
	ExpiresAt  time.Time  `json:"expires_at"`
	ConsumedAt *time.Time `json:"consumed_at"` // 非空=已消费（一次性）
	CreatedAt  time.Time  `gorm:"index:idx_vc_target_time,priority:2;index:idx_vc_ip_time,priority:2" json:"created_at"`
}

func (VerificationCode) TableName() string {
	return "verification_codes"
}
