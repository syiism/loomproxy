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

func TestCallLogRetentionDisabledByDefault(t *testing.T) {
	srv := newTestServer(t)
	_ = srv
	if conf.Config.MonitorRetentionDays != 0 {
		t.Fatalf("MONITOR_RETENTION_DAYS 默认应为 0（永久），实为 %d", conf.Config.MonitorRetentionDays)
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
