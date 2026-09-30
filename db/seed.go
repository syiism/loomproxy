package db

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"strings"
	"time"

	"gorm.io/gorm"

	"loomproxy/conf"
	"loomproxy/models"
)

func seedRoles(db *gorm.DB) error {
	roles := []models.Role{
		{Name: "超级管理员", Code: "admin", Description: "拥有所有权限", Status: 1},
		{Name: "普通用户", Code: "user", Description: "普通注册用户", Status: 1},
		{Name: "VIP用户", Code: "vip", Description: "付费VIP用户", Status: 1},
	}

	for _, role := range roles {
		var count int64
		db.Model(&models.Role{}).Where("code = ?", role.Code).Count(&count)
		if count == 0 {
			if err := db.Create(&role).Error; err != nil {
				return err
			}
			log.Printf("Created role: %s", role.Code)
		}
	}
	return nil
}

func seedSettings(db *gorm.DB) error {
	settings := []models.SystemSetting{
		{Key: "site_name", Value: "LoomProxy", Type: "string", Description: "站点名称"},
		{Key: "register_enabled", Value: "true", Type: "bool", Description: "是否允许注册"},
		{Key: "default_role", Value: "user", Type: "string", Description: "新用户默认角色"},
		{Key: "default_quota_plan", Value: "free", Type: "string", Description: "新用户默认额度套餐"},
		{Key: "maintenance_mode", Value: "false", Type: "bool", Description: "维护模式"},
		{Key: "legado_import_url", Value: "", Type: "string", Description: "书源 JSON 直链（个人中心「导入书源」按钮，legado:// 拉起阅读 App）"},
		{Key: "jwt_expire_hours", Value: "168", Type: "number", Description: "登录 token 有效时长（小时），-1 表示永不过期；用户可在个人中心设置自己的时长覆盖"},
		{Key: "proxy_enabled_sources", Value: "", Type: "string", Description: "启用 IP 代理池的数据源/接口，逗号分隔（如 novel_a/chapter 单接口、novel_a 整源）；留空表示不限制（全部走代理）"},
		{Key: "announcement", Value: "", Type: "string", Description: "站内公告（留空=不展示；登录后面板顶部横幅展示，用户可关闭，内容变更后重新展示）"},
		{Key: "verify_code_scenes", Value: "", Type: "string", Description: "启用验证码校验的场景，逗号分隔（register、forgot_password）；留空=全部关闭，业务行为不变"},
		{Key: "verify_send_interval_sec", Value: "60", Type: "number", Description: "验证码发送冷却：同一目标两次发码的最小间隔（秒）"},
		{Key: "verify_daily_send_limit", Value: "10", Type: "number", Description: "验证码每日上限：同一目标/同一 IP 每日发码次数（北京时间零点重置）"},
		{Key: "verify_code_ttl_sec", Value: "600", Type: "number", Description: "验证码有效期（秒）"},
		{Key: "verify_code_max_attempts", Value: "5", Type: "number", Description: "单个验证码最大验证失败次数（超过作废）"},
		{Key: "verify_provider", Value: "mock", Type: "string", Description: "发码通道：mock=仅打印服务端日志（开发默认）；http=通用 HTTP 模板适配器（配 verify_http_* 对接发码平台）"},
		{Key: "verify_http_url", Value: "", Type: "string", Description: "http 通道发码接口地址，支持占位符 {{target}} {{code}} {{scene}}"},
		{Key: "verify_http_method", Value: "POST", Type: "string", Description: "http 通道请求方法"},
		{Key: "verify_http_headers", Value: "", Type: "string", Description: "http 通道请求头（JSON 对象），如 {\"Authorization\":\"Bearer xx\"}"},
		{Key: "verify_http_body", Value: "", Type: "string", Description: "http 通道请求体模板，支持 {{target}} {{code}} {{scene}} 占位符"},
		{Key: "verify_http_success_keyword", Value: "", Type: "string", Description: "http 通道成功判定关键字（响应体需包含，留空=HTTP 2xx 即成功）"},
		{Key: "auto_block_enabled", Value: "false", Type: "bool", Description: "IP 自动拉黑开关：滑动窗口内 403/429 次数达阈值自动加入黑名单（回环地址永不自动拉黑）"},
		{Key: "auto_block_threshold", Value: "30", Type: "number", Description: "IP 自动拉黑阈值：窗口内允许的 403/429 次数上限"},
		{Key: "auto_block_window_sec", Value: "60", Type: "number", Description: "IP 自动拉黑统计窗口（秒）"},
	}

	for _, setting := range settings {
		var count int64
		// MySQL 中 `key` 是保留字，需用反引号包裹
		db.Model(&models.SystemSetting{}).Where("`key` = ?", setting.Key).Count(&count)
		if count == 0 {
			if err := db.Create(&setting).Error; err != nil {
				return err
			}
			log.Printf("Created setting: %s", setting.Key)
		}
	}
	return nil
}

