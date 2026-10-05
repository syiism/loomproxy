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
		{Key: "register_enabled", Value: "true", Type: "bool", Description: "是否允许注册"},
		{Key: "default_role", Value: "user", Type: "string", Description: "新用户默认角色"},
		{Key: "default_quota_plan", Value: "free", Type: "string", Description: "新用户默认额度套餐"},
		{Key: "maintenance_mode", Value: "false", Type: "bool", Description: "维护模式"},
		{Key: "jwt_expire_hours", Value: "168", Type: "number", Description: "登录 token 有效时长（小时），-1 表示永不过期；用户可在个人中心设置自己的时长覆盖"},
		{Key: "proxy_enabled_sources", Value: "", Type: "string", Description: "启用 IP 代理池的数据源/接口，逗号分隔（如 novel_a/chapter 单接口、novel_a 整源）；留空表示不限制（全部走代理）"},
		{Key: "rank_public_sources", Value: "", Type: "string", Description: "允许普通用户查看排行榜的数据源，逗号分隔的数据源码；留空=不对普通用户开放任何榜单。管理员不受此名单限制（面板也可在「数据源列表」逐源开关）"},
		{Key: "announcement", Value: "", Type: "string", Description: "站内公告（留空=不展示；登录后面板顶部横幅展示，用户可关闭，内容变更后重新展示）"},
		{Key: "verify_code_scenes", Value: "", Type: "string", Description: "启用验证码校验的场景，逗号分隔（register、forgot_password）；留空=全部关闭，业务行为不变"},
		{Key: "verify_send_interval_sec", Value: "60", Type: "number", Description: "验证码发送冷却：同一目标两次发码的最小间隔（秒）"},
		{Key: "verify_daily_send_limit", Value: "10", Type: "number", Description: "验证码每日上限：同一目标/同一 IP 每日发码次数（北京时间零点重置）"},
		{Key: "verify_code_ttl_sec", Value: "600", Type: "number", Description: "验证码有效期（秒）"},
		{Key: "verify_code_max_attempts", Value: "5", Type: "number", Description: "单个验证码最大验证失败次数（超过作废）"},
		{Key: "verify_provider", Value: "mock", Type: "string", Description: "发码通道：mock=仅打印服务端日志（开发默认）；http=通用 HTTP 模板适配器（配 verify_http_* 对接发码平台）"},
		{Key: "verify_http_url", Value: "", Type: "string", Description: "http 通道发码接口地址，支持占位符 {{target}} {{code}} {{scene}}"},
		{Key: "verify_http_method", Value: "POST", Type: "string", Description: "http 通道请求方法"},
		{Key: "verify_http_headers", Value: "", Type: "json_object", Description: "http 通道请求头（必须是字符串到字符串的 JSON 对象，保存时校验并压成单行），如 {\"authorization\":\"Bearer xx\",\"content-type\":\"application/json\"}；数组、裸字符串、值写成数字都会被发送端拿不到，这一档当场拒（P94）"},
		{Key: "verify_http_body", Value: "", Type: "json", Description: "http 通道请求体模板（JSON，保存时校验并压成单行），支持 {{target}} {{code}} {{scene}} 占位符"},
		{Key: "verify_http_success_keyword", Value: "", Type: "string", Description: "http 通道成功判定关键字（响应体需包含，留空=HTTP 2xx 即成功）"},
		{Key: "auto_block_enabled", Value: "false", Type: "bool", Description: "IP 自动拉黑开关：滑动窗口内 403/429 次数达阈值自动加入黑名单（回环地址永不自动拉黑）"},
		{Key: "auto_block_threshold", Value: "30", Type: "number", Description: "IP 自动拉黑阈值：窗口内允许的 403/429 次数上限"},
		{Key: "auto_block_window_sec", Value: "60", Type: "number", Description: "IP 自动拉黑统计窗口（秒）"},
		// 多设备登录监控（待办清单 P44）：默认关——一上线就踢人会误伤，先让人看见读数再开处置
		{Key: "device_watch_enabled", Value: "false", Type: "bool", Description: "多设备监控开关：开启后登录时把超出上限的旧会话移出（保留最近的）。只按会话数处置，不按 IP 数"},
		{Key: "max_active_sessions", Value: "5", Type: "number", Description: "同一账号允许同时存在的活跃登录会话数上限（登录时按最后活跃时间保留这么多）"},
		{Key: "device_watch_window_days", Value: "7", Type: "number", Description: "设备与密钥监控的统计窗口（天）"},
		{Key: "suspect_distinct_ips", Value: "8", Type: "number", Description: "窗口内不同 IP 数达到该值即在面板标红（只标红，不因此踢人——NAT/家庭共享下一个 IP 后面是多个真人）"},
	}

	// 已废弃的设置键：策略换了形态时把旧键清掉，否则它会以「未被管理的 key」形式
	// 赖在面板「自定义配置」卡里，看起来像一个还能生效的开关。
	// rank_public_enabled（bool 总开关）被 rank_public_sources（按源白名单）取代；
	// legado_import_url（管理员手填书源直链）被静态托管 /data/shuyuan/bookSource.json 取代——
	// 下发位置是部署事实，不该是一个可能被填错、又没人校验的设置项。
	// site_name 没有任何后端读取方（P53② 巡检数出来的），面板控件一并摘掉、键随之硬删。
	// **废弃键必须同时从上面那份 settings 里删掉**：这段跑在播种之前，
	// 两处都留着就是「每次启动先删再种回来」——P34② 那条「摘出 AutoMigrate 否则 DROP 后复活」的同一形状。
	for _, gone := range []string{"rank_public_enabled", "legado_import_url", "site_name"} {
		var n int64
		goneCond := map[string]interface{}{"key": gone}
		db.Model(&models.SystemSetting{}).Unscoped().Where(goneCond).Count(&n)
		if n > 0 {
			if err := db.Unscoped().Where(goneCond).Delete(&models.SystemSetting{}).Error; err != nil {
				return err
			}
			log.Printf("已移除废弃设置项: %s（原因见上方注释，留着它会以「未被管理的 key」形式赖在面板里）", gone)
		}
	}

	for _, setting := range settings {
		var existing models.SystemSetting
		// 条件一律用 map 形式：让 GORM 按方言给 `key`（MySQL 保留字）加引号
		err := db.Model(&models.SystemSetting{}).Where(map[string]interface{}{"key": setting.Key}).First(&existing).Error
		if err != nil {
			if err != gorm.ErrRecordNotFound {
				return err
			}
			if err := db.Create(&setting).Error; err != nil {
				return err
			}
			log.Printf("Created setting: %s", setting.Key)
			continue
		}
		// type 由声明决定（面板不提供改类型的入口）：老库里这些 key 可能仍是 string，
		// 不对账就会错过保存期的 JSON 校验。只同步 type，不动 value 与 description。
		if existing.Type != setting.Type {
			if err := db.Model(&models.SystemSetting{}).Where(map[string]interface{}{"key": setting.Key}).
				Update("type", setting.Type).Error; err != nil {
				return err
			}
			log.Printf("设置项 %s 的 type 对账为 %s（原 %s）", setting.Key, setting.Type, existing.Type)
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
	justCreated := make(map[string]bool, len(plans))
	for _, plan := range plans {
		var count int64
		db.Model(&models.QuotaPlan{}).Where("code = ?", plan.Code).Count(&count)
		if count == 0 {
			if err := db.Create(&plan).Error; err != nil {
				return err
			}
			justCreated[plan.Code] = true
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

	now := time.Now()
	// 限额播种**只发生在套餐建出来的那一次**：数据源级的限额行现在同时就是该套餐的授权
	// （待办清单 P34），每次启动都按行补齐会把管理员删掉的那条授权复活——
	// 「删了又自己回来」是面板上最说不清的一格。全局 API 限额同一条理由一起收进来。
	// 后来新接入的源由 attachSourceToBuiltinPlans 补授权，沿用下面这同一份默认档。
	var limits []models.QuotaLimit
	for _, d := range builtinPlanLimitDefaults {
		var plan models.QuotaPlan
		if err := db.Where("code = ?", d.Code).First(&plan).Error; err != nil {
			continue
		}
		if !justCreated[plan.Code] {
			continue
		}
		globalRow := NewExplicitGrant(plan.ID, "global", "api", d.GlobalAPI)
		globalRow.CreatedAt, globalRow.UpdatedAt = now, now
		limits = append(limits, globalRow)
		if sourceSeedProvider == nil {
			continue
		}
		for _, src := range sourceSeedProvider() {
			// 每源的默认档走 P69 的那一个入口（`NewSourceGrantRow`），不再在这里抄一份 `d.PerSource`：
			// 两者读的都是 `builtinPlanLimitDefaults`，抄一遍就是"改一处漏一处"的那个第五处。
			row := NewSourceGrantRow(plan.ID, plan.Code, src.Name)
			row.CreatedAt, row.UpdatedAt = now, now
			limits = append(limits, row)
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
// data_sources 为软删除模型，用 Unscoped 硬删；授权（限额表的 scope=source 行）按名字清理
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
		if err := db.Unscoped().Where("name IN ?", removedSources).Delete(&models.DataSource{}).Error; err != nil {
			return err
		}
		log.Printf("已下线数据源 %v：硬删 data_sources 行（授权行由下方按名字清理）", removedSources)
		// 这里**不再**给三个内置套餐补齐全部存活源：那条回填会在下一次有源下线时，
		// 把管理员手动回收过的授权静默发回去。新源由 attachSourceToBuiltinPlans 逐个授权，
		// 存量授权由 alignPlanGrants 从旧关联表搬来，两个入口都够了。
	}

	// 已下线源的计费键**不在这里失效**（待办清单 P39 提过这一处）：包依赖上 db 不能导入 utils/gate
	// （utils 已经导入 db，反过来就成环），而机制上它是**读不到**的——源行刚被硬删，
	// 访问控制 access(400) 排在 billing(500) 之前，任何指向该源的请求先在上一环被挡掉，
	// 那份缓存键从此没有读者，只会随 TTL 自然消失。真要改的是顺序，不是这里。
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

// DefaultInterfaceCost 新接口的默认单价：**只有正文计费**，其余动作（search/detail/chapter/explore/
// recommend/front/landing…）一律 0 = 放行不计费。
// 播种（db/seed.go）与面板「接口消耗」列表自动补行（handlers/quota）共用它——
// 两处各写一遍默认值，早晚有一处漂回旧口径（这条默认值刚从 1 改成 0 就是这么错的）。
func DefaultInterfaceCost(iface string) int64 {
	if iface == "content" {
		return 1
	}
	return 0
}

func seedQuotaCosts(db *gorm.DB) error {
	if sourceSeedProvider == nil {
		return nil
	}
	// 按各源声明的动作集播种。**默认只有 content 计费**：
	// 正文是「一次请求换一整章上游内容」，而 search/detail/chapter/explore 都是导航与元数据——
	// 按请求给它们计费，用户翻一次目录就吃掉几十点额度，而网关侧大多还有上游缓存（成本对不上读数）。
	// recommend 的 cost=0 是历史语义，现在与其余非正文动作一致。
	// 注意这只影响**新播种出来的行**：已有行不改（下面的 count==0 判据），
	// 生产要调口径走面板「接口消耗」或直接改库 + 等 CACHE_TTL 过期（成本缓存在 Redis 里，重启不清）。
	for _, src := range sourceSeedProvider() {
		for _, iface := range src.Actions {
			var count int64
			db.Model(&models.QuotaCost{}).Where("group_code = ? AND interface = ?", src.Name, iface).Count(&count)
			if count == 0 {
				cost := DefaultInterfaceCost(iface)
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

// builtinPlanLimitDefaults 三个内置套餐的默认额度档：全局 API 限额 + 每个数据源的日限额。
// 这份默认值只有这一处定义——建套餐时铺满全部声明源、以及后来新接入的源补授权，
// 都读它（两处各写一遍就会漂移；-1 = 不限）。
var builtinPlanLimitDefaults = []struct {
	Code      string
	GlobalAPI int64
	PerSource int64
}{
	{"free", 1000, 100},  // 免费版
	{"vip", 10000, 1000}, // VIP 版
	{"admin", -1, -1},    // 管理员版（不限）
}

// DefaultPerSourceLimit 该套餐给新授权源配的默认限额；自定义套餐没有默认档，取 -1（不限）。
// 播种与 gate.GrantPlanSource 共用它——「授权一个新源」在两条路上落地的数值必须是同一个，
// 否则脚本装机和面板操作就会各长出一种默认。
func DefaultPerSourceLimit(planCode string) int64 {
	for _, d := range builtinPlanLimitDefaults {
		if d.Code == planCode {
			return d.PerSource
		}
	}
	return -1
}

// builtinPlanCodes 三个内置套餐：新接入的源默认授权给这三个（每套餐一行 scope=source 的限额）。
var builtinPlanCodes = []string{"free", "vip", "admin"}

// seedDataSources 按声明播种 data_sources，并**返回本轮新建的源名**。
// 新建即补套餐关联是这里做的：否则存量库上新增的源进不了任何套餐，访问控制一律 403
// （seedPlanDataSources 只在关联表整表为空时引导，新增源赶不上那次引导——P12）。
// 反过来，已存在的源**不再自动补关联**：管理员在面板手工摘掉的关联不该被下次重启悄悄塞回来。
func seedDataSources(db *gorm.DB) ([]string, error) {
	if sourceSeedProvider == nil {
		log.Println("未注入数据源声明（SetSourceSeedProvider），跳过 data_sources 播种")
		return nil, nil
	}
	var created []string
	for _, ds := range sourceSeedProvider() {
		var count int64
		db.Model(&models.DataSource{}).Where("name = ?", ds.Name).Count(&count)
		if count > 0 {
			continue
		}
		row := models.DataSource{
			Name:        ds.Name,
			DisplayName: ds.DisplayName,
			Category:    ds.Category,
			Description: ds.Description,
			Status:      ds.Status,
			SortOrder:   ds.SortOrder,
		}
		if err := db.Create(&row).Error; err != nil {
			return created, err
		}
		created = append(created, ds.Name)
		log.Printf("Created data source: %s", ds.Name)
		if err := attachSourceToBuiltinPlans(db, row.Name); err != nil {
			return created, err
		}
	}
	return created, nil
}

// attachSourceToBuiltinPlans 把一个源授权给尚缺该授权的内置套餐（幂等：已有授权行就跳过）。
// 授权 = 一行 scope=source 的限额（待办清单 P34，方案 A）。
// 新建行取**该套餐的默认档**（free 100 / vip 1000 / admin 与自定义 -1），
// 不是固定的 -1——过去这句注释写的是 -1，而代码早就改成了默认档，属于"注释替代码说假话"。
func attachSourceToBuiltinPlans(db *gorm.DB, name string) error {
	for _, code := range builtinPlanCodes {
		var plan models.QuotaPlan
		if err := db.Where("code = ?", code).First(&plan).Error; err != nil {
			continue // 该内置套餐不在这次的库里（例如未播种的空库），交给调用方的其它步骤
		}
		created, err := ensurePlanSourceGrant(db, NewSourceGrantRow(plan.ID, code, name))
		if err != nil {
			return err
		}
		if created {
			log.Printf("已将新增数据源 %s 授权给套餐 %s（新增源默认对全部内置套餐可用）", name, code)
		}
	}
	return nil
}

// ensurePlanSourceGrant 保证「套餐 × 源」有一行 scope=source 的限额；已存在则不动，返回是否新建。
// 行由调用方用 P69 的两个意图口之一构造（默认档 / 显式给值），这里只负责"有没有、要不要建"——
// 参数从 `(planID, sourceName, limit)` 收成一行 `models.QuotaLimit` 的原因就是这个：
// 只要限额还从函数签名上传进来，第五处字面量就随时可以再冒出来。
func ensurePlanSourceGrant(tx *gorm.DB, row models.QuotaLimit) (bool, error) {
	var n int64
	if err := tx.Model(&models.QuotaLimit{}).
		Where("plan_id = ? AND scope = ? AND target = ?", row.PlanID, row.Scope, row.Target).
		Count(&n).Error; err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}
	if err := tx.Create(&row).Error; err != nil {
		return false, err
	}
	return true, nil
}

// alignPlanGrants 把旧「套餐-数据源关联」表里还活着的事实搬进限额表（幂等、只补行不删行）。
//
// 生产已经 DROP 掉这张表（待办清单 P34②），而它**不在 AutoMigrate 清单里了**——全新库根本不建它。
// 但骨架是公开的：从 P34 之前的库一路升上来的人，「有没有这一行」在他那里仍然决定能不能用某个源
// （P34 之后没有行 = 没权限），所以这条搬迁必须留着，直到不再有比 P34 更旧的存量库。
// 于是它由「表在不在」自己判定：**表不在就一行不读**（否则 `Find` 会因缺表失败、Seed 直接失败、
// 服务拒绝启动——症状是升级完起不来，而不是「迁移没做」）。
func alignPlanGrants(db *gorm.DB) error {
	if !db.Migrator().HasTable("quota_plan_data_sources") {
		return nil // 已 DROP 或全新库：无存量可搬
	}
	var links []models.QuotaPlanDataSource
	if err := db.Find(&links).Error; err != nil {
		return err
	}
	if len(links) == 0 {
		return nil
	}
	ids := make([]uint, 0, len(links))
	for _, l := range links {
		ids = append(ids, l.DataSourceID)
	}
	var sources []models.DataSource
	if err := db.Where("id IN ?", ids).Find(&sources).Error; err != nil {
		return err
	}
	nameByID := make(map[uint]string, len(sources))
	for _, ds := range sources {
		nameByID[ds.ID] = ds.Name
	}
	migrated := 0
	for _, l := range links {
		name, ok := nameByID[l.DataSourceID]
		if !ok {
			continue // 源已下线的关联行是脏数据，不给它补授权
		}
		// 补 -1 是**忠实还原**（显式给值那个意图口，不是默认档）：旧模型里「有关联行、没限额行」就是不限额。
		// 这里若沿用套餐默认档（free 每源 100），搬迁本身就成了给用户新加一道上限。
		created, err := ensurePlanSourceGrant(db, NewExplicitGrant(l.PlanID, "source", name, -1))
		if err != nil {
			return err
		}
		if created {
			migrated++
		}
	}
	if migrated > 0 {
		log.Printf("套餐授权已并入限额表：从旧关联表迁移 %d 行（limit=-1，不限额但可用）", migrated)
	}
	return nil
}

// warnUngrantedSources 存量库里「一个套餐都没授权」的源对所有人都是 403，
// 但自动补会把管理员的有意回收也抹掉——所以只告警，不写数据。
func warnUngrantedSources(db *gorm.DB) {
	var sources []models.DataSource
	if err := db.Find(&sources).Error; err != nil {
		return
	}
	for _, ds := range sources {
		var n int64
		db.Model(&models.QuotaLimit{}).
			Where("scope = ? AND target = ?", "source", ds.Name).Count(&n)
		if n == 0 {
			log.Printf("警告：数据源 %s 未授权给任何套餐，其接口对所有人返回 403；"+
				"确需开放请在管理面板「额度 → 限制项」加一行（scope=source，限额 -1 即不限额），"+
				"确要下线请把该源置为禁用", ds.Name)
		}
	}
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
	if _, err := seedDataSources(db); err != nil {
		return err
	}
	if err := alignPlanGrants(db); err != nil {
		return err
	}
	warnUngrantedSources(db)
	if err := seedAdmin(db); err != nil {
		return err
	}
	return nil
}
