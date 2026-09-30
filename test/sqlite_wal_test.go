package test

// SQLite 加固回归测试：db.Init 经 DSN 启用 WAL 日志模式（并发写等待而非
// database is locked；WAL 为数据库级持久属性，设置一次即可由 PRAGMA 读回）。

import (
	"testing"

	"loomproxy-go/db"
)

func TestSQLiteJournalModeWAL(t *testing.T) {
	newTestServer(t)

	var mode string
	if err := db.DB.Raw("PRAGMA journal_mode").Scan(&mode).Error; err != nil {
		t.Fatalf("查询 journal_mode 失败: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode=%s，期望 wal", mode)
	}
}
