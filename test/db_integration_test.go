package test

// db 层集成测试：方言兼容与启动迁移行为。

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"loomproxy-go/db"
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
