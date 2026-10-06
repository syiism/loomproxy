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
	// QuotaCycleMode 单日额度的清零钟点模式：`day`=平台时区自然日 0 点（默认），
	// `subscription`=本人**注册时刻那个钟点**（每 24h 一轮，见 utils.DailyAnchorStart）。
	// **只移起算点，不动限额数字**——限额仍然是「/ 日」，P70 那条「额度只有一个窗口：当日」不被推翻。
	// 默认值写在这一处就是唯一默认处，且默认**不是新行为**（P85：默认方向不许是改动后的那一头）；
	// AutoMigrate 给存量行补的也是 `day`，所以升级前后所有人口径一字不变，不需要一次回填写库。
	QuotaCycleMode string `gorm:"size:16;default:day" json:"quota_cycle_mode"`
	// QuotaCycleChangedAt 本人上一次切换模式的时刻；NULL=从未切换。**它是那条端点的限频依据**（30 天一次，
	// 与 UsernameChangedAt 同一个形状理由）——不限频就是白送刷新：起算点一旦后移，
	// 前一段用量就不在窗口里了，而 UsedToday 正是按起算点求和的。
	QuotaCycleChangedAt *time.Time `json:"quota_cycle_changed_at"`
	// ContentConsent 是否同意网关留存「搜索词与阅读记录」（监控明细的内容维度）。
	// **NULL = 从未表态 = 同意**：默认档写在这里，升级前的存量用户读出来就是同意，
	// 不需要一次回填写数据（那是生产库写操作）。显式 false 才是不同意。
	// 只有本人能改，且走会话 only 的 /auth/me（API Key 改不动自己的同意位）——
	// 同意位是「谁授权网关留」的记录，让长期密钥能改它等于把授权来源搞混。
	ContentConsent *bool `gorm:"column:content_consent" json:"-"`
	// ContentConsentSetAt 同意位**有效值真变**的时刻（待办清单 P52）。
	// 存在的理由很窄：判断「用户关掉留存之后还有没有新行被捕获」需要一个时间点，
	// 而 `UpdatedAt` 会被任何一次改昵称/邮箱顶掉——用它定案纯靠"那个用户当天只动过这一次"的时间轴巧合。
	// **只在有效值变化时盖**（NULL 与显式 true 在 `KeepsContentData` 眼里是同一个状态，
	// 给没变的状态盖时刻等于让这列说假话）；NULL = 从来没有真变过（含升级前的存量用户）。
	// 判据同 P41 的 `quota_reset_at`：**被判定读取的时间点要有自己的列**，不借别人的列。
	ContentConsentSetAt *time.Time     `gorm:"column:content_consent_set_at" json:"-"`
	CreatedAt           time.Time      `json:"created_at"`
	UpdatedAt           time.Time      `json:"updated_at"`
	DeletedAt           gorm.DeletedAt `gorm:"index" json:"-"`
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
		// 额度清零口径下发的是**有效值**（空串归一成 day）与算好的可切换时刻：
		// 面板不许自己抄那 30 天。钟点那一格由读侧用 utils.ClockHHMM 补——models 不能反向依赖 utils
		// （`utils/auth.go` 已经在用 models，倒过来就是环），这条边界是被编译器教的，不是设计的。
		"quota_cycle_mode":           u.EffectiveQuotaCycleMode(),
		"quota_cycle_next_switch_at": u.QuotaCycleNextSwitchAt(),
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

// 额度清零模式的两个合法值（写口与判定读的是同一组常量，不在两处写字面量）。
const (
	QuotaCycleDay          = "day"
	QuotaCycleSubscription = "subscription"
)

// QuotaCycleSwitchInterval 是本人切换清零模式的冷却长度（30 天，与「用户名每 30 天限改一次」同族）。
// 为什么必须限频：切换**不改流水、只移起算点**，而起算点一旦后移，之前那段用量就不在窗口里了——
// UsedToday 正是按起算点求和的。不限频的话「来回切两次」等于免费把当天额度清空一次，
// 而监控与流水看不出任何异常。
const QuotaCycleSwitchInterval = 30 * 24 * time.Hour

// QuotaCycleNextSwitchAt 下一次允许切换的时刻；nil = 现在就能切（含从未切换的存量用户）。
// 算法只在这一处：面板要显示「何时可再切」、端点要拒绝、用例要钉边界——三处必须同一个数。
func (u *User) QuotaCycleNextSwitchAt() *time.Time {
	if u == nil || u.QuotaCycleChangedAt == nil {
		return nil
	}
	next := u.QuotaCycleChangedAt.Add(QuotaCycleSwitchInterval)
	if time.Now().Before(next) {
		return &next
	}
	return nil
}

// IsQuotaCycleMode 校验写入口收到的值。**空串不是合法值**——它是 AutoMigrate 之前那批行的形状，
// 读的一侧由 EffectiveQuotaCycleMode 归一成 day，写的一侧必须显式给两个词之一。
func IsQuotaCycleMode(v string) bool {
	return v == QuotaCycleDay || v == QuotaCycleSubscription
}

// EffectiveQuotaCycleMode 读「这个人实际按哪个钟点清零」：**空值归一到 day**。
// 归一必须只有一个地方做——判定与面板各归一次，就会在新增第三种模式时漏掉一处。
func (u *User) EffectiveQuotaCycleMode() string {
	if u == nil {
		return QuotaCycleDay
	}
	if u.QuotaCycleMode == QuotaCycleSubscription {
		return QuotaCycleSubscription
	}
	return QuotaCycleDay
}
