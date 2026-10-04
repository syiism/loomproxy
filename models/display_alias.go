package models

import (
	"strconv"
	"time"
)

// UserDisplayAlias 用户对「套餐名 / 角色名」的**显示别名**（待办清单 P43）。
//
// 默认名在 `quota_plans.name` / `roles.name`，两张表都带 `uniqueIndex`——那是所有人共用的一份事实，
// 就地改名等于替每个用户、每张卡密批次、每份报表改称呼。所以这里只存「这个人希望它显示成什么」。
//
// 键是 `(user_id, kind, target_id)` 而不是挂在人身上的一列：别名跟着**那一份套餐**走，
// 用户从 A 换到 B 之后，A 的别名不会被读到（也永远不会在某个时刻串到 B 上）。
// 旧行留在库里不清理是刻意的——用户换回原套餐时称呼还在，而"清理孤儿行"会多出一个要解释的定时任务。
//
// `Alias` 允许重复（不加唯一索引）：两个用户把同一个套餐叫成同一个名字不是冲突，是巧合。
type UserDisplayAlias struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `gorm:"index:idx_display_alias_unique,unique;not null" json:"user_id"`
	Kind      string    `gorm:"index:idx_display_alias_unique,unique;size:16;not null" json:"kind"` // plan / role
	TargetID  uint      `gorm:"index:idx_display_alias_unique,unique;not null" json:"target_id"`
	Alias     string    `gorm:"size:96;not null" json:"alias"` // 上限 32 个 rune（中文按 3 字节算），见 handlers/auth 的清洗
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// DisplayAliasKey 别名表的键格式——**只在这里定义一次**：
// 读的一侧（models.User.Public）与写的一侧（UpdateDisplayAlias）都拼不出第二套形状。
func DisplayAliasKey(kind string, targetID uint) string {
	return kind + ":" + strconv.FormatUint(uint64(targetID), 10)
}

// 别名种类（写端点按这两个值校验，别的直接拒）
const (
	DisplayKindPlan = "plan"
	DisplayKindRole = "role"
)

// AliasMaxRunes 别名长度上限（rune 数，不是字节数）
const AliasMaxRunes = 32
