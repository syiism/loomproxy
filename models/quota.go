package models

import (
	"time"

	"gorm.io/gorm"
)

type QuotaPlan struct {
	ID          uint           `gorm:"primaryKey" json:"id"`
	Name        string         `gorm:"size:64;uniqueIndex;not null" json:"name"`
	Code        string         `gorm:"size:32;uniqueIndex;not null" json:"code"`
	Description string         `gorm:"size:255" json:"description"`
	Level       int            `gorm:"default:0" json:"level"` // 套餐等级：0=free, 1=vip…，升级/降级判定
	Price       int            `gorm:"default:0" json:"price"` // 展示价格（分），仅展示
	RoleID      *uint          `gorm:"index" json:"role_id"`   // 绑定角色：用户套餐变更时自动切换为该角色；NULL=不绑定
	Role        *Role          `gorm:"foreignKey:RoleID" json:"role,omitempty"`
	Status      int            `gorm:"default:1" json:"status"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `gorm:"index" json:"-"`
}

type QuotaLimit struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	PlanID    uint      `gorm:"index;not null" json:"plan_id"`
	Scope     string    `gorm:"size:32;not null" json:"scope"`
	Target    string    `gorm:"size:128;not null" json:"target"`
	Limit     int64     `gorm:"default:0" json:"limit"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (QuotaLimit) TableName() string {
	return "quota_limits"
}

type UserQuota struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	UserID      uint      `gorm:"index;not null" json:"user_id"`
	PlanID      uint      `gorm:"index;not null" json:"plan_id"`
	Used        int64     `gorm:"default:0" json:"used"`
	PeriodStart time.Time `json:"period_start"`
	PeriodEnd   time.Time `json:"period_end"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (UserQuota) TableName() string {
	return "user_quotas"
}

type UserQuotaOverride struct {
	ID     uint `gorm:"primaryKey" json:"id"`
	UserID uint `gorm:"uniqueIndex:idx_uqo_user_group;not null" json:"user_id"`
	// GroupCode 历史字段名：存数据源码（组级覆盖已随组概念移除）
	GroupCode string    `gorm:"size:32;uniqueIndex:idx_uqo_user_group;not null" json:"group_code"`
	Limit     int64     `gorm:"default:0" json:"limit"` // >0=指定额度, 0=删除覆盖回退计划, -1=不限
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (UserQuotaOverride) TableName() string {
	return "user_quota_overrides"
}

// UserSourceConfig 用户个人数据源配置（如 baseUrl）
type UserSourceConfig struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	UserID     uint      `gorm:"uniqueIndex:idx_usc_user_source;not null" json:"user_id"`
	SourceName string    `gorm:"size:64;uniqueIndex:idx_usc_user_source;not null" json:"source_name"`
	BaseURL    string    `gorm:"size:512" json:"base_url"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (UserSourceConfig) TableName() string {
	return "user_source_configs"
}

// PlatformSourceConfig 平台级数据源默认配置
type PlatformSourceConfig struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	SourceName string    `gorm:"size:64;uniqueIndex;not null" json:"source_name"`
	BaseURL    string    `gorm:"size:512" json:"base_url"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (PlatformSourceConfig) TableName() string {
	return "platform_source_configs"
}

// QuotaCost 数据源接口消耗/状态/全局限流配置
type QuotaCost struct {
	ID uint `gorm:"primaryKey" json:"id"`
	// GroupCode 历史字段名：组概念移除后统一存数据源码
	GroupCode string `gorm:"size:32;uniqueIndex:idx_gp_iface;not null" json:"group_code"`
	Interface string `gorm:"size:64;uniqueIndex:idx_gp_iface;not null" json:"interface"`
	// Cost 带 gorm default 标签的整型字段有个坑：**值为零时 GORM 会省略该列，让 DB 默认值生效**。
	// 原来这里是 default:1，于是 `Create(&QuotaCost{Cost: 0})`（播种免费动作、面板填 0）
	// 全部静默变成 1 点——「写了 0 拿到 1」，生产上 recommend 一类行 cost 混合就是这么来的。
	// 默认档定成 0（新接口默认不计费，要不要收费由人或播种显式写 1），语义与代码里的默认一致。
	Cost       int64     `gorm:"default:0" json:"cost"`
	Status     int       `gorm:"default:1" json:"status"`
	Interval   int64     `gorm:"default:0" json:"interval"`    // 速率限制间隔(秒)：0=不限，>0=每隔N秒允许1次请求（不可突发）
	LimitCount int64     `gorm:"default:0" json:"limit_count"` // 窗口计数限流：窗口内最多 N 次（允许突发），0=不限；与 Interval 同时配置时本字段优先
	WindowSec  int64     `gorm:"default:60" json:"window_sec"` // 窗口计数限流的窗口长度(秒)，<=0 时按 60 处理
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (QuotaCost) TableName() string {
	return "quota_costs"
}

// QuotaCostPlan 套餐级别接口速率限制（覆盖全局 QuotaCost.Interval）
// GroupCode 历史字段名：存数据源码（管理面板「接口管理」按数据源保存）
type QuotaCostPlan struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	PlanID     uint      `gorm:"uniqueIndex:idx_qcp_plan_group_iface;not null" json:"plan_id"`
	GroupCode  string    `gorm:"size:32;uniqueIndex:idx_qcp_plan_group_iface;not null" json:"group_code"`
	Interface  string    `gorm:"size:64;uniqueIndex:idx_qcp_plan_group_iface;not null" json:"interface"`
	Interval   int64     `gorm:"default:0" json:"interval"`    // 0=不限，>0=每隔N秒允许1次请求（不可突发）
	LimitCount int64     `gorm:"default:0" json:"limit_count"` // 窗口计数限流：窗口内最多 N 次（允许突发），0=不限；与 Interval 同时配置时本字段优先
	WindowSec  int64     `gorm:"default:60" json:"window_sec"` // 窗口计数限流的窗口长度(秒)，<=0 时按 60 处理
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (QuotaCostPlan) TableName() string {
	return "quota_cost_plans"
}

// QuotaUsageLog 额度使用流水（只追加，不更新）；GroupCode 历史字段名：存数据源码
type QuotaUsageLog struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `gorm:"index:idx_usage_user_time,priority:1;not null" json:"user_id"`
	GroupCode string    `gorm:"size:32;index;not null" json:"group_code"`
	Interface string    `gorm:"size:64;not null" json:"interface"`
	Cost      int64     `gorm:"default:0" json:"cost"`
	CreatedAt time.Time `gorm:"index:idx_usage_user_time,priority:2" json:"created_at"`
}

func (QuotaUsageLog) TableName() string {
	return "quota_usage_logs"
}

// DataSource 数据源定义（管理员可启用/禁用、配置元数据）
type DataSource struct {
	ID          uint           `gorm:"primaryKey" json:"id"`
	Name        string         `gorm:"size:64;uniqueIndex;not null" json:"name"` // 唯一标识（路由前缀 / quota group_code）
	DisplayName string         `gorm:"size:64;not null" json:"display_name"`     // 显示名称
	Category    string         `gorm:"size:32;not null" json:"category"`         // 分类（数据源自述，同时作为 data/ 下的文件目录名）
	Description string         `gorm:"size:255" json:"description"`              // 描述
	Status      int            `gorm:"default:1" json:"status"`                  // 1=启用，0=禁用
	SortOrder   int            `gorm:"default:0" json:"sort_order"`              // 排序
	GroupID     *uint          `gorm:"index" json:"group_id"`                    // 所属分组，NULL=未分组（一源至多一组，见 SourceGroup）
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `gorm:"index" json:"-"`
}

func (DataSource) TableName() string {
	return "data_sources"
}

// QuotaPlanDataSource 套餐-数据源关联。**已弃用（待办清单 P34，方案 A）**：
// 「哪个套餐包含哪个源」现在就是 quota_limits 里那行 scope=source 的限额，两处记录同一份事实
// 就会两处都能被单独改一遍。**不再被写、也不在 AutoMigrate 清单里**（全新库不建它），
// 生产已经 DROP；类型与 `db/seed.go:alignPlanGrants` 只为「从 P34 之前的库升上来」的存量迁移保留，
// 那条路径按「表在不在」自我开关——留着它是因为对老库来说删掉等于把所有授权抹掉。
type QuotaPlanDataSource struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	PlanID       uint      `gorm:"uniqueIndex:idx_plan_source;not null" json:"plan_id"`
	DataSourceID uint      `gorm:"uniqueIndex:idx_plan_source;not null" json:"data_source_id"`
	CreatedAt    time.Time `json:"created_at"`
}

func (QuotaPlanDataSource) TableName() string {
	return "quota_plan_data_sources"
}

// SourceGroup 数据源分组：管理员自建的**归类与筛选视图**（相当于给数据源打标签），
// 不参与额度、计费、限速的解析——那些口径的键一律仍是数据源码。
// 与被移除的历史「平台组」（表 quota_source_groups，曾是 quota_costs / quota_cost_plans /
// user_quota_overrides 的 group_code 键）无关。
// 一源至多一组（DataSource.GroupID），删组不删源；按组批量套用限额只是**写入时的批量入口**，
// 落库仍是每源一行 quota_limits。
type SourceGroup struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Name        string    `gorm:"size:64;uniqueIndex;not null" json:"name"`
	Description string    `gorm:"size:255" json:"description"`
	Status      int       `gorm:"default:1" json:"status"` // 1=启用，0=停用：停用只在展示侧（首页分节/筛选/下游清单）视作未分组，不影响访问控制与计费
	SortOrder   int       `gorm:"default:0" json:"sort_order"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (SourceGroup) TableName() string {
	return "source_groups"
}
