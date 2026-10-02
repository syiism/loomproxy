package models

import (
	"time"

	"gorm.io/gorm"
)

// PoolDevice 号池中的通用「号」记录。
// 池框架（base/pool）只关心冷热状态与额度台账，具体凭证（设备 sn、账号 cookie 等）
// 由各数据源的 Provider 通过 Attrs 自行编解码。
// Attrs 是**扁平字符串表**（map[string]string 的 JSON）；装不下的嵌套凭证
// （如设备会话：device 对象 + cookies 对象）放 Payload——原样存一段 JSON、由源解释，
// 框架只在快照里列它的第一层键名。别把嵌套塞进 Attrs 的某个键：
// decodeAttrs 对形态不符的值是静默丢弃，症状会是「号在库里但凭证为空」。
type PoolDevice struct {
	ID         uint           `gorm:"primaryKey"`
	Pool       string         `gorm:"size:64;not null;uniqueIndex:idx_pool_device" json:"pool"`   // 池名（通常数据源码）
	Ident      string         `gorm:"size:128;not null;uniqueIndex:idx_pool_device" json:"ident"` // 池内唯一标识
	Attrs      string         `gorm:"type:text" json:"-"`                                         // JSON 附加凭证（扁平），Provider 解释
	Payload    string         `gorm:"type:text" json:"-"`                                         // 嵌套凭证载荷（原样 JSON，见上）
	TotalQuota int            `json:"total_quota"`                                                // 周期内可领取次数上限
	UsedQuota  int            `json:"used_quota"`                                                 // 周期内已领取次数
	ExpireAt   *time.Time     `json:"expire_at"`                                                  // 已领取资源的到期时间，nil=无
	Status     string         `gorm:"size:16;index;default:cold" json:"status"`
	Note       string         `gorm:"size:255" json:"note,omitempty"` // 进入当前状态的原因（spread 池的冷却原因走这里）
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
	DeletedAt  gorm.DeletedAt `gorm:"index" json:"deleted_at,omitempty"`
}
