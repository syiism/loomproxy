package legado

// 搜索的**响应契约**与聚合的形状都在这里：`BookItem.Source`、`kind` 首项为源码、
// `sources` 参数的解析（AggregateTargets）与结果的合并/打标（StampSearchSource / MergeSearchResults）。
//
// 一条搜索请求的聚合**编排**在 `app/aggregate_search.go`：扇出到别的源要逐源过访问判定与额度、
// 要各自解析 baseUrl，这些是链与装配层的东西——放在这里会让 DTO 层反过来指挥治理层，
// 找实现时请按「形状在本文件、编排在 app 包」两条线索走，别只在本文件里找。

import (
	"bytes"
	"encoding/json"
	"strings"

	"loomproxy/base"
)

const (
	// AggregateSourcesParam 聚合搜索的参数名：逗号分隔的数据源码（如 fq_novel,sq_novel）
	AggregateSourcesParam = "sources"
	// AggregateMaxSources 一次聚合最多打几个源。上限不是保护下游，是保护上游与这条请求自己：
	// 没有上限的话，一个参数就能让网关替调用方去打遍全部源。
	AggregateMaxSources = 8
	// searchListKey 搜索结果列表的键：平台与源之间的契约就是这个键（Legado 的 searchRule 认它）
	searchListKey = "bookList"
)

type BookItem struct {
	BookId      string `json:"bookId"`
	Name        string `json:"name"`
	Author      string `json:"author"`
	Kind        string `json:"kind"`
	WordCount   string `json:"wordCount"`
	LastChapter string `json:"lastChapter"`
	Intro       string `json:"intro"`
	CoverUrl    string `json:"coverUrl"`
	// 媒介类型（可空）：源在搜索/发现结果里能给出类型时填上，监控明细据此标注这本书；
	// 不填也不报错——骨架会退到 tab 声明 / 命名缓存 / 源默认。
	// 用指针是因为 bookTypeCode=0 在 Legado 规范里是「小说」这个确定值，
	// 零值下发会把没表态的源误标成小说（同 ChapterResponse.NextTocUrl 的处理）。
	BookType     string `json:"bookType,omitempty"`
	BookTypeCode *int   `json:"bookTypeCode,omitempty"`
	// Source 这条结果来自哪个数据源码（如 fq_novel），下游据此把详情/目录/正文路由回同一个源。
	//
	// **由平台在出口统一填**（StampSearchSource），不采信源自报的值：这个字段的作用是
	// 「回到哪个源取正文」，源把自己的名字写错一个字，下游就整条链断在第二步，
	// 而那一刻看起来像源坏了。带 omitempty 是因为本类型与发现（explore）共用——
	// 发现不走聚合，不该凭空多出一个恒为空的键。
	Source string `json:"source,omitempty"`
}

type SearchResponse struct {
	BookList []BookItem `json:"bookList"`
	// SourcesStatus 聚合搜索里每个目标源的处置结果（ok / ungranted / disabled /
	// limit_exceeded / missing_params / not_found / upstream_failed / handler_missing）。
	// 只有**成因枚举**，没有上游文案——对外错误文案必须过脱敏，而这里连一个失败目标的
	// URL 都不该吐出去。单源搜索不带这个键。
	SourcesStatus map[string]string `json:"sources_status,omitempty"`
}

type SearchBaseHandler struct {
	base.BaseHandler
}

func NewSearchBaseHandler() *SearchBaseHandler {
	h := &SearchBaseHandler{
		BaseHandler: *base.NewBaseHandler(),
	}
	h.UpstreamCacheTTL = upstreamCacheTTL()
	return h
}

// AggregateTargets 解析 sources 参数并定出目标源列表。
//
// 路由源永远排第一（这条请求本来就是从它的路由进来的，不跑它等于悄悄改了接口语义），
// 其余按声明顺序追加；去重、去空白、忽略空项，超过 AggregateMaxSources 的截掉。
func AggregateTargets(routeSource, raw string) []string {
	seen := make(map[string]bool, AggregateMaxSources)
	out := make([]string, 0, AggregateMaxSources)
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] || len(out) >= AggregateMaxSources {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	add(routeSource)
	for _, part := range strings.Split(raw, ",") {
		add(part)
	}
	return out
}

