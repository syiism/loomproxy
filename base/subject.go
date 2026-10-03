package base

import (
	"net/url"
	"strings"
	"sync"
	"time"
)

// CallSubject 一次调用的「内容维度」：搜了什么词、读的是哪本书、哪一章、什么媒介。
//
// 为什么需要它：监控中间件在 c.Next() 之后只能看到路由与状态码——请求参数里只有
// bookId/itemId/url 这类**标识**，没有书名与章节名。所以由拿到规范化响应的那一层
// （base/legado 的 ObserveCall）回填：search/detail 的响应里本就带名称，顺手喂进命名缓存，
// 后续 content 请求就能靠标识反查出书名、章节名与此前判定的媒介。
//
// 字段来源汇总（空串 = 这次没抽到，不是错误）：
//
//	形状        Keyword        BookName                 ChapterTitle      Media               ResultCount
//	bookList    搜索词参数      空（结果灌书名缓存）        空                条目自带或空          结果条数
//	bookInfo    空             响应的 name（缺则查缓存）   空                响应的类型码/名       1（有 info）
//	chapterList 空             缓存                       空                缓存                章节目录条数
//	正文        空             缓存（按书标识或章节标识）   缓存（toc 灌入）     响应的 contentType   正文非空=1
type CallSubject struct {
	Keyword      string `json:"keyword"`
	BookName     string `json:"book_name"`
	ChapterTitle string `json:"chapter_title"`
	Media        string `json:"media"` // 枚举见 media.go；空 = 未判定
	ResultCount  int    `json:"result_count"`
	// InBandError 带内失败（待办清单 P22）：HTTP 200 但正文是 ContentType=="error" 的
	// 错误载荷（Legado 书源对参数缺失/上游失败的传统写法）。HTTP 读数看不出它，
	// 不标出来的话，失败的正文请求就以「成功 + 内容维度全空」的形状混进明细，
	// 成功率、覆盖率、榜三处读数一起说谎。只标观测，不改响应、不动计费。
	InBandError bool `json:"-"`
	// 内部标识：只用于查命名缓存，不对外暴露（面板展示的是名称）
	BookKey    string `json:"-"`
	ChapterKey string `json:"-"`
	// ContentWithheld 内容维度是**被本人关掉的**、不是没抽到。
	// 这一格存在的理由不是给用户看，是让读数分得开两种「全空」：
	// 覆盖率算 `COALESCE(col,'')<>''`，退出的人必然掉进「缺失」那一侧——
	// 没有这个标记，采集在漏（P19 那条）与用户选择留存关闭长得一模一样，
	// 下一个人又会重新猜一遍「这 47% 是谁的锅」。
	ContentWithheld bool `json:"-"`
}

// WithholdContent 抹掉本次调用的全部内容维度。用户不同意留存时由 monitor 在
// 写进环形缓冲**之前**调用——此后进程外没有任何一份副本，不是「先落库再遮」。
//
// 标识（BookKey/ChapterKey）必须一起清：名称回填循环按 (source, 标识) 反查命名缓存
// 补写 book_name/chapter_title，留着标识等于「当场说不捕获、下一轮被自己补回来」，
// 开关就形同虚设了。
//
// InBandError 保留：它说的是这次请求成没成，不是用户读了什么，
// 清掉它会让成功率读数对退出用户说谎。
func (s *CallSubject) WithholdContent() {
	if s == nil {
		return
	}
	s.Keyword, s.BookName, s.ChapterTitle, s.Media, s.ResultCount = "", "", "", "", 0
	s.BookKey, s.ChapterKey = "", ""
	s.ContentWithheld = true
}

// 内容维度字段的长度上限（rune）。上游书名/章节名可以很长，明细表要保住有界：
// 超长按 rune 截断（按 byte 截会把中文截成半个字）。
const (
	SubjectKeywordMax  = 100
	SubjectBookMax     = 120
	SubjectChapterMax  = 200
	SubjectMediaMax    = 16
	subjectKeyIdentMax = 512 // 标识既用于查缓存，也随明细落库（供事后回填名称），此处做内存与列宽保护
)

// Normalize 统一收口：去空白 + 按 rune 截断，保证写进内存环形缓冲与数据库都界内。
// 放在抽取末尾做，而不是落库时做——内存缓冲同样长期驻留。
func (s *CallSubject) Normalize() {
	s.Keyword = truncateRunes(strings.TrimSpace(s.Keyword), SubjectKeywordMax)
	s.BookName = truncateRunes(strings.TrimSpace(s.BookName), SubjectBookMax)
	s.ChapterTitle = truncateRunes(strings.TrimSpace(s.ChapterTitle), SubjectChapterMax)
	s.Media = truncateRunes(strings.TrimSpace(s.Media), SubjectMediaMax)
	s.BookKey = truncateRunes(strings.TrimSpace(s.BookKey), subjectKeyIdentMax)
	s.ChapterKey = truncateRunes(strings.TrimSpace(s.ChapterKey), subjectKeyIdentMax)
}