func seedQuotaPlans(db *gorm.DB) error {
	freePlan := models.QuotaPlan{
		Name: "免费版", Code: "free", Description: "免费用户套餐", Level: 0, Status: 1,
	}
	vipPlan := models.QuotaPlan{
		Name: "VIP版", Code: "vip", Description: "VIP用户套餐", Level: 1, Status: 1,
	}
	adminPlan := models.QuotaPlan{
		Name: "管理员版", Code: "admin", Description: "管理员无限制", Level: 2, Status: 1,
	}

	plans := []models.QuotaPlan{freePlan, vipPlan, adminPlan}
	for _, plan := range plans {
		var count int64
		db.Model(&models.QuotaPlan{}).Where("code = ?", plan.Code).Count(&count)
		if count == 0 {
			if err := db.Create(&plan).Error; err != nil {
				return err
			}
			log.Printf("Created quota plan: %s", plan.Code)
		}
	}
	// 存量数据修正：level 全为 0 时按 code 补等级（升级判定依赖）
	db.Model(&models.QuotaPlan{}).Where("code = ? AND level = 0", "vip").Update("level", 1)
	db.Model(&models.QuotaPlan{}).Where("code = ? AND level = 0", "admin").Update("level", 2)

	// 存量数据修正：内置套餐未绑定角色时按 code 补默认绑定（free→user、vip→vip、admin→admin；
	// 仅补 role_id 为空的行，不覆盖管理员手动配置的绑定）
	var roleBindings = map[string]string{"free": "user", "vip": "vip", "admin": "admin"}
	for planCode, roleCode := range roleBindings {
		var role models.Role
		if err := db.Where("code = ?", roleCode).First(&role).Error; err != nil {
			continue
		}
		db.Model(&models.QuotaPlan{}).
			Where("code = ? AND role_id IS NULL", planCode).
			Update("role_id", role.ID)
	}

	var free models.QuotaPlan
	var vip models.QuotaPlan
	var admin models.QuotaPlan
	db.Where("code = ?", "free").First(&free)
	db.Where("code = ?", "vip").First(&vip)
	db.Where("code = ?", "admin").First(&admin)

	now := time.Now()
	// 限额播种：全局 API 限额固定三条；数据源级限额按各源声明（sourceSeedProvider）
	// 动态生成——底座项目无数据源时只有全局限额，接入书源后自动补齐
	planLimits := []struct {
		plan      models.QuotaPlan
		global    int64
		perSource int64
	}{
		{free, 1000, 100},  // 免费版
		{vip, 10000, 1000}, // VIP 版
		{admin, -1, -1},    // 管理员版（不限）
	}
	var limits []models.QuotaLimit
	for _, pl := range planLimits {
		limits = append(limits, models.QuotaLimit{
			PlanID: pl.plan.ID, Scope: "global", Target: "api",
			Limit: pl.global, Period: "day", CreatedAt: now, UpdatedAt: now,
		})
		if sourceSeedProvider == nil {
			continue
		}
		for _, src := range sourceSeedProvider() {
			limits = append(limits, models.QuotaLimit{
				PlanID: pl.plan.ID, Scope: "source", Target: src.Name,
				Limit: pl.perSource, Period: "day", CreatedAt: now, UpdatedAt: now,
			})
		}
	}
	for _, limit := range limits {
		var count int64
		db.Model(&models.QuotaLimit{}).Where("plan_id = ? AND scope = ? AND target = ?", limit.PlanID, limit.Scope, limit.Target).Count(&count)
		if count == 0 {
			if err := db.Create(&limit).Error; err != nil {
				return err
			}
		}
	}

	return nil
}

