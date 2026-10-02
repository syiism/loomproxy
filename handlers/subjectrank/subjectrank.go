// Package subjectrank 内容维度榜单的聚合引擎。
//
// 单独成包是因为同一份聚合要被两个权限档位消费：管理端 /admin/monitor/subjects
// 看全维度全字段，公开榜单 /rank/boards 只给两个维度、只给名称与次数。
// 逻辑必须只有一份——口径分叉比多写一个包危险。
//
// 统计口径：库中未清理明细 + 内存环形缓冲里尚未落库的明细（淘汰即落库、落库即移出缓冲，两侧不重叠）。
package subjectrank

import (
	"errors"
	"sort"
	"strings"
	"time"

	"loomproxy/base"
	"loomproxy/db"
	"loomproxy/models"
)

// ErrUnknownDim 维度键不在白名单内
var ErrUnknownDim = errors.New("未知的统计维度")

// Dim 一个维度的列与过滤声明。列名只取自本包 Dims 表，调用方传进来的只是维度键，
// 拼进 SQL 的列名不是请求可控的；值一律走绑定参数。
type Dim struct {
	Column      string
	OnlyPresent bool     // 空值不入榜（搜索词/书名/章节名没抽到就是这次没发生）
	OnlyActions []string // 非空则只统计这些接口动作
	// PerSourceVisitors 该维度的**排名数字**用「同数据源内的访问人数」而不是请求次数。
	// 书名榜需要这条：一条 content 明细就是**一章正文**，一个人连读 30 章会被请求数记成 30，
	// 榜单于是回答的是「哪本书被翻的章节多」而不是「哪本书有人在读」——长篇天然占优，
	// 多人各读两章的书排在一个人独读二十章的书后面（待办清单 P18）。
	// 章节榜反过来：按章节请求数才是它要的东西，所以按维度声明，不做全局开关。
	PerSourceVisitors bool
}

// visitorExpr 访问者身份：注册用户用用户名，匿名退到 IP（与监控明细的标识口径一致）。
// 只用标准函数（NULLIF/COALESCE），SQLite/MySQL/PostgreSQL 都认；不写反引号（P1 的教训）。
const visitorExpr = "COALESCE(NULLIF(username, ''), ip)"

// Dims 维度白名单。
//
// book/chapter 只统计 content：一次「打开书目」会连着产生 detail 与多页 chapter
// （Legado 按 nextTocUrl 分页拉目录），全计入等于把同一本书凭空乘上几倍——
// 榜单回答「读了什么正文」，不是「点开了什么」。keyword 只有 search 会产生，
// media 要的是全量分布，二者不受动作限制。
var Dims = map[string]Dim{
	"keyword": {Column: "keyword", OnlyPresent: true},
	"book": {Column: "book_name", OnlyPresent: true, OnlyActions: []string{"content"},
		PerSourceVisitors: true},
	"chapter": {Column: "chapter_title", OnlyPresent: true, OnlyActions: []string{"content"}},
	"media":   {Column: "media", OnlyPresent: false},
}

// Item 一个维度条目的聚合结果（管理端字段齐全；公开榜只取 Name/Total，见 handlers/rank）。
// Total 是**排名用的那个数**：声明了 PerSourceVisitors 的维度是访问人数，其余是请求次数。
// 两个口径同时给出（Visitors / Requests），因为「人数」与「次数」谁更重要取决于问的人是谁，
// 让调用方挑，别让一个数字含糊地兼表两义。
type Item struct {
	Name         string     `json:"name"`
	Label        string     `json:"label,omitempty"`
	Total        int64      `json:"total"`
	Visitors     int64      `json:"visitors"` // 同数据源内去重的访问人数（跨源相加，见包注释）
	Requests     int64      `json:"requests"` // 请求次数（书名维度下即章节请求数）
	Success      int64      `json:"success"`
	Failed       int64      `json:"failed"`
	SuccessRate  float64    `json:"success_rate"`
	AvgLatencyMs int64      `json:"avg_latency_ms"`
	MaxLatencyMs int64      `json:"max_latency_ms"`
	EmptyResults int64      `json:"empty_results"`
	Sources      []string   `json:"sources"`
	LastCalledAt *time.Time `json:"last_called_at,omitempty"`
}

type agg struct {
	Name     string `gorm:"column:name"`
	Source   string `gorm:"column:source"`
	Total    int64  `gorm:"column:total"`
	Visitors int64  `gorm:"column:visitors"`
	Success  int64  `gorm:"column:success"`
	Latency  int64  `gorm:"column:latency"`
	Max      int64  `gorm:"column:max"`
	Empty    int64  `gorm:"column:empty_count"`
}

// NormalizeDays 收敛天数入参：越界回落 7，上限 365
func NormalizeDays(days int) int {
	if days < 1 || days > 365 {
		return 7
	}
	return days
}

// WindowStart 窗口起点：本地时区当日零点往前推 days-1 天
func WindowStart(days int) time.Time {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local).AddDate(0, 0, -(days - 1))
}

