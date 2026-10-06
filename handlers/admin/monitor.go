package admin

import (
	"log"
	"net/http"
	"sort"
	"time"

	"github.com/gin-gonic/gin"

	"loomproxy/base"
	"loomproxy/db"
	"loomproxy/handlers/auth"
	"loomproxy/models"
	"loomproxy/utils"
)

type monitorRow struct {
	Source       string     `json:"source"`
	Action       string     `json:"action"`
	Total        int64      `json:"total"`
	Success      int64      `json:"success"`
	Failed       int64      `json:"failed"`
	SuccessRate  float64    `json:"success_rate"`
	AvgLatencyMs int64      `json:"avg_latency_ms"`
	MaxLatencyMs int64      `json:"max_latency_ms"`
	Lifetime     int64      `json:"lifetime"` // 历史累计调用（归档 + 未清理明细）
	LastCalledAt *time.Time `json:"last_called_at"`
	LastStatus   int        `json:"last_status"`
}

// lifetimeCounts 各接口的历史累计调用次数：永久归档（api_call_stats）
// + 尚未清理的明细（api_call_logs）。默认 MONITOR_RETENTION_DAYS=0 时明细即全量、归档为空，
// 设了保留期才有「已归档」的那部分——两种模式下都不重不漏
func lifetimeCounts() map[string]int64 {
	m := make(map[string]int64)
	var stats []models.ApiCallStat
	if err := db.DB.Find(&stats).Error; err != nil {
		db.LogReadFail("monitor_trend:api_call_stats", err)
	}
	for _, s := range stats {
		m[s.Source+"/"+s.Action] += s.Total
	}
	type row struct {
		Source string
		Action string
		Total  int64
	}
	var rows []row
	if err := db.DB.Model(&models.ApiCallLog{}).
		Select("source, action, COUNT(*) AS total").
		Group("source, action").
		Scan(&rows).Error; err != nil {
		db.LogReadFail("monitor_subjects_agg:api_call_logs", err)
	}
	for _, r := range rows {
		m[r.Source+"/"+r.Action] += r.Total
	}
	return m
}

// GetMonitor 数据源接口调用监控（内存计数，进程重启清零，与额度计费无关）
func GetMonitor(c *gin.Context) {
	started, snap := base.MetricsSnapshot()
	lifetime := lifetimeCounts()
	// 会话聚合含已淘汰落库的部分，lifetime 口径需扣除避免与明细表双计
	flushed, flushedTotal := base.FlushedCounts()
	rows := make([]monitorRow, 0, 64)
	var total, success int64
	// 全量历史累计 = 归档 + 未清理明细 + 会话未落库计数
	var lifetimeTotal int64
	for _, v := range lifetime {
		lifetimeTotal += v
	}
	for src, am := range snap {
		for act, m := range am {
			var rate float64
			if m.Total > 0 {
				rate = float64(m.Success) / float64(m.Total) * 100
			}
			rows = append(rows, monitorRow{
				Source:       src,
				Action:       act,
				Total:        m.Total,
				Success:      m.Success,
				Failed:       m.Failed,
				SuccessRate:  rate,
				AvgLatencyMs: m.AvgLatencyMs,
				MaxLatencyMs: m.MaxLatencyMs,
				Lifetime:     lifetime[src+"/"+act] + m.Total - flushed[src+"/"+act],
				LastCalledAt: m.LastCalledAt,
				LastStatus:   m.LastStatus,
			})
			total += m.Total
			success += m.Success
		}
	}
	lifetimeTotal += total - flushedTotal
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Source != rows[j].Source {
			return rows[i].Source < rows[j].Source
		}
		return rows[i].Action < rows[j].Action
	})
	var overallRate float64
	if total > 0 {
		overallRate = float64(success) / float64(total) * 100
	}
	auth.Ok(c, gin.H{
		"started_at":            started,
		"total":                 total,
		"success":               success,
		"failed":                total - success,
		"success_rate":          overallRate,
		"lifetime_total":        lifetimeTotal,
		"items":                 rows,
		"recent":                base.RecentCalls(50),
		"name_store_persistent": base.SubjectStoreLoaded(),
	})
}