func seedAdmin(db *gorm.DB) error {
	var adminCount int64
	db.Model(&models.User{}).
		Joins("JOIN user_roles ON user_roles.user_id = users.id").
		Joins("JOIN roles ON roles.id = user_roles.role_id").
		Where("roles.code = ?", "admin").
		Count(&adminCount)
	if adminCount > 0 {
		return nil
	}

	username := strings.TrimSpace(conf.Config.AdminUsername)
	password := conf.Config.AdminPassword

	// 生产环境（启用鉴权）且未配置管理员凭据时，自动生成随机密码
	if password == "" {
		if conf.Config.AuthEnabled {
			password = generateRandomPassword(16)
			log.Printf("WARNING: 未配置 ADMIN_PASSWORD，自动生成随机密码: %s", password)
		} else {
			log.Printf("INFO: 开发模式下未配置管理员密码，跳过创建管理员账号")
			return nil
		}
	}

	if username == "" {
		username = "admin"
		log.Printf("INFO: 未配置 ADMIN_USERNAME，使用默认用户名: %s", username)
	}

	var adminRole models.Role
	if err := db.Where("code = ?", "admin").First(&adminRole).Error; err != nil {
		return err
	}

	var freePlan models.QuotaPlan
	if err := db.Where("code = ?", "free").First(&freePlan).Error; err != nil {
		return err
	}

	user := &models.User{
		Username: strings.ToLower(username),
		Nickname: "Administrator",
		Status:   1,
		PlanID:   &freePlan.ID,
		Roles:    []models.Role{adminRole},
	}
	if err := user.SetPassword(password); err != nil {
		return err
	}
	if err := db.Create(user).Error; err != nil {
		return err
	}

	log.Printf("Created admin user: %s", user.Username)
	return nil
}

func generateRandomPassword(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// fallback
		return "admin" + time.Now().Format("20060102150405")
	}
	return hex.EncodeToString(b)[:n]
}

// SourceSeed 数据源播种声明（app 启动时从 base 注册表注入；db 不反向依赖 base）。
type SourceSeed struct {
	Name         string
	DisplayName  string
	Category     string
	Description  string
	SortOrder    int
	Status       int
	Actions      []string
	LegacyGroups []string // 历史平台组码，用于展开存量按组配置的行
}

var sourceSeedProvider func() []SourceSeed

// SetSourceSeedProvider 注入数据源声明；未注入时数据源相关播种跳过（角色/设置等不受影响）。
func SetSourceSeedProvider(fn func() []SourceSeed) { sourceSeedProvider = fn }

// retiredSources 本部署声明的已下线数据源码（环境变量 RETIRED_SOURCES，逗号分隔，默认空）。
// 底座不携带任何书源实现，也就不知道该清理谁的存量行——清单归部署侧，底座只提供清理机制。
// 历史用量流水（api_call_logs / quota_usage_logs）一律保留不删
func retiredSources() []string {
	if conf.Config == nil {
		return nil
	}
	return conf.Config.RetiredSources
}

// cleanupRemovedSources 清理已下线数据源在各配置表中的存量行（幂等：无行时无操作）。
// data_sources 为软删除模型，用 Unscoped 硬删；套餐-数据源关联按外键先行清理
func cleanupRemovedSources(db *gorm.DB) error {
	removedSources := retiredSources()
	if len(removedSources) == 0 {
		return nil
	}
	var ids []uint
	if err := db.Model(&models.DataSource{}).Where("name IN ?", removedSources).Pluck("id", &ids).Error; err != nil {
		return err
	}
	if len(ids) > 0 {
		if err := db.Where("data_source_id IN ?", ids).Delete(&models.QuotaPlanDataSource{}).Error; err != nil {
			return err
		}
		if err := db.Unscoped().Where("name IN ?", removedSources).Delete(&models.DataSource{}).Error; err != nil {
			return err
		}
		log.Printf("已下线数据源 %v：清理 data_sources 与套餐关联行", removedSources)

		// 存量库的套餐关联可能停留在早期 seed（seedPlanDataSources 仅在关联表
		// 首次为空时播种，后续新增的源不会自动补进套餐），借本次下线清理一并对
		// 三个内置套餐补齐全部存活源的关联（幂等，仅本清理触发时执行一次）
		var plans []models.QuotaPlan
		if err := db.Where("code IN ?", []string{"free", "vip", "admin"}).Find(&plans).Error; err != nil {
			return err
		}
		var sources []models.DataSource
		if err := db.Find(&sources).Error; err != nil {
			return err
		}
		for _, plan := range plans {
			for _, ds := range sources {
				var n int64
				db.Model(&models.QuotaPlanDataSource{}).
					Where("plan_id = ? AND data_source_id = ?", plan.ID, ds.ID).Count(&n)
				if n == 0 {
					if err := db.Create(&models.QuotaPlanDataSource{PlanID: plan.ID, DataSourceID: ds.ID}).Error; err != nil {
						return err
					}
				}
			}
		}
		log.Printf("已为内置套餐补齐 %d 个存活数据源的关联", len(sources))
	}
	if err := db.Where("group_code IN ?", removedSources).Delete(&models.QuotaCost{}).Error; err != nil {
		return err
	}
	if err := db.Where("group_code IN ?", removedSources).Delete(&models.QuotaCostPlan{}).Error; err != nil {
		return err
	}
	if err := db.Where("group_code IN ?", removedSources).Delete(&models.UserQuotaOverride{}).Error; err != nil {
		return err
	}
	if err := db.Where("scope = ? AND target IN ?", "source", removedSources).Delete(&models.QuotaLimit{}).Error; err != nil {
		return err
	}
	if err := db.Where("source_name IN ?", removedSources).Delete(&models.PlatformSourceConfig{}).Error; err != nil {
		return err
	}
	if err := db.Where("source_name IN ?", removedSources).Delete(&models.UserSourceConfig{}).Error; err != nil {
		return err
	}
	return nil
}