// ValidDim 维度键是否受支持
func ValidDim(key string) bool {
	_, ok := Dims[normalizeDim(key)]
	return ok
}

func normalizeDim(key string) string { return strings.ToLower(strings.TrimSpace(key)) }

// sourceAllowed 白名单命中判定（内存侧合并用，与 SQL 的 source IN (...) 同一口径）
func sourceAllowed(allowed []string, source string) bool {
	for _, a := range allowed {
		if a == source {
			return true
		}
	}
	return false
}

func actionAllowed(allowed []string, action string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, a := range allowed {
		if a == action {
			return true
		}
	}
	return false
}

// Query 聚合某维度在窗口内的榜单，按调用次数降序取前 limit 条（limit<=0 不截断）。
// sourceFilter 非空时只统计该数据源；mediaFilter 非空时只统计该媒介（一站多形态的源
// 不切开就没法看「同一本书在听书侧有多热」）；allowSources 非 nil 时把统计范围**钉死在这份白名单内**
// （含 sourceFilter 为空时的合并统计——否则「全部数据源」这一档会把未授权源的热度漏出去）。
// allowSources 为空切片表示零个源可见，非 nil 即生效；传 nil 表示不限制。
func Query(dimKey string, days int, sourceFilter, mediaFilter string, limit int, allowSources []string) ([]Item, error) {
	key := normalizeDim(dimKey)
	dim, ok := Dims[key]
	if !ok {
		return nil, ErrUnknownDim
	}
	from := WindowStart(days)
	cond, args := dimCondition(dim, from, sourceFilter, mediaFilter, allowSources)
	if cond == "" {
		return []Item{}, nil // 白名单是空集：没有任何源可见，不必查库
	}

	var aggs []agg
	if err := db.DB.Model(&models.ApiCallLog{}).
		Select(dim.Column+" AS name, source, COUNT(*) AS total, "+
			"COUNT(DISTINCT "+visitorExpr+") AS visitors, "+
			"SUM(CASE WHEN status >= 200 AND status < 300 THEN 1 ELSE 0 END) AS success, "+
			"COALESCE(SUM(latency_ms), 0) AS latency, COALESCE(MAX(latency_ms), 0) AS max, "+
			"SUM(CASE WHEN status >= 200 AND status < 300 AND result_count = 0 THEN 1 ELSE 0 END) AS empty_count").
		Where(cond, args...).
		Group(dim.Column + ", source").
		Scan(&aggs).Error; err != nil {
		return nil, err
	}

	type acc struct {
		requests, visitors, success, latency, max, empty int64
		sources                                          map[string]bool
	}
	merged := map[string]*acc{}
	take := func(name, src string, requests, visitors, success, latency, max, empty int64) *acc {
		a := merged[name]
		if a == nil {
			a = &acc{sources: map[string]bool{}}
			merged[name] = a
		}
		a.requests += requests
		a.visitors += visitors
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
		take(r.Name, r.Source, r.Total, r.Visitors, r.Success, r.Latency, r.Max, r.Empty)
	}

	// 合并内存中尚未落库的明细（缓冲满 250 条才批量落库，低流量时近期记录几乎都在内存里）
	pending := pendingCalls(dim, key, from, sourceFilter, mediaFilter, allowSources)
	// 人数不能直接相加：同一个人可能既有已落库的旧章节、又有还在环里没落库的新章节，
	// 各数一次就把一个人成两个人。所以两侧按 (名称, 源, 身份) 三元组去重，**所有维度同一口径**
	// （只有声明了 PerSourceVisitors 的维度拿它当排名数字，但 visitors 字段在哪都是「去重人数」）。
	// 代价是一次带 IN 列表的回查，规模受环形缓冲上界约束（200 条），且只在真有待落库明细时发生。
	seen := map[string]bool{}
	if len(pending) > 0 {
		seen = landedVisitors(dim, cond, args, pending)
	}
	for _, rc := range pending {
		var success, empty, visitors int64
		if rc.Status >= 200 && rc.Status < 300 {
			success = 1
			if rc.ResultCount == 0 {
				empty = 1
			}
		}
		id := visitorKey(rc.Name, rc.Source, rc.Who)
		if !seen[id] {
			seen[id] = true
			visitors = 1
		}
		take(rc.Name, rc.Source, 1, visitors, success, rc.LatencyMs, rc.LatencyMs, empty)
	}

	items := make([]Item, 0, len(merged))
	for name, a := range merged {
		if a.requests == 0 {
			continue
		}
		srcs := make([]string, 0, len(a.sources))
		for s := range a.sources {
			srcs = append(srcs, s)
		}
		sort.Strings(srcs)
		total := a.requests
		if dim.PerSourceVisitors {
			total = a.visitors
		}
		it := Item{
			Name: name, Total: total, Visitors: a.visitors, Requests: a.requests,
			Success: a.success, Failed: a.requests - a.success,
			// 成功率与平均耗时分位一律按**请求**算：人数当分母会把「一个人读三十章」
			// 的失败率稀释成三十分之一
			SuccessRate:  float64(a.success) / float64(a.requests) * 100,
			AvgLatencyMs: a.latency / a.requests, MaxLatencyMs: a.max,
			EmptyResults: a.empty, Sources: srcs,
		}
		if key == "media" {
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
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}

	for i := range items {
		var last models.ApiCallLog
		q := db.DB.Model(&models.ApiCallLog{}).
			Where("created_at >= ? AND "+dim.Column+" = ?", from, items[i].Name)
		if sourceFilter != "" {
			q = q.Where("source = ?", sourceFilter)
		}
		if mediaFilter != "" {
			q = q.Where("media = ?", mediaFilter)
		}
		if err := q.Order("id DESC").First(&last).Error; err == nil {
			t := last.CreatedAt
			items[i].LastCalledAt = &t
		}
	}
	return items, nil
}

// dimCondition 组装维度过滤条件（主聚合与「已落库身份」回查共用，保证两侧口径一致）。
// 返回的 cond 为空串表示范围被钉成空集，调用方直接返回空榜。
func dimCondition(dim Dim, from time.Time, sourceFilter, mediaFilter string, allowSources []string) (string, []interface{}) {
	cond := "created_at >= ?"
	args := []interface{}{from}
	if dim.OnlyPresent {
		cond += " AND " + dim.Column + " <> ''"
	}
	if sourceFilter != "" {
		cond += " AND source = ?"
		args = append(args, sourceFilter)
	}
	if mediaFilter != "" {
		cond += " AND media = ?"
		args = append(args, mediaFilter)
	}
	if allowSources != nil {
		if len(allowSources) == 0 {
			return "", nil
		}
		cond += " AND source IN (?" + strings.Repeat(",?", len(allowSources)-1) + ")"
		for _, s := range allowSources {
			args = append(args, s)
		}
	}
	if len(dim.OnlyActions) > 0 {
		cond += " AND action IN (?" + strings.Repeat(",?", len(dim.OnlyActions)-1) + ")"
		for _, a := range dim.OnlyActions {
			args = append(args, a)
		}
	}
	return cond, args
}

// pendingCall 环形缓冲里一条待落库明细的榜单视角视图
type pendingCall struct {
	Name, Source, Who string
	Status            int
	LatencyMs         int64
	ResultCount       int
}

// pendingCalls 取内存里落在窗口内、且通过维度过滤的明细
func pendingCalls(dim Dim, key string, from time.Time, sourceFilter, mediaFilter string, allowSources []string) []pendingCall {
	var out []pendingCall
	for _, rc := range base.RecentCalls(0) {
		if rc.Time.Before(from) || (sourceFilter != "" && rc.Source != sourceFilter) ||
			(mediaFilter != "" && rc.Media != mediaFilter) {
			continue
		}
		if allowSources != nil && !sourceAllowed(allowSources, rc.Source) {
			continue
		}
		if !actionAllowed(dim.OnlyActions, rc.Action) {
			continue
		}
		var val string
		switch key {
		case "keyword":
			val = rc.Keyword
		case "book":
			val = rc.BookName
		case "chapter":
			val = rc.ChapterTitle
		case "media":
			val = rc.Media
		}
		if dim.OnlyPresent && val == "" {
			continue
		}
		who := rc.Username
		if who == "" {
			who = rc.IP
		}
		out = append(out, pendingCall{
			Name: val, Source: rc.Source, Who: who,
			Status: rc.Status, LatencyMs: rc.LatencyMs, ResultCount: rc.ResultCount,
		})
	}
	return out
}

// landedVisitors 回查这些 (名称, 源) 下**已经落库**的访问者身份。
// 只按内存里出现过的名称过滤，所以结果规模远小于全表；用 DISTINCT 而不是 GROUP BY，
// 免得依赖「按输出列别名分组」这种各方言支持不一的写法。
func landedVisitors(dim Dim, cond string, args []interface{}, pending []pendingCall) map[string]bool {
	names := make([]string, 0, len(pending))
	seenName := map[string]bool{}
	for _, rc := range pending {
		if !seenName[rc.Name] {
			seenName[rc.Name] = true
			names = append(names, rc.Name)
		}
	}
	if len(names) == 0 {
		return map[string]bool{}
	}
	type triple struct {
		Name   string `gorm:"column:name"`
		Source string `gorm:"column:source"`
		Who    string `gorm:"column:who"`
	}
	var rows []triple
	q := db.DB.Model(&models.ApiCallLog{}).
		Select("DISTINCT "+dim.Column+" AS name, source, "+visitorExpr+" AS who").
		Where(cond, args...).
		Where(dim.Column+" IN (?)", names)
	if err := q.Scan(&rows).Error; err != nil {
		return map[string]bool{} // 回查失败只影响去重精度（最坏把一个人多算一次），不该让榜单整体失败
	}
	seen := make(map[string]bool, len(rows))
	for _, r := range rows {
		seen[visitorKey(r.Name, r.Source, r.Who)] = true
	}
	return seen
}

func visitorKey(name, source, who string) string {
	return name + "\x00" + source + "\x00" + who
}
