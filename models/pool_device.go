package models

import (
	"time"

	"gorm.io/gorm"
)

// PoolDevice 号池中的通用「号」记录。
// 池框架（base/pool）只关心冷热状态与额度台账，具体凭证（设备 sn、账号 cookie 等）
// 由各数据源的 Provider 通过 Attrs 自行编解码。
type PoolDevice struct {
	ID         uint           `gorm:"primaryKey"`
	Pool       string         `gorm:"size:64;not null;uniqueIndex:idx_pool_device" json:"pool"`   // 池名（通常数据源码）
	Ident      string         `gorm:"size:128;not null;uniqueIndex:idx_pool_device" json:"ident"` // 池内唯一标识
	Attrs      string         `gorm:"type:text" json:"-"`                                         // JSON 附加凭证，Provider 解释
	TotalQuota int            `json:"total_quota"`                                                // 周期内可领取次数上限
	UsedQuota  int            `json:"used_quota"`                                                 // 周期内已领取次数
	ExpireAt   *time.Time     `json:"expire_at"`                                                  // 已领取资源的到期时间，nil=无
	Status     string         `gorm:"size:16;index;default:cold" json:"status"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
	DeletedAt  gorm.DeletedAt `gorm:"index" json:"deleted_at,omitempty"`
}
