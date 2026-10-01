package test

// db 层集成测试：方言兼容与启动迁移行为。

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"loomproxy/conf"
	"loomproxy/db"
	"loomproxy/models"
)

// TestUserRolesDedupOnSQLite 回归：user_roles 去重 SQL 原为 MySQL 多表删除语法
// （DELETE t1 FROM ...），SQLite 下启动报错；现按方言选择，SQLite 走标准子查询。
// 预置含重复行的旧库 → db.Init 去重 + AutoMigrate 加唯一索引 → 重复行被清理且不可再插入。
func TestUserRolesDedupOnSQLite(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// 预置旧版（无唯一索引）user_roles 表并写入重复关联
	rawDB, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := rawDB.Exec(`CREATE TABLE user_roles (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL,
		role_id INTEGER NOT NULL,
		created_at DATETIME
	)`); err != nil {
		t.Fatalf("create user_roles: %v", err)
	}
	for _, pair := range [][2]int{{1, 1}, {1, 1}, {1, 2}, {2, 1}} {
		if _, err := rawDB.Exec("INSERT INTO user_roles (user_id, role_id, created_at) VALUES (?, ?, ?)",
			pair[0], pair[1], time.Now()); err != nil {
			t.Fatalf("seed user_roles: %v", err)
		}
	}
	rawDB.Close()

	setupTestConf(t, dir)
	if err := db.Init(); err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB.DB(); err == nil {
			sqlDB.Close()
		}
	})

	// 4 行去重后应剩 3 行；(1,1) 只保留一条
	var total int64
	if err := db.DB.Table("user_roles").Count(&total).Error; err != nil {
		t.Fatalf("count user_roles: %v", err)
	}
	if total != 3 {
		t.Fatalf("去重后 user_roles 行数 = %d, want 3", total)
	}
	var dup int64
	if err := db.DB.Table("user_roles").Where("user_id = 1 AND role_id = 1").Count(&dup).Error; err != nil {
		t.Fatalf("count dup: %v", err)
	}
	if dup != 1 {
		t.Fatalf("(1,1) 关联行数 = %d, want 1", dup)
	}

	// AutoMigrate 应已建立唯一索引：再插重复关联必须失败
	if err := db.DB.Exec("INSERT INTO user_roles (user_id, role_id, created_at) VALUES (1, 1, ?)",
		time.Now()).Error; err == nil {
		t.Fatal("唯一索引未生效：重复插入 (1,1) 未报错")
	}
}

// TestSeedLegacyGroupExpansionAndRetired 覆盖 seed 的两条「清单来自部署侧」机制：
//   - 存量按历史组码配置的计费行，按各源 LegacyGroups 声明展开为每源一行（沿用原
//     cost 与限流配置）并删除组行——未声明该组的源不得被展开；
//   - RETIRED_SOURCES 声明的退役源，其在 data_sources / quota_costs / quota_limits
//     的存量行被清理（历史用量流水保留）。
//
// 底座不携带任何书源，映射与清单都由声明提供，故用假源与夹具组码验证机制本身。
func TestSeedLegacyGroupExpansionAndRetired(t *testing.T) {
	newTestServer(t)

	const retired = "retired_src"
	// 组行用尚未播种的动作，避开 seedQuotaCosts 已建的每源行（否则展开会因目标已存在而跳过）
	const iface = "legacy_only_action"

	conf.Config.RetiredSources = []string{retired}
	defer func() { conf.Config.RetiredSources = nil }()

	toCreate := []interface{}{
		&models.QuotaCost{GroupCode: fakeLegacyGroup, Interface: iface, Cost: 3, Status: 1, Interval: 7},
		&models.QuotaCost{GroupCode: retired, Interface: "search", Cost: 9, Status: 1},
		&models.DataSource{Name: retired, DisplayName: "已退役源", Category: "legacy", Status: 1},
		&models.QuotaLimit{PlanID: planIDByCode(t, "free"), Scope: "source", Target: retired, Limit: 5, Period: "day"},
	}
	for _, row := range toCreate {
		if err := db.DB.Create(row).Error; err != nil {
			t.Fatalf("构造存量行失败: %v", err)
		}
	}

	if err := db.Seed(db.DB); err != nil {
		t.Fatalf("Seed 出错: %v", err)
	}

	var expanded models.QuotaCost
	if err := db.DB.Where("group_code = ? AND interface = ?", fakeA, iface).First(&expanded).Error; err != nil {
		t.Fatalf("声明了 %s 的 %s 未被展开: %v", fakeLegacyGroup, fakeA, err)
	}
	if expanded.Cost != 3 || expanded.Interval != 7 {
		t.Fatalf("展开未沿用组行的计费与限流配置: cost=%d interval=%d", expanded.Cost, expanded.Interval)
	}

	var n int64
	if err := db.DB.Model(&models.QuotaCost{}).Where("group_code = ? AND interface = ?", fakeB, iface).Count(&n).Error; err != nil {
		t.Fatalf("count fake_b: %v", err)
	}
	if n != 0 {
		t.Fatalf("未声明组归属的 %s 被误展开（行数=%d）", fakeB, n)
	}
	if err := db.DB.Model(&models.QuotaCost{}).Where("group_code = ?", fakeLegacyGroup).Count(&n).Error; err != nil {
		t.Fatalf("count 组行: %v", err)
	}
	if n != 0 {
		t.Fatalf("展开后组行未清理（行数=%d）", n)
	}

	for _, check := range []struct {
		table string
		cond  string
		args  []interface{}
	}{
		{"data_sources", "name = ?", []interface{}{retired}},
		{"quota_costs", "group_code = ?", []interface{}{retired}},
		{"quota_limits", "scope = ? AND target = ?", []interface{}{"source", retired}},
	} {
		if err := db.DB.Unscoped().Table(check.table).Where(check.cond, check.args...).Count(&n).Error; err != nil {
			t.Fatalf("count %s: %v", check.table, err)
		}
		if n != 0 {
			t.Fatalf("退役源 %s 在 %s 的存量行未清理（行数=%d）", retired, check.table, n)
		}
	}

	// 历史用量流水不清理（口径与数据源无关，审计需要）
	var logs int64
	if err := db.DB.Model(&models.QuotaUsageLog{}).Count(&logs).Error; err != nil {
		t.Fatalf("count quota_usage_logs: %v", err)
	}
}