// StampSearchSource 给一次搜索结果的每条书目项硬性写上 source，并把源码放到 kind 的第一项。
//
// 为什么在出口做而不是要求每个源自己填：源有十几个、且分支携带的源不属骨架所有，
// 「每个源都记得填」等于没有规则。出口一次做完，新接的源不填也符合契约。
//
// 形状归一后再写回（见 normalizeEnvelope）：typed SearchResponse 与 map 两种返回都得覆盖，
// 骨架的假源透传的就是 map。
//
// **就地改写**：输入是 map 时返回的就是同一个 map（条目对象也是同一批引用），不是副本。
// 聚合合并正是靠这一点把每个目标的条目按各自源标好；但也意味着调用方若在打标后还拿原始
// 结果去做别的事，看到的已经是打过标的形状。写它的人需要知道，别当纯函数用。
func StampSearchSource(result interface{}, source string) interface{} {
	if source == "" {
		return result
	}
	env, ok := normalizeEnvelope(result)
	if !ok {
		return result
	}
	// 没有 bookList 这个键就不打标：带内错误正文（ContentType=="error"）长成这样，
	// 打标等于顺手给失败响应塞一个空列表，下游会把「这次失败了」读成「没搜到」
	if _, has := env[searchListKey]; !has {
		return result
	}
	stampItems(env[searchListKey], source)
	return env
}

// MergeSearchResults 合并多个源的搜索结果成一个响应体。
//
// 输出保住**第一个成功结果自带的其它键**（有些源在 bookList 之外还带分页/统计字段），
// 列表按目标源顺序拼接、不做跨源去重：重名书是不是同一本，只有源自己知道，
// 平台在这里猜一次就会把两本不同的书合成一本。
// 全部目标都失败时返回**空列表 + 状态**而不是报错：下游按「这次没搜到」处理，
// 比把一个源名都没解析出来的错抛给阅读器好读。
func MergeSearchResults(results []interface{}, statuses map[string]string) interface{} {
	var merged map[string]interface{}
	var items []interface{}
	for _, r := range results {
		env, ok := normalizeEnvelope(r)
		if !ok {
			continue
		}
		if merged == nil {
			merged = env
		}
		if list, isList := env[searchListKey].([]interface{}); isList {
			items = append(items, list...)
		}
	}
	if merged == nil {
		merged = map[string]interface{}{}
	}
	if items == nil {
		items = []interface{}{}
	}
	merged[searchListKey] = items
	if len(statuses) > 0 {
		merged["sources_status"] = statuses
	}
	return merged
}

// stampItems 就地改写列表里的对象条目；非对象的条目跳过（不该发生，但别为它 panic）
func stampItems(raw interface{}, source string) {
	items, ok := raw.([]interface{})
	if !ok {
		return
	}
	for _, it := range items {
		m, ok := it.(map[string]interface{})
		if !ok {
			continue
		}
		m["source"] = source
		m["kind"] = PrependSourceToKind(m["kind"], source)
	}
}

// PrependSourceToKind 把数据源码放到 kind 的第一项。
//
// 为什么 source 之外还要在 kind 里再放一份：Legado 的搜索规则逐字段取值，
// 有些版本只认 name/author/kind 这些标准字段，自定义顶层键读不到——
// 而 kind 在下游是「标签串」，把它当分组键用的书源很多。
// 两个位置都给，聚合的调用方选哪个都成立。
//
// 保持原形状：源给的是字符串就回字符串、给的是数组就回数组（Legado 两种都吃），
// 平台不该在打标时顺手改了字段的类型。已有的同名项挪到最前，不重复第二遍。
func PrependSourceToKind(v interface{}, source string) interface{} {
	switch tv := v.(type) {
	case nil:
		return source
	case string:
		return sourceWith(tv, source, ",")
	case []interface{}:
		out := make([]interface{}, 0, len(tv)+1)
		out = append(out, source)
		for _, e := range tv {
			if s, isStr := e.(string); isStr && strings.TrimSpace(s) == source {
				continue
			}
			out = append(out, e)
		}
		return out
	default:
		return v
	}
}

// sourceWith 按分隔符拆串、去掉已有的源码项、再把源码放回第一位
func sourceWith(kind, source, sep string) string {
	var rest []string
	for _, p := range strings.Split(kind, sep) {
		p = strings.TrimSpace(p)
		if p == "" || p == source {
			continue
		}
		rest = append(rest, p)
	}
	if len(rest) == 0 {
		return source
	}
	return source + sep + strings.Join(rest, sep)
}

// normalizeEnvelope 把源的返回形状统一成 map[string]interface{}。
//
// 过一次 JSON 而不是类型断言，是因为要同时接住 typed SearchResponse 与透传的 map，
// 并且**保住源自带的未知字段**（类型断言到 map 会丢掉结构体里的额外键）。
// 数字一律用 json.Number：走 float64 会把 1675148 这样的字数在回写时变成 1.675148e+06，
// 那是给下游一个看着像乱码的值。
func normalizeEnvelope(result interface{}) (map[string]interface{}, bool) {
	if m, ok := result.(map[string]interface{}); ok {
		return m, true
	}
	if m, ok := result.(*map[string]interface{}); ok && m != nil {
		return *m, true
	}
	b, err := json.Marshal(result)
	if err != nil || len(b) == 0 || b[0] != '{' {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var m map[string]interface{}
	if err := dec.Decode(&m); err != nil {
		return nil, false
	}
	return m, true
}