// ResetMonitor 清零监控计数并重置统计起点
func ResetMonitor(c *gin.Context) {
	base.ResetMetrics()
	auth.Ok(c, gin.H{"message": "监控计数已清零"})
}

// GetMonitorHistory 分页查询历史调用明细（api_call_logs，最新在前）
func GetMonitorHistory(c *gin.Context) {
	page, pageSize := utils.Paginate(c.DefaultQuery("page", "1"), c.DefaultQuery("page_size", "20"), 20)
	q := db.DB.Model(&models.ApiCallLog{})
	if src := c.Query("source"); src != "" {
		q = q.Where("source = ?", src)
	}
	if username := c.Query("username"); username != "" {
		q = q.Where("username = ?", username)
	}
	if act := c.Query("action"); act != "" {
		q = q.Where("action = ?", act)
	}
	// 内容维度：文本类做 contains 筛选（列名取自代码内固定字面量，值走绑定参数；
	// 转义 LIKE 通配符，用户输入的 % 不该吞掉全表），媒介是枚举、等值匹配
	if v := c.Query("keyword"); v != "" {
		q = q.Where(likeESCAPE("keyword"), "%"+escapeLike(v)+"%")
	}
	if v := c.Query("book_name"); v != "" {
		q = q.Where(likeESCAPE("book_name"), "%"+escapeLike(v)+"%")
	}
	if v := c.Query("chapter_title"); v != "" {
		q = q.Where(likeESCAPE("chapter_title"), "%"+escapeLike(v)+"%")
	}
	if media := c.Query("media_type"); media != "" {
		q = q.Where("media = ?", media)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "数据库错误: "+err.Error())
		return
	}
	list := make([]models.ApiCallLog, 0, pageSize)
	if err := q.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "数据库错误: "+err.Error())
		return
	}
	auth.Ok(c, gin.H{
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"list":      list,
	})
}

// trendDays 趋势图的展示窗口（天）。这是展示口径，与明细保留期无关：
// 明细默认永久保留（MONITOR_RETENTION_DAYS=0），设了保留期也只影响清理，不改这张图的天数
const trendDays = 7

