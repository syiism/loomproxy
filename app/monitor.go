package app

import (
	"context"
	"errors"
	"log"
	"sync/atomic"
	"time"

	"gorm.io/gorm"

	"loomproxy/base"
	"loomproxy/conf"
	"loomproxy/db"
	"loomproxy/handlers/admin"
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

// PersistCallLogs 把一批明细写进 api_call_logs（导出位，同 PurgeExpiredCallLogs 的先例）。
// flusher 由 app.Run 的 initMonitorPersistence 接线，而测试夹具只建 CreateApp——
// 不导出这一层，集成测试就永远钉不住「RecentCall → 明细列」的落库映射
// （断言只到内存那一段，映射行删了也不会红，正是 P124 那一族「声明了、没人装配」的形状）。
func PersistCallLogs(calls []base.RecentCall) {
	persistCallLogs(calls)
}

// persistCallLogs 批量写入调用明细，并顺带清理超过保留期的历史记录
func persistCallLogs(calls []base.RecentCall) {
	if len(calls) == 0 || db.DB == nil {
		return
	}
	rows := make([]models.ApiCallLog, 0, len(calls))
	for _, rc := range calls {
		rows = append(rows, models.ApiCallLog{
			Username:     rc.Username,
			IP:           rc.IP,
			Source:       rc.Source,
			Action:       rc.Action,
			Status:       rc.Status,
			InBandError:  rc.InBandError,
			InBandReason: rc.InBandReason,
			InBandCode:   rc.InBandCode,
			LatencyMs:    rc.LatencyMs,
			CreatedAt:    rc.Time,
			Keyword:      rc.Keyword,
			BookName:     rc.BookName,
			ChapterTitle: rc.ChapterTitle,
			BookIdent:    rc.BookIdent,
			ChapterIdent: rc.ChapterIdent,
			Media:        rc.Media,
			ResultCount:  rc.ResultCount,
			// 空的原因是「本人关掉」还是「没抽到」，只有写的一刻知道——不落库就再也分不出来
			ContentWithheld: rc.ContentWithheld,
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
//
// **累加与删除必须在同一个事务里**（待办清单 P55）：这两步以前各写各的，于是只要
// 「归档已写入、明细没删掉」（删除报错、进程在两步之间崩），下一轮就会把同一批行**再加一遍**——
// 而面板的「历史累计」= 归档 + 现存明细，那一刻起就永久性虚高，没有任何地方说它虚高。
// 现在删除失败会整体回滚：归档不涨、明细还在，下一轮重跑等价于重来一次。
//
// 这里刻意**不**用「先 Pluck id 再按 id 删」：那要往语句里塞几万个 id（现网明细三万行），
// 而 `created_at < cutoff` 这个谓词在同一事务里是稳定的（新写入的行时间只会更新），
// 谓词删除更短也不会跨语句漂移。
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
	err := db.DB.Transaction(func(tx *gorm.DB) error {
		var aggs []aggRow
		// 成功 = 2xx 且非带内失败（P22）。IS NOT TRUE 对 NULL 也成立：旧行没有这一列的值，
		// 仍按纯 HTTP 口径计成功，不会因为列后加而被读成失败
		if err := tx.Model(&models.ApiCallLog{}).
			Select("source, action, COUNT(*) AS total, "+
				"SUM(CASE WHEN status >= 200 AND status < 300 AND in_band_error IS NOT TRUE THEN 1 ELSE 0 END) AS success, "+
				"COALESCE(SUM(latency_ms), 0) AS latency_sum, COALESCE(MAX(latency_ms), 0) AS max_latency").
			Where("created_at < ?", cutoff).
			Group("source, action").
			Scan(&aggs).Error; err != nil {
			return err
		}
		for _, a := range aggs {
			var st models.ApiCallStat
			// 查不到就用零值行（下面 Save 会插入）；这里的报错不单独判——同一事务里后面任何一步失败都整体回滚
			if err := tx.Where("source = ? AND action = ?", a.Source, a.Action).First(&st).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			st.Source = a.Source
			st.Action = a.Action
			st.Total += a.Total
			st.Success += a.Success
			st.Failed += a.Total - a.Success
			st.TotalLatencyMs += a.LatencySum
			if a.MaxLatency > st.MaxLatencyMs {
				st.MaxLatencyMs = a.MaxLatency
			}
			if err := tx.Save(&st).Error; err != nil {
				return err
			}
		}
		return tx.Where("created_at < ?", cutoff).Delete(&models.ApiCallLog{}).Error
	})
	if err != nil {
		// 整体回滚，等下一轮重跑；这条日志就是说"这一轮没清掉、数字没被污染"的地方
		log.Printf("ERROR: 监控明细清理失败（归档与删除同事务，已整体回滚，未产生重复归档）: %v", err)
	}
}

// backfillBusy 挡住「上一轮还没跑完又来一轮」：库慢加上间隔调短，两轮会互相踩同一批行。
var backfillBusy atomic.Bool

// backfillBlockedLogged 「捞到行却一处都补不上」只在变化发生时说一次：这类行会长期留在结果里，
// 每轮各打一行就是纯噪声；补到东西之后重新允许说一次，才看得见「又开始缺名」这件事。
var backfillBlockedLogged atomic.Bool

// 一轮 tick 里最多连捞几批（每批 admin.BackfillScanCap 行）：够把积压捞空，又有上界，
// 不会因为某次导入把库里灌进几万缺名行就占着连接不放。
const backfillRoundsPerTick = 4

// startSubjectNameBackfill 定时用命名缓存回填明细里缺失的书名/章节名（待办清单 P20）。
//
// 为什么要有循环：回填本来只有手动入口，而缺名的成因是结构性的——重启之后命名缓存是空的，
// 那第一批只带 bookId/itemId 的正文调用就只记下标识、名称留空；等这本书再被搜索命中，
// 映射回来了，那些行本可以补上，但没人记得要点一次（点一次还只捞 5000 行）。
// 把它做成默认行为，面板的覆盖率表（P19）才不会一直红着一格。
func startSubjectNameBackfill(ctx context.Context) {
	sec := conf.Config.MonitorBackfillSec
	if sec <= 0 {
		log.Printf("名称回填定时任务已关闭（MONITOR_BACKFILL_SEC=%d），只剩手动入口", sec)
		return
	}
	interval := time.Duration(sec) * time.Second
	go func() {
		// 先等一整轮再动手：源的 OnBoot 导入与第一批请求还没来得及把命名缓存 warm 起来，立刻跑只会捞空
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// 一轮回填会读明细、调命名缓存，都在 gin Recovery 之外（待办清单 P103）
				base.Supervised("名称回填的一轮", RunSubjectBackfillTick)
			}
		}
	}()
	log.Printf("名称回填定时任务已启动：每 %v 一轮，窗口 %d 天、单批上限 %d 行",
		interval, admin.BackfillDefaultDays, admin.BackfillScanCap)
}

// RunSubjectBackfillTick 跑一轮回填：按单批上限连续捞到「捞空」或「补不动」为止——
// 反查不到映射的标识会一直留在结果里，不认这个条件就每轮重复捞同一批行。
// 导出与 PurgeExpiredCallLogs 同例：供集成测试按配置驱动循环体的语义。
func RunSubjectBackfillTick() {
	if !backfillBusy.CompareAndSwap(false, true) {
		log.Printf("名称回填跳过：上一轮还没跑完")
		return
	}
	defer backfillBusy.Store(false)

	for round := 1; round <= backfillRoundsPerTick; round++ {
		summary, err := admin.RunSubjectBackfill(admin.BackfillDefaultDays)
		if err != nil {
			log.Printf("ERROR: 定时回填调用明细名称失败: %v", err)
			return
		}
		if summary.BookFilled+summary.ChapterFilled == 0 {
			if summary.Scanned > 0 && backfillBlockedLogged.CompareAndSwap(false, true) {
				log.Printf("名称回填：窗口内 %d 行缺名，命名缓存里都反查不到，先不补（等这些书再被搜索/详情命中）", summary.Scanned)
			}
			return
		}
		backfillBlockedLogged.Store(false)
		log.Printf("名称回填第 %d 批：扫描 %d 行，补书名 %d 处、补章节名 %d 处%s",
			round, summary.Scanned, summary.BookFilled, summary.ChapterFilled,
			map[bool]string{true: "（未捞空，接着捞）", false: ""}[summary.Truncated])
		if !summary.Truncated {
			return
		}
	}
}