// TestSeedLinksNewSourceButNotRevivedOne 覆盖 P12 的两面：
//   - **新增**的数据源（本轮才建出行）必须自动进三个内置套餐，否则存量库上加源＝全员 403；
//   - 已存在的源被管理员手工摘光套餐关联后，重启**不再**把关联塞回来（那会覆盖管理员的有意配置），
//     只在启动日志里告警。
func TestSeedLinksNewSourceButNotRevivedOne(t *testing.T) {
	newTestServer(t)

	countLinks := func(name string) int64 {
		var n int64
		if err := db.DB.Model(&models.QuotaPlanDataSource{}).
			Joins("JOIN data_sources ON data_sources.id = quota_plan_data_sources.data_source_id").
			Where("data_sources.name = ?", name).Count(&n).Error; err != nil {
			t.Fatalf("统计 %s 的套餐关联失败: %v", name, err)
		}
		return n
	}

	// 造出「新源」：把 fake_c 连行带关联硬删（软删会让 name 唯一索引继续占位，seed 就建不出来了）
	var c models.DataSource
	if err := db.DB.Unscoped().Where("name = ?", fakeC).First(&c).Error; err != nil {
		t.Fatalf("取 fake_c 失败: %v", err)
	}
	if err := db.DB.Where("data_source_id = ?", c.ID).Delete(&models.QuotaPlanDataSource{}).Error; err != nil {
		t.Fatalf("清 fake_c 关联失败: %v", err)
	}
	if err := db.DB.Unscoped().Delete(&models.DataSource{}, c.ID).Error; err != nil {
		t.Fatalf("删 fake_c 行失败: %v", err)
	}

	// 造出「被管理员摘光关联的存量源」：只删关联，保留 data_sources 行
	var b models.DataSource
	if err := db.DB.Where("name = ?", fakeB).First(&b).Error; err != nil {
		t.Fatalf("取 fake_b 失败: %v", err)
	}
	if err := db.DB.Where("data_source_id = ?", b.ID).Delete(&models.QuotaPlanDataSource{}).Error; err != nil {
		t.Fatalf("清 fake_b 关联失败: %v", err)
	}

	if err := db.Seed(db.DB); err != nil {
		t.Fatalf("Seed 出错: %v", err)
	}

	if got := countLinks(fakeC); got != 3 {
		t.Errorf("新增源 %s 的内置套餐关联 = %d, want 3（free/vip/admin 各一行）", fakeC, got)
	}
	if got := countLinks(fakeB); got != 0 {
		t.Errorf("管理员摘除的关联被重启塞回来了：%s 现有 %d 行（应只告警不改数据）", fakeB, got)
	}
	// 幂等：再跑一次不得重复插入（唯一索引 idx_plan_source 会直接报错）
	if err := db.Seed(db.DB); err != nil {
		t.Fatalf("二次 Seed 出错（幂等被破坏）: %v", err)
	}
	if got := countLinks(fakeC); got != 3 {
		t.Errorf("二次 Seed 后新增源关联 = %d, want 仍为 3", got)
	}
}
