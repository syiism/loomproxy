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
	// 带内失败的业务码等值筛选（待办清单 P122）：取证「站方到底回了哪一档」用。
	// 只在带内失败的行上有值，筛它天然把范围缩到带内失败；等值而不是 contains——
	// 码是一档一档的枚举，不是文本
	if v := c.Query("in_band_code"); v != "" {
		q = q.Where("in_band_code = ?", v)
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

// TrendRow 一天 × 一个数据源的窗口聚合。字段与 json 标签是 `/admin/monitor/trend` 的响应形状，
// 抽函数那一步（报表要复用同一份聚合）特意一字未改——三个钉住日界的用例都隔着 HTTP 断言它。
type TrendRow struct {
	Day     string `json:"day"`
	Source  string `json:"source"`
	Total   int64  `json:"total"`
	Success int64  `json:"success"`
}

// TrendActionRow 窗口内「源 × 接口」的聚合，报表那一块长尾用。
// 它不是第二次查询：与 TrendRow 同一次扫描、同一个 kept 判据，只是换个键再数一遍。
type TrendActionRow struct {
	Source  string `json:"source"`
	Action  string `json:"action"`
	Total   int64  `json:"total"`
	Success int64  `json:"success"`
}

// TrendData 一次窗口聚合的全部读数。
type TrendData struct {
	Days     []string         // 连续天序列（无调用的天占位，图上不断档）
	Sources  []string         // 窗口内出现过的源，字典序
	Rows     []TrendRow       // 天 × 源
	ByAction []TrendActionRow // 源 × 接口
}

// GetMonitorTrend 近 trendDays 天调用趋势的 HTTP 门面：读数全部来自 MonitorTrend，这里只管成败与出口。
// 报表（GetMonitorReport）调的是同一个 MonitorTrend——日界与成功判据因此不可能有第二份。
func GetMonitorTrend(c *gin.Context) {
	t, err := MonitorTrend()
	if err != nil {
		log.Printf("ERROR: monitor trend query failed: %v", err)
		auth.Fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}
	auth.Ok(c, gin.H{"days": t.Days, "sources": t.Sources, "rows": t.Rows})
}

// MonitorTrend 聚合近 trendDays 天的调用明细：按天 × 数据源（另出一份源 × 接口给报表）。
func MonitorTrend() (*TrendData, error) {
	// 含今天在内的最近 trendDays 天，从最早一天零点起算（**平台时区**，与额度同一个日界）。
	// api_call_stats 永久归档不带时间维度，按天趋势只能来自尚未清理的明细；
	// 这一条登记在待办清单 P72，本轮只把口径写明，不改分桶方式。
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
		Action    string
		Status    int
		// *bool：后加的列在旧行是 NULL，`IS NOT TRUE` 那句判据的意思是"NULL 按纯 HTTP 口径算成功"。
		// 用非指针 bool 接会在 Scan 时就报"converting NULL to bool is unsupported"。
		InBandError *bool
	}
	var details []detail
	if err := db.DB.Model(&models.ApiCallLog{}).
		Select("created_at", "source", "action", "status", "in_band_error").
		Where(db.SinceCond("created_at", firstDay)).
		Scan(&details).Error; err != nil {
		return nil, err
	}

	// 桶键 = 平台时区的 YYYY-MM-DD + "|" + source。日期串只由 `bkey` 生成一次，
	// 所以旧实现里那层"截前 10 字符对齐 MySQL parseTime 扫出来的 RFC3339"的补丁也不再需要——
	// 那个补丁本来就是库侧判日界的副作用之一。
	buckets := make(map[string]*TrendRow)
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
			r = &TrendRow{Day: day.In(utils.PlatformZone()).Format("2006-01-02"), Source: source}
			buckets[k] = r
		}
		r.Total++
		if ok {
			r.Success++
		}
	}
	// 源 × 接口那一份是**同一次扫描**换个键再数一遍：不新增查询、不新增日界、不新增成功定义。
	// 报表的逐接口长尾要用它，而它绝不能长成第二个聚合口径。
	actBuckets := make(map[string]*TrendActionRow)
	addAction := func(source, action string, ok bool) {
		k := source + "|" + action
		a := actBuckets[k]
		if a == nil {
			a = &TrendActionRow{Source: source, Action: action}
			actBuckets[k] = a
		}
		a.Total++
		if ok {
			a.Success++
		}
	}
	for _, d := range details {
		ok := kept(d.Status, d.InBandError != nil && *d.InBandError)
		add(d.CreatedAt, d.Source, ok)
		addAction(d.Source, d.Action, ok)
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
		ok := kept(rc.Status, rc.InBandError)
		add(rc.Time, rc.Source, ok)
		addAction(rc.Source, rc.Action, ok)
	}

	rows := make([]TrendRow, 0, len(buckets))
	for _, r := range buckets {
		rows = append(rows, *r)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Day != rows[j].Day {
			return rows[i].Day < rows[j].Day
		}
		return rows[i].Source < rows[j].Source
	})

	// 生成连续天序列（无调用的天也要占位，报表的逐日列与迷你柱不能断档）
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

	byAction := make([]TrendActionRow, 0, len(actBuckets))
	for _, a := range actBuckets {
		byAction = append(byAction, *a)
	}
	sort.Slice(byAction, func(i, j int) bool {
		if byAction[i].Source != byAction[j].Source {
			return byAction[i].Source < byAction[j].Source
		}
		return byAction[i].Action < byAction[j].Action
	})

	return &TrendData{Days: days, Sources: sources, Rows: rows, ByAction: byAction}, nil
}
