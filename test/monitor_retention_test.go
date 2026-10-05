package test

// 接口调用明细的保留期语义（MONITOR_RETENTION_DAYS）。
//
// 默认 0=永久保留且整段不清理：归档表恒为空、明细表即全量；设 N 天才先聚合归档再删除。
// 两条用例把这两种模式各钉一次——写死保留期（如曾经的 7 天）时第一条必然失败。

import (
	"testing"
	"time"

	"loomproxy/app"
	"loomproxy/conf"
	"loomproxy/db"
	"loomproxy/models"
)

// TestCallLogRetentionDisabledWhenZero 保留窗口为 0 时：既不删明细，也不写归档表。
//
// 名字里的 "ByDefault" 去掉了（待办清单 P98 巡检发现的假绿灯）：这一句读的是**测试脚手架**的
// `conf.Config`（`setupTestConf` 的结构体字面量没写这个字段，于是它是 Go 零值 0），
// 不是产品默认值——把 `conf.go` 里 `envInt("MONITOR_RETENTION_DAYS", 0)` 改成 7，这条照样绿。
// 产品默认那一钉现在在 `test/conf_env_notice_test.go` 的 TestEnvDocumentedDefaults 上，
// 那条走的是解析器本身，改默认就会红。
func TestCallLogRetentionDisabledWhenZero(t *testing.T) {
	srv := newTestServer(t)
	_ = srv
	if conf.Config.MonitorRetentionDays != 0 {
		t.Fatalf("这条用例的前提是脚手架里保留窗口为 0（实为 %d）；要验产品默认去看 conf_env_notice_test.go", conf.Config.MonitorRetentionDays)
	}

	now := time.Now()
	insertCallLog(t, "fake_a", "search", 200, now.AddDate(0, 0, -10))
	insertCallLog(t, "fake_a", "search", 500, now.AddDate(0, 0, -400))
	insertCallLog(t, "fake_a", "detail", 200, now)

	var total int64
	db.DB.Model(&models.ApiCallLog{}).Count(&total)
	if total != 3 {
		t.Fatalf("插入后应有 3 条明细，实为 %d", total)
	}

	app.PurgeExpiredCallLogs()

	db.DB.Model(&models.ApiCallLog{}).Count(&total)
	if total != 3 {
		t.Errorf("保留期关闭时不得清理任何明细，剩余 %d 条（期望 3）", total)
	}
	var stats int64
	db.DB.Model(&models.ApiCallStat{}).Count(&stats)
	if stats != 0 {
		t.Errorf("保留期关闭时不得写归档表，归档 %d 行（期望 0）", stats)
	}
}

func TestCallLogPurgedWithRetention(t *testing.T) {
	newTestServer(t)
	restore := conf.Config.MonitorRetentionDays
	defer func() { conf.Config.MonitorRetentionDays = restore }()
	conf.Config.MonitorRetentionDays = 7

	now := time.Now()
	insertCallLog(t, "fake_b", "search", 200, now.AddDate(0, 0, -10)) // 过期
	insertCallLog(t, "fake_b", "search", 500, now.AddDate(0, 0, -9))  // 过期
	insertCallLog(t, "fake_b", "detail", 200, now.AddDate(0, 0, -1))  // 期内

	app.PurgeExpiredCallLogs()

	var expired int64
	db.DB.Model(&models.ApiCallLog{}).
		Where("created_at < ?", now.AddDate(0, 0, -7)).Count(&expired)
	if expired != 0 {
		t.Errorf("过期明细应被删除，剩余 %d 条", expired)
	}
	var kept int64
	db.DB.Model(&models.ApiCallLog{}).
		Where("created_at >= ?", now.AddDate(0, 0, -7)).Count(&kept)
	if kept != 1 {
		t.Errorf("期内明细应保留 1 条，实为 %d", kept)
	}

	var st models.ApiCallStat
	if err := db.DB.Where("source = ? AND action = ?", "fake_b", "search").First(&st).Error; err != nil {
		t.Fatalf("过期明细应先归档到 api_call_stats: %v", err)
	}
	if st.Total != 2 || st.Success != 1 || st.Failed != 1 {
		t.Errorf("归档计数 = total %d / success %d / failed %d，期望 2/1/1", st.Total, st.Success, st.Failed)
	}
}

