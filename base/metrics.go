package base

import (
	"sync"
	"time"
)

// ActionMetrics 单个数据源接口的调用指标
type ActionMetrics struct {
	Total          int64      `json:"total"`
	Success        int64      `json:"success"`
	Failed         int64      `json:"failed"`
	TotalLatencyMs int64      `json:"-"`
	AvgLatencyMs   int64      `json:"avg_latency_ms"`
	MaxLatencyMs   int64      `json:"max_latency_ms"`
	LastCalledAt   *time.Time `json:"last_called_at"`
	LastStatus     int        `json:"last_status"`
}

// RecentCall 一次调用的明细记录（供监控页展示最近调用）
type RecentCall struct {
	Time     time.Time `json:"time"`
	Username string    `json:"username"`
	IP       string    `json:"ip"`
	Source   string    `json:"source"`
	Action   string    `json:"action"`
	Status   int       `json:"status"`
	// InBandError 带内失败（P22）：HTTP 200 但正文是 ContentType=="error" 的错误载荷。
	// 监控读数把它计入失败；HTTP 状态、IP 封禁计数与额度计费不受它影响。
	InBandError bool  `json:"in_band_error,omitempty"`
	LatencyMs   int64 `json:"latency_ms"`
	// 内容维度（由 legado.ObserveCall 从规范化响应回填；没抽到就是空串）
	Keyword      string `json:"keyword,omitempty"`
	BookName     string `json:"book_name,omitempty"`
	ChapterTitle string `json:"chapter_title,omitempty"`
	// 标识与名称一起记：正文/目录请求只带 bookId/itemId，名称要靠进程内命名缓存反查，
	// 重启后缓存空了就只能查到标识。名字当时补不上不代表永远补不上——
	// 标识落库后，缓存重新建立起来时可以按 (source, 标识) 回填一次（见 /admin/monitor/backfill-subjects）。
	BookIdent    string `json:"book_ident,omitempty"`
	ChapterIdent string `json:"chapter_ident,omitempty"`
	Media        string `json:"media,omitempty"` // 枚举见 media.go；空 = 未判定
	ResultCount  int    `json:"result_count"`
}

// recentCallsCap 最近调用明细的内存保留条数（环形缓冲）
const recentCallsCap = 200

// flushBatchSize 淘汰批量：缓冲满 cap+batch 时，最旧的 batch 条一次性交给
// flusher 落库（避免每条请求都触发一次数据库写入）
const flushBatchSize = 50

// 数据源接口调用指标收集：纯内存计数（重启清零聚合值），与额度计费相互独立，
// 统计到达路由的全部请求（含鉴权失败之外的 403/429/5xx 等失败响应）。
// 明细超出内存容量时通过 flusher 批量落库，内存占用恒定有界。
var metricsState = struct {
	sync.Mutex
	started time.Time
	actions map[string]map[string]*ActionMetrics // source -> action -> 指标
	recent  []RecentCall                         // 最近调用明细（最旧在前）
	flusher func([]RecentCall)                   // 淘汰/关停时的落库回调（应用层注入）
	// flushed 本会话已淘汰落库的明细计数（source -> action -> n）。
	// 会话聚合计数（actions）含已落库部分，lifetime 口径 = 归档 + 明细表 +
	// 未落库部分 = 归档 + 明细表 + (会话聚合 - flushed)，不扣除会双计
	flushed map[string]map[string]int64
}{started: time.Now(), actions: make(map[string]map[string]*ActionMetrics), flushed: make(map[string]map[string]int64)}

// flushWG 跟踪飞行中的异步批量落库：淘汰即移出环形缓冲，若关停时不等待
// 这批飞行中的落库完成，该批明细将永久丢失（曾致重启后 lifetime 少 50）
var flushWG sync.WaitGroup

// SetMetricsFlusher 注册明细落库回调（须在启动时、请求进入前调用）
func SetMetricsFlusher(f func([]RecentCall)) {
	metricsState.Lock()
	defer metricsState.Unlock()
	metricsState.flusher = f
}

