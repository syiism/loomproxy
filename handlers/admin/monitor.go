package admin

import (
	"log"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"loomproxy-go/base"
	"loomproxy-go/db"
	"loomproxy-go/handlers/auth"
	"loomproxy-go/models"
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
// + 保留期内尚未清理的明细（api_call_logs）
func lifetimeCounts() map[string]int64 {
	m := make(map[string]int64)
	var stats []models.ApiCallStat
	db.DB.Find(&stats)
	for _, s := range stats {
		m[s.Source+"/"+s.Action] += s.Total
	}
	type row struct {
		Source string
		Action string
		Total  int64
	}
	var rows []row
	db.DB.Model(&models.ApiCallLog{}).
		Select("source, action, COUNT(*) AS total").
		Group("source, action").
		Scan(&rows)
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
		"started_at":     started,
		"total":          total,
		"success":        success,
		"failed":         total - success,
		"success_rate":   overallRate,
		"lifetime_total": lifetimeTotal,
		"items":          rows,
		"recent":         base.RecentCalls(50),
	})
}

// ResetMonitor 清零监控计数并重置统计起点
func ResetMonitor(c *gin.Context) {
	base.ResetMetrics()
	auth.Ok(c, gin.H{"message": "监控计数已清零"})
}

// GetMonitorHistory 分页查询历史调用明细（api_call_logs，最新在前）
func GetMonitorHistory(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	q := db.DB.Model(&models.ApiCallLog{})
	if src := c.Query("source"); src != "" {
		q = q.Where("source = ?", src)
	}
	if username := c.Query("username"); username != "" {
		q = q.Where("username = ?", username)
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

// trendDays 趋势图覆盖的天数（与 api_call_logs 明细保留期 7 天一致）
const trendDays = 7

// GetMonitorTrend 近 7 天调用趋势：按天 × 数据源聚合 api_call_logs 明细。
// 口径说明：api_call_stats 永久归档不带时间维度，按天趋势只能来自保留期内的明细；
// 日期分桶用服务器本地时区（与明细 created_at 口径一致）。
func GetMonitorTrend(c *gin.Context) {
	// 日期函数按方言分流（postgres 没有 date(col) 函数，用 ::date 转换）
	dateExpr := "date(created_at)"
	if db.DB.Dialector.Name() == "postgres" {
		dateExpr = "created_at::date"
	}

	// 含今天在内的最近 trendDays 天，从最早一天零点起算（本地时区）
	now := time.Now()
	firstDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local).AddDate(0, 0, -(trendDays - 1))

	type row struct {
		Day     string `json:"day"`
		Source  string `json:"source"`
		Total   int64  `json:"total"`
		Success int64  `json:"success"`
	}
	var rows []row
	if err := db.DB.Model(&models.ApiCallLog{}).
		Select(dateExpr+" AS day, source, COUNT(*) AS total, "+
			"SUM(CASE WHEN status >= 200 AND status < 300 THEN 1 ELSE 0 END) AS success").
		Where("created_at >= ?", firstDay).
		Group(dateExpr + ", source").
		Scan(&rows).Error; err != nil {
		log.Printf("ERROR: monitor trend query failed: %v", err)
		auth.Fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}

	// 统一日期格式为 YYYY-MM-DD：MySQL 驱动 parseTime=True 会把 date() 结果
	// 扫成 time.Time 再序列化为 RFC3339（2026-08-01T00:00:00Z），与 days 序列
	// 及前端分桶键（YYYY-MM-DD）不匹配会导致历史柱全部不渲染（SQLite 返回
	// 文本无此问题）；截断前 10 字符对齐
	for i := range rows {
		if len(rows[i].Day) > 10 {
			rows[i].Day = rows[i].Day[:10]
		}
	}

	// 合并内存环形缓冲中尚未落库的明细：缓冲满 250 条才批量落库（或关停时
	// 兑底），低流量/刚重启时大部分调用只在内存，不合并趋势图会长期空白。
	// 淘汰即落库、落库即移出缓冲，两侧记录不重叠，无需去重
	merged := make(map[string]int, len(rows))
	for i, r := range rows {
		merged[r.Day+"|"+r.Source] = i
	}
	for _, rc := range base.RecentCalls(0) {
		if rc.Time.Before(firstDay) {
			continue
		}
		key := rc.Time.Format("2006-01-02") + "|" + rc.Source
		if idx, ok := merged[key]; ok {
			rows[idx].Total++
			if rc.Status >= 200 && rc.Status < 300 {
				rows[idx].Success++
			}
		} else {
			var success int64
			if rc.Status >= 200 && rc.Status < 300 {
				success = 1
			}
			merged[key] = len(rows)
			rows = append(rows, row{
				Day:     rc.Time.Format("2006-01-02"),
				Source:  rc.Source,
				Total:   1,
				Success: success,
			})
		}
	}

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