// 归档与删除的原子性（待办清单 P55）。
//
// 修前的形状：先逐行 Save 归档、再单独 DELETE。只要那次删除失败（或进程在两步之间崩），
// 归档里已经多了这批数、明细却还在——而面板的「历史累计」= 归档 + 现存明细，
// 于是这个数字从那一刻起**永久性虚高**，且再跑一轮还会再虚高一次。
// 这条用例用 sqlite 的 BEFORE DELETE 触发器把删除钉死，验的是"整体回滚"这四个字。
func TestCallLogPurgeRollsBackWhenDeleteFails(t *testing.T) {
	newTestServer(t)
	restore := conf.Config.MonitorRetentionDays
	defer func() { conf.Config.MonitorRetentionDays = restore }()
	conf.Config.MonitorRetentionDays = 7

	now := time.Now()
	insertCallLog(t, "fake_c", "search", 200, now.AddDate(0, 0, -12))
	insertCallLog(t, "fake_c", "search", 500, now.AddDate(0, 0, -11))
	insertCallLog(t, "fake_c", "detail", 200, now)

	// 让 DELETE 语句失败（触发器里那行 SELECT RAISE 就是报错本身）
	if err := db.DB.Exec(`CREATE TRIGGER block_purge_before_delete BEFORE DELETE ON api_call_logs
		BEGIN SELECT RAISE(ABORT, 'boom: 用例故意挡住删除'); END`).Error; err != nil {
		t.Fatalf("建挡删除的触发器失败: %v", err)
	}
	defer func() {
		if err := db.DB.Exec(`DROP TRIGGER IF EXISTS block_purge_before_delete`).Error; err != nil {
			t.Errorf("撤掉触发器失败: %v", err)
		}
	}()

	app.PurgeExpiredCallLogs()

	var archived int64
	db.DB.Model(&models.ApiCallStat{}).Where("source = ?", "fake_c").
		Select("COALESCE(SUM(total),0)").Scan(&archived)
	if archived != 0 {
		t.Errorf("删除失败时归档却涨了 %d 行——归档与删除没在一个事务里，这正是「永久性虚高」的入口", archived)
	}
	var kept int64
	db.DB.Model(&models.ApiCallLog{}).Count(&kept)
	if kept != 3 {
		t.Errorf("回滚后明细应还是 3 条，实为 %d", kept)
	}

	// 撤掉障碍后重跑：数字必须**只算一次**
	if err := db.DB.Exec(`DROP TRIGGER block_purge_before_delete`).Error; err != nil {
		t.Fatalf("撤触发器失败: %v", err)
	}
	app.PurgeExpiredCallLogs()
	var st models.ApiCallStat
	if err := db.DB.Where("source = ? AND action = ?", "fake_c", "search").First(&st).Error; err != nil {
		t.Fatalf("重跑后应归档成功: %v", err)
	}
	if st.Total != 2 || st.Success != 1 || st.Failed != 1 {
		t.Errorf("重跑后的归档 = %d/%d/%d, want 2/1/1（虚高会表现成 4/2/2）", st.Total, st.Success, st.Failed)
	}
	// 再跑一轮（此时已无过期行）：数字不动，才算幂等
	app.PurgeExpiredCallLogs()
	var st2 models.ApiCallStat
	if err := db.DB.Where("source = ? AND action = ?", "fake_c", "search").First(&st2).Error; err != nil {
		t.Fatalf("再查归档失败: %v", err)
	}
	if st2.Total != 2 {
		t.Errorf("多跑一轮后归档变成 %d, want 仍为 2", st2.Total)
	}
}
