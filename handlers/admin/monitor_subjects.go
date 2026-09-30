package admin

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"loomproxy/base"
	"loomproxy/db"
	"loomproxy/handlers/auth"
	"loomproxy/models"
)

// escapeLike 转义 LIKE 模式里的通配符：用户搜「100%」不该把全表匹配进来。
// 转义字符用反斜杠（SQLite/MySQL/PostgreSQL 的 ESCAPE '\' 语义一致）
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// subjectDim 内容维度榜单的维度定义。column 只取自这张代码内白名单表，请求只能选 dim 键，
// 因而拼进 SQL 的列名不是请求可控的（值一律走绑定参数）。
//
// onlyPresent=true 表示空值不入榜：搜索词/书名/章节名没抽到就是这次没发生。
// 媒介例外——空值要作为「未判定」桶留下，它是「源还没声明媒介」的可见信号。
type subjectDim struct {
	column      string
	onlyPresent bool
}

var subjectDims = map[string]subjectDim{
	"keyword": {column: "keyword", onlyPresent: true},
	"book":    {column: "book_name", onlyPresent: true},
	"chapter": {column: "chapter_title", onlyPresent: true},
	"media":   {column: "media", onlyPresent: false},
}

type subjectItem struct {
	Name         string     `json:"name"`
	Label        string     `json:"label,omitempty"` // 媒介维度的中文展示名
	Total        int64      `json:"total"`
	Success      int64      `json:"success"`
	Failed       int64      `json:"failed"`
	SuccessRate  float64    `json:"success_rate"`
	AvgLatencyMs int64      `json:"avg_latency_ms"`
	MaxLatencyMs int64      `json:"max_latency_ms"`
	EmptyResults int64      `json:"empty_results"` // 2xx 但结果数为 0（搜了个寂寞）
	Sources      []string   `json:"sources"`
	LastCalledAt *time.Time `json:"last_called_at,omitempty"`
}

// subjectAgg 按 名称 × 源 分组的中间行（源维度保留，最后合成 Sources 集合）
type subjectAgg struct {
	Name    string `json:"name"`
	Source  string `json:"source"`
	Total   int64  `json:"total"`
	Success int64  `json:"success"`
	Latency int64  `json:"latency"`
	Max     int64  `json:"max"`
	Empty   int64  `json:"empty"`
}

// GetMonitorSubjects 内容维度榜单：某个搜索词/书名/章节/媒介在窗口内被调用得怎么样。
// 口径与 /monitor 一致——库中未清理明细 + 内存环形缓冲里尚未落库的明细（两侧不重叠）。
func GetMonitorSubjects(c *gin.Context) {
	dimKey := strings.ToLower(strings.TrimSpace(c.Query("dim")))
	dim, ok := subjectDims[dimKey]
	if !ok {
		auth.Fail(c, http.StatusBadRequest, "dim 只接受 keyword/book/chapter/media")
		return
	}

	days, _ := strconv.Atoi(c.DefaultQuery("days", "7"))
	if days < 1 {
		days = 7
	}
	if days > 365 {
		days = 365
	}
	now := time.Now()
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local).AddDate(0, 0, -(days - 1))
	sourceFilter := c.Query("source")

	// 列名来自上面的白名单表，取值全部走占位参数
	cond := "created_at >= ?"
	args := []interface{}{from}
	if dim.onlyPresent {
		cond += " AND " + dim.column + " <> ''"
	}
	if sourceFilter != "" {
		cond += " AND source = ?"
		args = append(args, sourceFilter)
	}

	var aggs []subjectAgg
	if err := db.DB.Model(&models.ApiCallLog{}).
		Select(dim.column+" AS name, source, COUNT(*) AS total, "+
			"SUM(CASE WHEN status >= 200 AND status < 300 THEN 1 ELSE 0 END) AS success, "+
			"COALESCE(SUM(latency_ms), 0) AS latency, COALESCE(MAX(latency_ms), 0) AS max, "+
			"SUM(CASE WHEN status >= 200 AND status < 300 AND result_count = 0 THEN 1 ELSE 0 END) AS empty").
		Where(cond, args...).
		Group(dim.column + ", source").
		Scan(&aggs).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "数据库错误: "+err.Error())
		return
	}

	type acc struct {
		total, success, latency, max, empty int64
		sources                             map[string]bool
	}
	merged := map[string]*acc{}
	take := func(name, src string, total, success, latency, max, empty int64) *acc {
		a := merged[name]
		if a == nil {
			a = &acc{sources: map[string]bool{}}
			merged[name] = a
		}
		a.total += total
		a.success += success
		a.latency += latency
		a.empty += empty
		if max > a.max {
			a.max = max
		}
		if src != "" {
			a.sources[src] = true
		}
		return a
	}
	for _, r := range aggs {
		take(r.Name, r.Source, r.Total, r.Success, r.Latency, r.Max, r.Empty)
	}

	// 合并内存中尚未落库的明细：缓冲满 250 条才批量落库，低流量时近期记录几乎都在内存里
	for _, rc := range base.RecentCalls(0) {
		if rc.Time.Before(from) || (sourceFilter != "" && rc.Source != sourceFilter) {
			continue
		}
		var val string
		switch dimKey {
		case "keyword":
			val = rc.Keyword
		case "book":
			val = rc.BookName
		case "chapter":
			val = rc.ChapterTitle
		case "media":
			val = rc.Media
		}
		if dim.onlyPresent && val == "" {
			continue
		}
		var success, empty int64
		if rc.Status >= 200 && rc.Status < 300 {
			success = 1
			if rc.ResultCount == 0 {
				empty = 1
			}
		}
		take(val, rc.Source, 1, success, rc.LatencyMs, rc.LatencyMs, empty)
	}

	items := make([]subjectItem, 0, len(merged))
	for name, a := range merged {
		if a.total == 0 {
			continue
		}
		srcs := make([]string, 0, len(a.sources))
		for s := range a.sources {
			srcs = append(srcs, s)
		}
		sort.Strings(srcs)
		it := subjectItem{
			Name:         name,
			Total:        a.total,
			Success:      a.success,
			Failed:       a.total - a.success,
			SuccessRate:  float64(a.success) / float64(a.total) * 100,
			AvgLatencyMs: a.latency / a.total,
			MaxLatencyMs: a.max,
			EmptyResults: a.empty,
			Sources:      srcs,
		}
		if dimKey == "media" {
			it.Label = base.MediaLabel(name)
		}
		items = append(items, it)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Total != items[j].Total {
			return items[i].Total > items[j].Total
		}
		return items[i].Name < items[j].Name
	})
	const subjectsLimit = 50
	if len(items) > subjectsLimit {
		items = items[:subjectsLimit]
	}

	// 最近调用时间：只回查榜上的前 50 条（单行查询，面板低频，够用）
	for i := range items {
		var last models.ApiCallLog
		q := db.DB.Model(&models.ApiCallLog{}).
			Where("created_at >= ? AND "+dim.column+" = ?", from, items[i].Name)
		if sourceFilter != "" {
			q = q.Where("source = ?", sourceFilter)
		}
		if err := q.Order("id DESC").First(&last).Error; err == nil {
			t := last.CreatedAt
			items[i].LastCalledAt = &t
		}
	}

	books, chapters := base.NameCacheStats()
	auth.Ok(c, gin.H{
		"dim":        dimKey,
		"days":       days,
		"from":       from,
		"items":      items,
		"name_cache": gin.H{"books": books, "chapters": chapters},
	})
}
