// Package subjectrank 内容维度榜单的聚合引擎。
//
// 单独成包是因为同一份聚合要被两个权限档位消费：管理端 /admin/monitor/subjects
// 看全维度全字段，公开榜单 /rank/boards 只给两个维度，字段收窄到名称、次数与（阅读榜的）书目标识。
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
	// BookID 只在书名维度下填：该书名最近一次调用所带的书目标识（`book_ident`）。
	// 存在的理由是**检索**而不是展示——面板不显示它，但拿着榜单结果去查这本书的人需要标识：
	// 上游书名可能重名、也只支持按 id 取详情与正文。取「最近一条非空标识」是因为旧行可能
	// 只记到名称（升级前写入）或反之，最近的那条才是当前还在服务这本书的那个标识。
	// 公开榜 `/rank/boards` 的阅读榜带同一个值（见 handlers/rank 的 boardEntry：那里只给名称、
	// 次数与这一个标识，不给时间与数据源归属）。
	BookID string `json:"book_id,omitempty"`
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
			// 成功 = 2xx 且非带内失败（P22）；IS NOT TRUE 对 NULL 成立，旧行仍按纯 HTTP 口径
			"SUM(CASE WHEN status >= 200 AND status < 300 AND in_band_error IS NOT TRUE THEN 1 ELSE 0 END) AS success, "+
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
		if rc.Status >= 200 && rc.Status < 300 && !rc.InBandError {
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
		if key == "book" {
			// 书目标识另取「最近一条带标识的」：LastCalledAt 要的是任何一条正文明细，
			// 两者问的不是同一件事，混在一个查询里会让其中一个说谎
			var withIdent models.ApiCallLog
			iq := db.DB.Model(&models.ApiCallLog{}).
				Where("created_at >= ? AND "+dim.Column+" = ? AND book_ident <> ''", from, items[i].Name)
			if sourceFilter != "" {
				iq = iq.Where("source = ?", sourceFilter)
			}
			if mediaFilter != "" {
				iq = iq.Where("media = ?", mediaFilter)
			}
			if err := iq.Order("id DESC").First(&withIdent).Error; err == nil {
				items[i].BookID = withIdent.BookIdent
			}
		}
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
	InBandError       bool
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
			Status: rc.Status, InBandError: rc.InBandError,
			LatencyMs: rc.LatencyMs, ResultCount: rc.ResultCount,
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

// —— 内容维度覆盖率（监控的自我观测）———————————————————————————————
//
// 这块存在的理由很实际：上面那几条口径（人数、book_id）出问题时，全靠人肉写 SQL 才发现。
// 而人肉核对本身也容易得错结论：`book_ident`/`chapter_ident` 是后加的列，旧行是 **NULL**；
// 再叠上「老二进制根本不写这列」的历史段，一条 `WHERE col = ''` 的缺失数会同时漏掉这两种行，
// 一条跨升级点的窗口又会把"升级前的空"读成"这个源采不到"（我就这么误判过一次，
// 把 99% 的源读成 50%）。所以这里只统计**有**的条数（`COALESCE(col,'') <> ''` 统一 NULL 与空串），
// 缺失一律由 `rows - 有` 推出来——不给两种空值各留一条口径的机会。
// 「哪个动作本就不该有书名」也随数据下发（`expect_book`）：search/explore 返回的是一批书，
// 没有"这一本"可言，它们的 0% 是正常状态，不该标红。
// 失败请求（HTTP 失败与带内失败，见 `failed`/`in_band_failed`）天然五维全空，
// 它们是「缺格子的合理成因」而不是「采集在漏」的证据——读格子先看这两个数。
//
// 口径是**已落库明细**：环形缓冲里那 ≤200 条不在此列（这条统计是健康度，不是实时榜）。

// CoverageRow 一个「数据源 × 接口」的内容维度覆盖情况。
type CoverageRow struct {
	Source string `json:"source"`
	Action string `json:"action"`
	// 别名列取 rows_ 而不是 rows：`ROWS` 是 MySQL 8 的保留字（SQLite 容忍、只有生产会炸，
	// 见待办清单 P1/P15 那条教训）
	Rows            int64 `gorm:"column:rows_" json:"rows"`
	HasBookName     int64 `json:"has_book_name"`
	HasBookIdent    int64 `json:"has_book_ident"`
	HasChapterTitle int64 `json:"has_chapter_title"`
	HasKeyword      int64 `json:"has_keyword"`
	HasMedia        int64 `json:"has_media"`
	// Failed 失败请求数 = HTTP 层失败 + 带内失败（P22：200 但正文是错误载荷的请求，
	// 只有标出来覆盖率才读得懂——一格不满可能只是请求在失败，不是采集在漏）。
	// InBandFailed 是其中带内的那部分；两者之差才是真正的 HTTP 层失败。
	Failed int64 `json:"failed"`
	// Withheld 因用户关闭留存而主动不捕获的行数（待办清单 P37）。它们不进 rows_、也不进 failed——
	// 这一列的存在就是为了让「分母为什么变小」有地方可查，而不是靠人猜。
	Withheld     int64 `json:"withheld"`
	InBandFailed int64 `json:"in_band_failed"`
	// ExpectBook 该动作是否理应记到书名/书目标识：为真才谈得上"缺失"。
	// 面板据此决定要不要标红，而不是对着一片合法的 0% 报警。
	ExpectBook bool `json:"expect_book"`
}

// bookExpectedActions 理应带书名与书目标识的动作。搜索与发现返回的是书目列表，
// 单一书名无从谈起（它们的维度是 keyword），所以留空是正常状态。
var bookExpectedActions = map[string]bool{"detail": true, "chapter": true, "content": true}

// CoverageActionOrder 面板展示顺序（按调用链的先后，不按字母）
var CoverageActionOrder = []string{"search", "explore", "detail", "chapter", "content"}

// Coverage 按 源×动作 统计窗口内的内容维度填充情况（min 条数以下的不返回，避免零星请求刷出 0%）。
// sourceFilter 非空时只看那一个源；allowSources 语义与 Query 一致（钉死可见范围）。
func Coverage(days int, sourceFilter string, allowSources []string) ([]CoverageRow, error) {
	from := WindowStart(days)
	cond := "created_at >= ?"
	args := []interface{}{from}
	if sourceFilter != "" {
		cond += " AND source = ?"
		args = append(args, sourceFilter)
	}
	if allowSources != nil {
		if len(allowSources) == 0 {
			return []CoverageRow{}, nil
		}
		cond += " AND source IN (?" + strings.Repeat(",?", len(allowSources)-1) + ")"
		for _, s := range allowSources {
			args = append(args, s)
		}
	}

	var rows []CoverageRow
	// 一律 COALESCE：这三列都出现过 NULL（列是后加的，旧行没有默认值），
	// 直接 <> '' 会把 NULL 行算成"没采到"，而这类误判的代价是有人去"修一个不存在的问题"
	if err := db.DB.Model(&models.ApiCallLog{}).
		// 明细数与失败数都**只数没退出留存的行**（待办清单 P37）：用户关掉同意位之后那几列本来就是空的，
		// 把它们算进分母等于用用户的合规选择去扣采集的分——下一个读覆盖率的人会去「修一个不存在的问题」。
		// 判据用 IS TRUE / IS NOT TRUE 而不是 = 0/1：这一列可能为 NULL（列是后加的），
		// 与上面 in_band_error 同一套写法（P22 那条踩过的坑）。
		Select("source, action, "+
			"SUM(CASE WHEN content_withheld IS NOT TRUE THEN 1 ELSE 0 END) AS rows_, "+
			"SUM(CASE WHEN content_withheld IS TRUE THEN 1 ELSE 0 END) AS withheld, "+
			"SUM(CASE WHEN COALESCE(book_name,'') <> '' THEN 1 ELSE 0 END) AS has_book_name, "+
			"SUM(CASE WHEN COALESCE(book_ident,'') <> '' THEN 1 ELSE 0 END) AS has_book_ident, "+
			"SUM(CASE WHEN COALESCE(chapter_title,'') <> '' THEN 1 ELSE 0 END) AS has_chapter_title, "+
			"SUM(CASE WHEN COALESCE(keyword,'') <> '' THEN 1 ELSE 0 END) AS has_keyword, "+
			"SUM(CASE WHEN COALESCE(media,'') <> '' THEN 1 ELSE 0 END) AS has_media, "+
			// IS TRUE 对 NULL 成立：旧行没有这一列的值，只按 status >= 400 计失败
			"SUM(CASE WHEN (status >= 400 OR in_band_error IS TRUE) AND content_withheld IS NOT TRUE THEN 1 ELSE 0 END) AS failed, "+
			"SUM(CASE WHEN in_band_error IS TRUE THEN 1 ELSE 0 END) AS in_band_failed").
		Where(cond, args...).
		Group("source, action").
		Scan(&rows).Error; err != nil {
		return nil, err
	}

	out := rows[:0]
	for _, r := range rows {
		if r.Rows < 5 { // 零星请求的百分比只会制造噪声（0% 或 100% 都不说明问题）
			continue
		}
		r.ExpectBook = bookExpectedActions[r.Action]
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		oi, oj := actionOrderIndex(out[i].Action), actionOrderIndex(out[j].Action)
		if oi != oj {
			return oi < oj
		}
		if out[i].Rows != out[j].Rows {
			return out[i].Rows > out[j].Rows
		}
		return out[i].Source < out[j].Source
	})
	return out, nil
}

func actionOrderIndex(action string) int {
	for i, a := range CoverageActionOrder {
		if a == action {
			return i
		}
	}
	return len(CoverageActionOrder)
}