// legacyGroupSources 历史平台组码 → 成员数据源。
// 组概念已于 2026-08-09 移除（quota_costs / quota_cost_plans / user_quota_overrides
// 的 group_code 字段统一为数据源码），此映射仅用于存量数据迁移；
// 底座项目的成员源已全部下线，展开为空 = 只清理组行。
// legacyGroupSources 历史平台组码 → 成员数据源，由各源的 LegacyGroups 声明聚合。
// 组概念已于 2026-08-09 移除（quota_costs / quota_cost_plans / user_quota_overrides
// 的 group_code 统一为数据源码），此映射仅用于存量数据迁移。
// 底座不携带源，故无声明即无映射：存量组行会留在库中 inert，由携带源的一侧声明补齐。
func legacyGroupSources() map[string][]string {
	if sourceSeedProvider == nil {
		return nil
	}
	out := map[string][]string{}
	for _, src := range sourceSeedProvider() {
		for _, group := range src.LegacyGroups {
			out[group] = append(out[group], src.Name)
		}
	}
	return out
}

// migrateGroupCostRows 将存量按历史组码配置的行展开为按数据源码的行
// （目标行已存在时保留），随后删除组行。幂等：无组行时无操作。
func migrateGroupCostRows(db *gorm.DB) error {
	for group, sources := range legacyGroupSources() {
		// quota_costs：组行 → 每源一行
		var costs []models.QuotaCost
		if err := db.Where("group_code = ?", group).Find(&costs).Error; err != nil {
			return err
		}
		for _, c := range costs {
			for _, src := range sources {
				var n int64
				db.Model(&models.QuotaCost{}).Where("group_code = ? AND interface = ?", src, c.Interface).Count(&n)
				if n == 0 {
					if err := db.Create(&models.QuotaCost{
						GroupCode: src, Interface: c.Interface,
						Cost: c.Cost, Status: c.Status, Interval: c.Interval,
						LimitCount: c.LimitCount, WindowSec: c.WindowSec,
					}).Error; err != nil {
						return err
					}
				}
			}
		}
		if len(costs) > 0 {
			if err := db.Where("group_code = ?", group).Delete(&models.QuotaCost{}).Error; err != nil {
				return err
			}
			log.Printf("已将 quota_costs 组级配置 %s 展开为数据源级（%d 个源）", group, len(sources))
		}

		// quota_cost_plans：按 (plan_id, interface) 展开
		var planCosts []models.QuotaCostPlan
		if err := db.Where("group_code = ?", group).Find(&planCosts).Error; err != nil {
			return err
		}
		for _, pc := range planCosts {
			for _, src := range sources {
				var n int64
				db.Model(&models.QuotaCostPlan{}).
					Where("plan_id = ? AND group_code = ? AND interface = ?", pc.PlanID, src, pc.Interface).Count(&n)
				if n == 0 {
					if err := db.Create(&models.QuotaCostPlan{
						PlanID: pc.PlanID, GroupCode: src, Interface: pc.Interface, Interval: pc.Interval,
						LimitCount: pc.LimitCount, WindowSec: pc.WindowSec,
					}).Error; err != nil {
						return err
					}
				}
			}
		}
		if len(planCosts) > 0 {
			if err := db.Where("group_code = ?", group).Delete(&models.QuotaCostPlan{}).Error; err != nil {
				return err
			}
			log.Printf("已将 quota_cost_plans 组级配置 %s 展开为数据源级（%d 个源）", group, len(sources))
		}

		// user_quota_overrides：按 user_id 展开
		var overrides []models.UserQuotaOverride
		if err := db.Where("group_code = ?", group).Find(&overrides).Error; err != nil {
			return err
		}
		for _, o := range overrides {
			for _, src := range sources {
				var n int64
				db.Model(&models.UserQuotaOverride{}).
					Where("user_id = ? AND group_code = ?", o.UserID, src).Count(&n)
				if n == 0 {
					if err := db.Create(&models.UserQuotaOverride{
						UserID: o.UserID, GroupCode: src, Limit: o.Limit,
					}).Error; err != nil {
						return err
					}
				}
			}
		}
		if len(overrides) > 0 {
			if err := db.Where("group_code = ?", group).Delete(&models.UserQuotaOverride{}).Error; err != nil {
				return err
			}
			log.Printf("已将 user_quota_overrides 组级覆盖 %s 展开为数据源级（%d 个源）", group, len(sources))
		}
	}
	return nil
}