// GetMonitorTrend 近 trendDays 天调用趋势：按天 × 数据源聚合 api_call_logs 明细。
// 口径说明：api_call_stats 永久归档不带时间维度，按天趋势只能来自尚未清理的明细；
// 日标序列（`days`）按**平台时区**算（`utils.DayStart`，与额度同一个日界），
// 而**分桶**由库侧的 `date(created_at)` 判——生产 MySQL（`loc=Local`）与平台时区一致，
// sqlite 会把带 +08 的串按 UTC 归一日界（实测：北京 10-05 04:40 存成 `2026-10-05T04:40:00+08:00`，
// `date()` 得 `2026-10-04`），所以开发/测试库里北京 00:00–08:00 这段量会挂到前一天的柱子上。
// 这一条登记在待办清单 P72，本轮只把口径写明，不改分桶方式。
func GetMonitorTrend(c *gin.Context) {
	// 含今天在内的最近 trendDays 天，从最早一天零点起算（**平台时区**，与额度同一个日界）
	firstDay := utils.DayStart(trendDays)

	// 分桶在 Go 侧按平台时区做（待办清单 P72，2026-10-06 拍板选 A）。
	// 原来这一句是 `SELECT date(created_at) … GROUP BY date(created_at), source`，
	// 于是「一天从几点开始」这件事有第二处定义，而且它在三种方言里给出三个答案：
	// SQLite 把带 +08 的串**先归一到 UTC** 再取日（北京 00:00–08:00 挂到前一天的柱子上），
	// MySQL 按连接时区（生产 `loc=Local` 恰好一致，所以这条在生产没有症状），PostgreSQL 又是第三种。
	// 日界的唯一定义是 `utils.DayStart`（P71 收口），柱子的高度不能由另一个口径判——
	// 否则同一张图上日标轴与柱高来自两套日历，而它常常是排障时第一眼看的东话。
	// 代价写明：这里取回窗口内的明细行自己数（7 天 × 现网日均约 3.2k 行 ≈ 2.2 万行 × 4 列），
	// 换来的是跨方言不再依赖库，且读数与日标轴同一个日界。
	type detail struct {
		CreatedAt time.Time
		Source    string
		Status    int
		// *bool：后加的列在旧行是 NULL，`IS NOT TRUE` 那句判据的意思是"NULL 按纯 HTTP 口径算成功"。
		// 用非指针 bool 接会在 Scan 时就报"converting NULL to bool is unsupported"。
		InBandError *bool
	}
	var details []detail
	if err := db.DB.Model(&models.ApiCallLog{}).
		Select("created_at", "source", "status", "in_band_error").
		Where("created_at >= ?", firstDay).
		Scan(&details).Error; err != nil {
		log.Printf("ERROR: monitor trend query failed: %v", err)
		auth.Fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}

	type row struct {
		Day     string `json:"day"`
		Source  string `json:"source"`
		Total   int64  `json:"total"`
		Success int64  `json:"success"`
	}
	// 桶键 = 平台时区的 YYYY-MM-DD + "|" + source。日期串只由 `bkey` 生成一次，
	// 所以旧实现里那层"截前 10 字符对齐 MySQL parseTime 扫出来的 RFC3339"的补丁也不再需要——
	// 那个补丁本来就是库侧判日界的副作用之一。
	buckets := make(map[string]*row)
	bkey := func(t time.Time, source string) string {
		return t.In(utils.PlatformZone()).Format("2006-01-02") + "|" + source
	}
	// 成功 = 2xx 且非带内失败（P22）；库里 `in_band_error` 为 NULL 的旧行按纯 HTTP 口径算成功。
	// 判据写成"取出来才是 bool"而不是把 *bool 传下去：缓冲里那份本来就是 bool，
	// 两边都能用同一句判据，才不会又长出第二套成功定义。
	kept := func(status int, inBandError bool) bool {
		return status >= 200 && status < 300 && !inBandError
	}
	add := func(day time.Time, source string, ok bool) {
		k := bkey(day, source)
		r := buckets[k]
		if r == nil {
			r = &row{Day: day.In(utils.PlatformZone()).Format("2006-01-02"), Source: source}
			buckets[k] = r
		}
		r.Total++
		if ok {
			r.Success++
		}
	}
	for _, d := range details {
		add(d.CreatedAt, d.Source, kept(d.Status, d.InBandError != nil && *d.InBandError))
	}

	// 合并内存环形缓冲中尚未落库的明细：缓冲满 250 条才批量落库（或关停时
	// 兑底），低流量/刚重启时大部分调用只在内存，不合并趋势图会长期空白。
	// 淘汰即落库、落库即移出缓冲，两侧记录不重叠，无需去重。
	//
	// 这一侧也走同一个 `add`：它原来自己写 `rc.Time.Format("2006-01-02")`——按**进程时区**判日界，
	// 与库侧那一份是第三套口径（P71 数出来的那七种写法里就有它）。
	// 收口要一次收干净：明细与缓冲共用一个桶键生成器，才谈得上"日界只有一处定义"。
	for _, rc := range base.RecentCalls(0) {
		if rc.Time.Before(firstDay) {
			continue
		}
		add(rc.Time, rc.Source, kept(rc.Status, rc.InBandError))
	}

	rows := make([]row, 0, len(buckets))
	for _, r := range buckets {
		rows = append(rows, *r)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Day != rows[j].Day {
			return rows[i].Day < rows[j].Day
		}
		return rows[i].Source < rows[j].Source
	})

	// 生成连续天序列（无调用的天也要占位，前端柱状图不断档）
	days := make([]string, 0, trendDays)
	for i := 0; i < trendDays; i++ {
		days = append(days, firstDay.AddDate(0, 0, i).Format("2006-01-02"))
	}

	sourceSet := make(map[string]bool)
	for _, r := range rows {
		sourceSet[r.Source] = true
	}
	sources := make([]string, 0, len(sourceSet))
	for s := range sourceSet {
		sources = append(sources, s)
	}
	sort.Strings(sources)

	auth.Ok(c, gin.H{
		"days":    days,
		"sources": sources,
		"rows":    rows,
	})
}