// RecordCall 记录一次数据源接口调用（status 为最终 HTTP 状态码，2xx 视为成功；
// username 为调用者用户名，匿名/API Key 调用传空串；ip 为调用者客户端 IP；
// subject 是本次调用的内容维度（搜索词/书名/章节/媒介/结果数），可为 nil——那时各维度留空）
func RecordCall(source, action, username, ip string, status int, latency time.Duration, subject *CallSubject) {
	ms := latency.Milliseconds()
	now := time.Now()
	metricsState.Lock()
	am := metricsState.actions[source]
	if am == nil {
		am = make(map[string]*ActionMetrics)
		metricsState.actions[source] = am
	}
	m := am[action]
	if m == nil {
		m = &ActionMetrics{}
		am[action] = m
	}
	m.Total++
	// 失败 = HTTP 层失败 + 带内失败（P22：200 但正文是 ContentType=="error" 的错误载荷，
	// 只可能出现在拿到了规范化响应的请求上）。只影响监控读数；IP 封禁与计费仍只看 HTTP。
	inBand := subject != nil && subject.InBandError
	if status >= 200 && status < 300 && !inBand {
		m.Success++
	} else {
		m.Failed++
	}
	m.TotalLatencyMs += ms
	if ms > m.MaxLatencyMs {
		m.MaxLatencyMs = ms
	}
	m.LastCalledAt = &now
	m.LastStatus = status

	rc := RecentCall{
		Time:        now,
		Username:    username,
		IP:          ip,
		Source:      source,
		Action:      action,
		Status:      status,
		InBandError: inBand,
		LatencyMs:   ms,
	}
	if subject != nil {
		rc.Keyword = subject.Keyword
		rc.BookName = subject.BookName
		rc.ChapterTitle = subject.ChapterTitle
		rc.BookIdent = subject.BookKey
		rc.ChapterIdent = subject.ChapterKey
		rc.Media = subject.Media
		rc.ResultCount = subject.ResultCount
	}
	metricsState.recent = append(metricsState.recent, rc)
	// 缓冲达到 cap+batch：淘汰最旧 batch 条，异步批量落库（不阻塞请求路径）
	var evicted []RecentCall
	var flusher func([]RecentCall)
	if len(metricsState.recent) >= recentCallsCap+flushBatchSize {
		evicted = append([]RecentCall(nil), metricsState.recent[:flushBatchSize]...)
		metricsState.recent = metricsState.recent[flushBatchSize:]
		flusher = metricsState.flusher
		for _, rc := range evicted {
			fam := metricsState.flushed[rc.Source]
			if fam == nil {
				fam = make(map[string]int64)
				metricsState.flushed[rc.Source] = fam
			}
			fam[rc.Action]++
		}
	}
	metricsState.Unlock()
	if flusher != nil && len(evicted) > 0 {
		flushWG.Add(1)
		go func() {
			defer flushWG.Done()
			flusher(evicted)
		}()
	}
}

// CallsRecordedSince 返回内存环形缓冲中时间不早于 t 的调用条数
// （已被淘汰落库的记录不在其中，调用方需自行叠加数据库统计）
func CallsRecordedSince(t time.Time) int64 {
	metricsState.Lock()
	defer metricsState.Unlock()
	var n int64
	for _, rc := range metricsState.recent {
		if !rc.Time.Before(t) {
			n++
		}
	}
	return n
}

// MetricsSnapshot 返回指标快照（统计起点 + source -> action -> 指标），
// 平均耗时尚未计算的在此补齐
func MetricsSnapshot() (time.Time, map[string]map[string]ActionMetrics) {
	metricsState.Lock()
	defer metricsState.Unlock()
	out := make(map[string]map[string]ActionMetrics, len(metricsState.actions))
	for src, am := range metricsState.actions {
		row := make(map[string]ActionMetrics, len(am))
		for act, m := range am {
			cp := *m
			if cp.Total > 0 {
				cp.AvgLatencyMs = cp.TotalLatencyMs / cp.Total
			}
			row[act] = cp
		}
		out[src] = row
	}
	return metricsState.started, out
}

// DrainRecentCalls 取出并清空内存中的全部最近调用明细（用于优雅关停时落库）。
// 先等待飞行中的异步批量落库完成（淘汰批已离环，不等待会丢批）
func DrainRecentCalls() []RecentCall {
	flushWG.Wait()
	metricsState.Lock()
	defer metricsState.Unlock()
	out := metricsState.recent
	metricsState.recent = nil
	return out
}

// RecentCalls 返回最近调用明细（最新在前），最多 n 条
func RecentCalls(n int) []RecentCall {
	metricsState.Lock()
	defer metricsState.Unlock()
	total := len(metricsState.recent)
	if n <= 0 || n > total {
		n = total
	}
	out := make([]RecentCall, 0, n)
	for i := total - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, metricsState.recent[i])
	}
	return out
}

// FlushedCounts 返回本会话已淘汰落库的明细计数（"source/action" -> n）及总数，
// 用于 lifetime 口径扣除会话聚合中的已落库部分（避免双计，见 metricsState.flushed）
func FlushedCounts() (map[string]int64, int64) {
	metricsState.Lock()
	defer metricsState.Unlock()
	out := make(map[string]int64, len(metricsState.flushed))
	var total int64
	for src, am := range metricsState.flushed {
		for act, n := range am {
			out[src+"/"+act] = n
			total += n
		}
	}
	return out, total
}

// ResetMetrics 清零全部指标并重置统计起点
func ResetMetrics() {
	metricsState.Lock()
	defer metricsState.Unlock()
	metricsState.started = time.Now()
	metricsState.actions = make(map[string]map[string]*ActionMetrics)
	metricsState.recent = nil
	metricsState.flushed = make(map[string]map[string]int64)
}