func seedQuotaCosts(db *gorm.DB) error {
	if sourceSeedProvider == nil {
		return nil
	}
	// 按各源声明的动作集播种（recommend 沿用历史语义：cost=0 不计费）
	for _, src := range sourceSeedProvider() {
		for _, iface := range src.Actions {
			var count int64
			db.Model(&models.QuotaCost{}).Where("group_code = ? AND interface = ?", src.Name, iface).Count(&count)
			if count == 0 {
				cost := int64(1)
				if iface == "recommend" {
					cost = 0
				}
				if err := db.Create(&models.QuotaCost{
					GroupCode: src.Name,
					Interface: iface,
					Cost:      cost,
					Status:    1,
				}).Error; err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func seedDataSources(db *gorm.DB) error {
	if sourceSeedProvider == nil {
		log.Println("未注入数据源声明（SetSourceSeedProvider），跳过 data_sources 播种")
		return nil
	}
	for _, ds := range sourceSeedProvider() {
		var count int64
		db.Model(&models.DataSource{}).Where("name = ?", ds.Name).Count(&count)
		if count == 0 {
			if err := db.Create(&models.DataSource{
				Name:        ds.Name,
				DisplayName: ds.DisplayName,
				Category:    ds.Category,
				Description: ds.Description,
				Status:      ds.Status,
				SortOrder:   ds.SortOrder,
			}).Error; err != nil {
				return err
			}
			log.Printf("Created data source: %s", ds.Name)
		}
	}
	return nil
}

func seedPlanDataSources(db *gorm.DB) error {
	// 只在 QuotaPlanDataSource 表首次为空时初始化（不覆盖用户手动修改）
	var count int64
	db.Model(&models.QuotaPlanDataSource{}).Count(&count)
	if count > 0 {
		return nil
	}
	if sourceSeedProvider == nil {
		return nil
	}

	// 获取套餐和数据源
	var freePlan, vipPlan, adminPlan models.QuotaPlan
	db.Where("code = ?", "free").First(&freePlan)
	db.Where("code = ?", "vip").First(&vipPlan)
	db.Where("code = ?", "admin").First(&adminPlan)

	var dataSources []models.DataSource
	db.Find(&dataSources)
	dsMap := make(map[string]uint)
	for _, ds := range dataSources {
		dsMap[ds.Name] = ds.ID
	}

	// 三个内置套餐均关联全部声明数据源（免费/会员/管理员同构）
	plans := []models.QuotaPlan{freePlan, vipPlan, adminPlan}
	for _, plan := range plans {
		for _, s := range sourceSeedProvider() {
			if dsID, ok := dsMap[s.Name]; ok {
				db.Create(&models.QuotaPlanDataSource{PlanID: plan.ID, DataSourceID: dsID})
			}
		}
	}
	return nil
}

func Seed(db *gorm.DB) error {
	if err := cleanupRemovedSources(db); err != nil {
		return err
	}
	if err := seedRoles(db); err != nil {
		return err
	}
	if err := seedSettings(db); err != nil {
		return err
	}
	if err := seedQuotaPlans(db); err != nil {
		return err
	}
	if err := migrateGroupCostRows(db); err != nil {
		return err
	}
	if err := seedQuotaCosts(db); err != nil {
		return err
	}
	if err := seedDataSources(db); err != nil {
		return err
	}
	if err := seedPlanDataSources(db); err != nil {
		return err
	}
	if err := seedAdmin(db); err != nil {
		return err
	}
	return nil
}
