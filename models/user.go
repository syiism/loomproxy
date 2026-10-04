package models

import (
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type User struct {
	ID                uint       `gorm:"primaryKey" json:"id"`
	Username          string     `gorm:"size:64;uniqueIndex;not null" json:"username"`
	Email             string     `gorm:"size:128;uniqueIndex;not null" json:"email"`
	PasswordHash      string     `gorm:"size:255;not null" json:"-"`
	Nickname          string     `gorm:"size:64" json:"nickname"`
	Avatar            string     `gorm:"size:512" json:"avatar"`
	Status            int        `gorm:"default:1" json:"status"`
	Roles             []Role     `gorm:"many2many:user_roles;" json:"roles,omitempty"`
	PlanID            *uint      `gorm:"index" json:"plan_id"`
	Plan              *QuotaPlan `gorm:"foreignKey:PlanID" json:"plan,omitempty"`
	PlanExpireAt      *time.Time `json:"plan_expire_at"` // 套餐到期时间；NULL=永久（存量用户兼容）
	LastLoginAt       *time.Time `json:"last_login_at"`
	UsernameChangedAt *time.Time `json:"username_changed_at"` // 上次修改用户名时间；NULL=从未修改（用户名每 30 天限改一次）
	TokenExpireHours  *int       `json:"token_expire_hours"`  // 个人登录 token 有效时长（小时）；NULL=跟随系统设置，对下次及以后登录生效
	// QuotaResetAt 单日额度的起算点覆盖（管理员「刷新额度」写入，待办清单 P41）。
	// **NULL = 从未刷新**，起算点就是当日零点；非空时取「零点」与「此刻」里较晚的那个，
	// 于是刷新之后的消耗重新计，而零点之后它自然失效（新的一天本来就从零开始）。
	// 刻意不删也不冲正 quota_usage_logs：那张表是只追加的流水，
	// 删行等于毁掉「今天到底用了多少」的证据，写负数行等于在总和里掺假账——
	// 需要改的是**起算点**这一份事实，而且它只有一处定义（gate.UsageSince）。
	QuotaResetAt *time.Time `json:"quota_reset_at"`
	// ContentConsent 是否同意网关留存「搜索词与阅读记录」（监控明细的内容维度）。
	// **NULL = 从未表态 = 同意**：默认档写在这里，升级前的存量用户读出来就是同意，
	// 不需要一次回填写数据（那是生产库写操作）。显式 false 才是不同意。
	// 只有本人能改，且走会话 only 的 /auth/me（API Key 改不动自己的同意位）——
	// 同意位是「谁授权网关留」的记录，让长期密钥能改它等于把授权来源搞混。
	ContentConsent *bool          `gorm:"column:content_consent" json:"-"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	DeletedAt      gorm.DeletedAt `gorm:"index" json:"-"`
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

// KeepsContentData 内容维度同意位的**有效值**：没表态（NULL）按默认档读成「同意」。
// 判据收成这一个方法，是因为「NULL 意味着什么」有三处要问它（monitor 的采集闸口、
// 面板下发、用例断言）——写在三处字面量判断里，早晚会有一处写成 `!= nil` 反了向。
func (u *User) KeepsContentData() bool {
	if u == nil || u.ContentConsent == nil {
		return true
	}
	return *u.ContentConsent
}

// Public 组装面向端点的用户视图。
//
// aliases 是本人的显示别名（`db.DisplayAliasesFor` / `DisplayAliasesForUsers` 读出来传进来），
// 键格式见 `DisplayAliasKey`。**签名收这个参数是刻意的**：别名规则只写在 `displayPair` 一处，
// 而少传一个参数就编译不过——否则总有某个端点会忘记带上，用户看到「一半界面改了、一半没改」。
//
// 每个 plan/role 同时下发三个值，谁看谁负责：
//   - `name`：默认名（全局那份事实，管理员视图以它为准）
//   - `alias`：本人起的别名，没有就是空串（管理员视图把它作为**第二栏**显示，不替换默认名）
//   - `display_name`：别名优先、否则默认名（本人界面只看这一个）
func (u *User) Public(aliases map[string]string) map[string]interface{} {
	seen := make(map[uint]bool)
	var roles []map[string]interface{}
	for _, r := range u.Roles {
		if seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		alias, display := displayPair(aliases, DisplayKindRole, r.ID, r.Name)
		roles = append(roles, map[string]interface{}{
			"id":           r.ID,
			"code":         r.Code,
			"name":         r.Name,
			"alias":        alias,
			"display_name": display,
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
		// 下发的是**有效值**而不是原始指针：面板不该需要知道「NULL 算什么」这条规则
		"content_consent": u.KeepsContentData(),
		"created_at":      u.CreatedAt,
	}
	if u.Plan != nil {
		alias, display := displayPair(aliases, DisplayKindPlan, u.Plan.ID, u.Plan.Name)
		result["plan"] = map[string]interface{}{
			// id 必须下发：设别名要按 (kind, target_id) 定位，缺 id 本人界面无从下手
			"id":           u.Plan.ID,
			"code":         u.Plan.Code,
			"name":         u.Plan.Name,
			"alias":        alias,
			"display_name": display,
		}
	}
	return result
}

// DisplayAlias 显示名 = 别名优先、否则默认名。**规则只有这一处**（`Public()` 与 dashboard 都走它），
// 否则「个人中心改了别名、首页没改」就是这样长出来的。
func DisplayAlias(aliases map[string]string, kind string, targetID uint, fallback string) string {
	if a := aliases[DisplayAliasKey(kind, targetID)]; a != "" {
		return a
	}
	return fallback
}

// displayPair 别名 → (原始别名, 显示名)。原始别名空串表示没改过，管理员视图要的就是这个空串
// （它据此决定「要不要挂第二栏」，所以不能在这里替它回退成默认名）。
func displayPair(aliases map[string]string, kind string, targetID uint, fallback string) (string, string) {
	alias := aliases[DisplayAliasKey(kind, targetID)]
	return alias, DisplayAlias(aliases, kind, targetID, fallback)
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