// ---------------------------------------------------------------------------
// 命名缓存：标识 → 名称（+ 媒介），进程内，有上限与 TTL
// ---------------------------------------------------------------------------

const (
	// nameCacheCap 单类缓存的最大条目数。按每本书记一章的读法，2 万条足够覆盖
	// 一个进程生命周期内的活跃内容；超了按 FIFO 淘汰最老的。
	nameCacheCap = 20000
	// nameCacheTTL 名称的有效期：书名/章节名几乎不变，但缓存不该无限长大，
	// 隔一段时间自然过期，也让改了名的书能被刷新。
	nameCacheTTL = 24 * time.Hour
)

type namedEntry struct {
	name  string
	media string // 只有书维度用：同一本书的媒介不会因请求而异
	at    time.Time
}

// nameCache 有界的「标识 → 名称」表。读写都在请求路径上，一把互斥锁足够：
// 临界区只有一次 map 操作，无 IO。
type nameCache struct {
	mu   sync.Mutex
	m    map[string]namedEntry
	fifo []string // 首次写入顺序，用于淘汰
}

func newNameCache() *nameCache {
	return &nameCache{m: make(map[string]namedEntry, 64)}
}

func (c *nameCache) put(key, name, media string) {
	if key == "" || (name == "" && media == "") {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, exists := c.m[key]; exists {
		// 已存在：只补空缺，不覆盖已有名字（新抽到的空值不该把老名字抹掉）
		if name == "" {
			name = old.name
		}
		if media == "" {
			media = old.media
		}
		c.m[key] = namedEntry{name: name, media: media, at: time.Now()}
		return
	}
	// 满了先腾位：按 FIFO 丢最老的（队列里的键可能已被 get 判过期删掉，
	// 所以逐个探测、存在的才真删；每次循环至少弹一个，必然终止）
	for len(c.m) >= nameCacheCap && len(c.fifo) > 0 {
		k := c.fifo[0]
		c.fifo = c.fifo[1:]
		delete(c.m, k)
	}
	c.fifo = append(c.fifo, key)
	c.m[key] = namedEntry{name: name, media: media, at: time.Now()}
}

func (c *nameCache) get(key string) namedEntry {
	if key == "" {
		return namedEntry{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok {
		return namedEntry{}
	}
	if time.Since(e.at) > nameCacheTTL {
		delete(c.m, key)
		return namedEntry{}
	}
	return e
}

func (c *nameCache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}

var (
	// 键带源码前缀：两个源可能复用同一个 bookId（尤其都拿 URL 当标识时）
	bookNameCache    = newNameCache()
	chapterNameCache = newNameCache()
)

func identKey(source, ident string) string {
	if ident == "" {
		return ""
	}
	return source + "|" + ident
}

// SubjectStore 命名缓存的持久化后端（可选）。进程内缓存重启即失效，而「标识 → 名称」
// 本该更长寿——正文/目录请求只带标识，缓存一空这些调用的名称维度就只能是空。
// base 不认识 Redis（也不该认识），由装配层在启动时注入；未注入即保持纯内存的现行为。
//
// 契约：Save* 不得阻塞请求路径（实现方自己排队或静默降级）；Load* 允许失败，
// 取不到就当没命中——名称留空是合法状态，猜一个才是错。
type SubjectStore interface {
	SaveBook(source, ident, name, media string)
	SaveChapter(source, ident, title string)
	LoadBook(source, ident string) (name, media string, ok bool)
	LoadChapter(source, ident string) (title string, ok bool)
}

var (
	subjectStoreMu sync.RWMutex
	subjectStore   SubjectStore
)

// SetSubjectStore 注入持久化后端（传 nil 退回纯内存）
func SetSubjectStore(st SubjectStore) {
	subjectStoreMu.Lock()
	subjectStore = st
	subjectStoreMu.Unlock()
}

// SubjectStoreHealth 持久化后端**可选**实现的写侧体检面。丢持久化不是致命错（内存缓存仍在），
// 但如果只有关停日志里那一行，运行期间「正在丢」这件事就没人看得见——面板要能直接读到
// 排队深度与两类丢失计数（见待办清单 P2）。风格照 base/pool 的 ResourceExpiredClassifier：
// 接口存在即能力声明，base 不做分支。
type SubjectStoreHealth interface {
	PersistHealth() (queued, dropped, failed int64)
}

// NameCachePersistHealth 返回写侧体检值；未注入后端、或后端没实现体检面时 ok=false
func NameCachePersistHealth() (queued, dropped, failed int64, ok bool) {
	st, ok := subjectStoreRef().(SubjectStoreHealth)
	if !ok {
		return 0, 0, 0, false
	}
	q, d, f := st.PersistHealth()
	return q, d, f, true
}

func subjectStoreRef() SubjectStore {
	subjectStoreMu.RLock()
	defer subjectStoreMu.RUnlock()
	return subjectStore
}

// RememberBook 登记「书标识 → 书名 + 媒介」；name 与 media 都空时不登记
func RememberBook(source, ident, name, media string) {
	if ident == "" || (name == "" && media == "") {
		return
	}
	bookNameCache.put(identKey(source, ident), name, media)
	if st := subjectStoreRef(); st != nil {
		st.SaveBook(source, ident, name, media)
	}
}

// LookupBook 反查书名与媒介；内存未命中时问持久化后端，问到就回填内存（不重复外呼）
func LookupBook(source, ident string) (name, media string) {
	key := identKey(source, ident)
	if e := bookNameCache.get(key); e.name != "" || e.media != "" {
		return e.name, e.media
	}
	st := subjectStoreRef()
	if st == nil || key == "" {
		return "", ""
	}
	name, media, ok := st.LoadBook(source, ident)
	if ok && (name != "" || media != "") {
		bookNameCache.put(key, name, media)
		return name, media
	}
	return "", ""
}

// RememberChapter 登记「章节标识 → 章节标题」
func RememberChapter(source, ident, title string) {
	if ident == "" || title == "" {
		return
	}
	chapterNameCache.put(identKey(source, ident), title, "")
	if st := subjectStoreRef(); st != nil {
		st.SaveChapter(source, ident, title)
	}
}

// LookupChapter 反查章节标题；内存未命中时问持久化后端并回填内存
func LookupChapter(source, ident string) string {
	key := identKey(source, ident)
	if e := chapterNameCache.get(key); e.name != "" {
		return e.name
	}
	st := subjectStoreRef()
	if st == nil || key == "" {
		return ""
	}
	if title, ok := st.LoadChapter(source, ident); ok && title != "" {
		chapterNameCache.put(key, title, "")
		return title
	}
	return ""
}

// SubjectStoreLoaded 命名缓存是否已接持久化后端（管理端据此说明"重启后名称是否还在"）
func SubjectStoreLoaded() bool { return subjectStoreRef() != nil }

// NameCacheStats 两个命名缓存的当前条目数（供监控页/自检展示缓存是否在工作）
func NameCacheStats() (books, chapters int) {
	return bookNameCache.size(), chapterNameCache.size()
}

// ResetNameCaches 清空命名缓存（仅供集成测试隔离全局态）
func ResetNameCaches() {
	bookNameCache = newNameCache()
	chapterNameCache = newNameCache()
}

// ---------------------------------------------------------------------------
// 标识推导
// ---------------------------------------------------------------------------

// BookIdentFromChapterKey 从章节标识里推导书标识（请求没单独带书标识时的兜底）。
// 两种常见形态：`bookId|chapterId`，以及带 bookId 查询参数的 URL。
func BookIdentFromChapterKey(key string) string {
	if key == "" {
		return ""
	}
	if i := strings.Index(key, "|"); i > 0 {
		return key[:i]
	}
	for _, qk := range []string{"bookId", "book_id", "bid", "bookid"} {
		if v := rawQueryValue(key, qk); v != "" {
			return v
		}
	}
	return ""
}

// rawQueryValue 从一段「原始 URL 字符串」里取查询参数（解一次转义）。
// 不用 url.Parse：这些串常常不是完整合法 URL（相对路径、带未转义中文），
// 手工切分更稳，也不会因为解析失败就整段丢掉。
func rawQueryValue(raw, key string) string {
	if raw == "" || key == "" {
		return ""
	}
	i := strings.Index(raw, "?")
	if i < 0 {
		return ""
	}
	q := raw[i+1:]
	if j := strings.IndexAny(q, "#"); j >= 0 {
		q = q[:j]
	}
	for _, kv := range strings.Split(q, "&") {
		if kv == "" {
			continue
		}
		k, v, found := strings.Cut(kv, "=")
		if !found || k != key {
			continue
		}
		if dec, err := url.QueryUnescape(v); err == nil {
			return dec
		}
		return v
	}
	return ""
}

// truncateRunes 按 rune 截断（中文安全），n<=0 或未超长时原样返回。
func truncateRunes(s string, n int) string {
	if n <= 0 || s == "" {
		return s
	}
	if len(s) <= n { // 字节数不超过就差不到哪去，省一次遍历
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
