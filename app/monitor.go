package app

import (
	"log"
	"time"

	"loomproxy/base"
	"loomproxy/conf"
	"loomproxy/db"
	"loomproxy/lifecycle"
	"loomproxy/models"
)

// monitorRetention 调用明细的保留时长；<=0 返回 0，表示永久保留且不做清理
func monitorRetention() time.Duration {
	days := conf.Config.MonitorRetentionDays
	if days <= 0 {
		return 0
	}
	return time.Duration(days) * 24 * time.Hour
}

// initMonitorPersistence 注册监控明细的落库链路：
// 内存环形缓冲淘汰时批量落库；服务收到关闭指令时内存中剩余明细兜底落库
func initMonitorPersistence() {
	base.SetMetricsFlusher(persistCallLogs)
	lifecycle.RegisterCleanup(func() {
		persistCallLogs(base.DrainRecentCalls())
	})
}

// persistCallLogs 批量写入调用明细，并顺带清理超过保留期的历史记录
func persistCallLogs(calls []base.RecentCall) {
	if len(calls) == 0 || db.DB == nil {
		return
	}
	rows := make([]models.ApiCallLog, 0, len(calls))
	for _, rc := range calls {
		rows = append(rows, models.ApiCallLog{
			Username:  rc.Username,
			IP:        rc.IP,
			Source:    rc.Source,
			Action:    rc.Action,
			Status:    rc.Status,
			LatencyMs: rc.LatencyMs,
			CreatedAt: rc.Time,
		})
	}
	if err := db.DB.Create(&rows).Error; err != nil {
		log.Printf("ERROR: persist api call logs failed: %v", err)
		return
	}
	log.Printf("监控明细落库 %d 条", len(rows))
	PurgeExpiredCallLogs()
}

// PurgeExpiredCallLogs 清理超过保留期的明细：删除前先按 数据源/接口 聚合
// 累加到 api_call_stats 永久归档（明细会过期，累计次数保留）。
// 保留期 <=0（默认永久）时整段 no-op——既不聚合也不删除，归档表保持为空、明细表即全量。
// 导出供集成测试按配置驱动清理语义。
func PurgeExpiredCallLogs() {
	retention := monitorRetention()
	if retention <= 0 {
		return
	}
	cutoff := time.Now().Add(-retention)

	type aggRow struct {
		Source     string
		Action     string
		Total      int64
		Success    int64
		LatencySum int64
		MaxLatency int64
	}
	var aggs []aggRow
	if err := db.DB.Model(&models.ApiCallLog{}).
		Select("source, action, COUNT(*) AS total, "+
			"SUM(CASE WHEN status >= 200 AND status < 300 THEN 1 ELSE 0 END) AS success, "+
			"COALESCE(SUM(latency_ms), 0) AS latency_sum, COALESCE(MAX(latency_ms), 0) AS max_latency").
		Where("created_at < ?", cutoff).
		Group("source, action").
		Scan(&aggs).Error; err != nil {
		log.Printf("ERROR: aggregate expired api call logs failed: %v", err)
	}
	for _, a := range aggs {
		var st models.ApiCallStat
		db.DB.Where("source = ? AND action = ?", a.Source, a.Action).First(&st)
		st.Source = a.Source
		st.Action = a.Action
		st.Total += a.Total
		st.Success += a.Success
		st.Failed += a.Total - a.Success
		st.TotalLatencyMs += a.LatencySum
		if a.MaxLatency > st.MaxLatencyMs {
			st.MaxLatencyMs = a.MaxLatency
		}
		if err := db.DB.Save(&st).Error; err != nil {
			log.Printf("ERROR: archive api call stat %s/%s failed: %v", a.Source, a.Action, err)
		}
	}

	if err := db.DB.Where("created_at < ?", cutoff).Delete(&models.ApiCallLog{}).Error; err != nil {
		log.Printf("ERROR: purge old api call logs failed: %v", err)
	}
}
