package legado

import (
	"strconv"
	"strings"

	"loomproxy/base"
)

// ObserveCall 从一次调用的规范化响应里抽取「内容维度」（搜索词/书名/章节名/媒介），
// 回填 subj 并喂命名缓存，供后续只带标识的请求反查。
//
// 为什么放在 legado 而不是 base：base 拥有 CallSubject 与命名缓存，但不能导入 legado
// （legado → base 已是依赖，反向即成环）；而形状识别必须认识本包的 DTO 类型。
//
// 媒介判定的优先级（本函数末尾的短路链）：
//  1. 响应自带（正文 contentType / 详情的 bookTypeCode|bookType / 条目的类型字段）
//  2. 请求 tab 命中的源声明（SearchTab.MediaType）
//  3. 命名缓存里同一本书此前的判定
//  4. 源默认声明（SourceMeta.MediaType）
//  5. 都没有 = 空串（未判定；被拦掉的请求从没触及内容，不该伪装成一个类型）
//
// 尽力而为，绝不添乱：抽取失败只让维度留空，返回值一律不影响响应，也不该把请求路径带倒。
func ObserveCall(source string, params map[string]interface{}, result interface{}, subj *base.CallSubject) {
	if subj == nil {
		return
	}
	defer func() {
		// 观测性代码不参与业务：形状再怪也只留空值，recover 后由 Normalize 收口
		_ = recover()
		subj.Normalize()
	}()

	// 标识与搜索词：字段名多写法兼容（不同源的参数习惯不一）
	subj.BookKey = base.ParamString(params, "bookId", "book_id", "id", "bookUrl")
	subj.ChapterKey = base.ParamString(params, "itemId", "item_id", "chapterUrl", "chapter_url", "url")
	subj.Keyword = base.ParamString(params, "key", "keyword", "query", "wd")

	switch v := result.(type) {
	case SearchResponse:
		observeBookList(source, subj, v.BookList)
	case *SearchResponse:
		observeBookList(source, subj, v.BookList)
	case ExploreResponse:
		observeBookList(source, subj, v.BookList)
	case *ExploreResponse:
		observeBookList(source, subj, v.BookList)
	case BookDetail:
		observeBookInfo(source, subj, v.Name, v.BookId, v.BookType, v.BookTypeCode)
	case *BookDetail:
		observeBookInfo(source, subj, v.Name, v.BookId, v.BookType, v.BookTypeCode)
	case ChapterResponse:
		observeChapterList(source, subj, v.ChapterList)
	case *ChapterResponse:
		observeChapterList(source, subj, v.ChapterList)
	case ContentResponse:
		observeContent(source, subj, v.ContentType, v.Data)
	case *ContentResponse:
		observeContent(source, subj, v.ContentType, v.Data)
	default:
		if m, ok := result.(map[string]interface{}); ok {
			observeLooseMap(source, subj, m)
		}
	}

	resolveMedia(source, params, subj)
	backfillFromCache(source, subj)
}

// ---------------------------------------------------------------------------
// 各形状的抽取
// ---------------------------------------------------------------------------

func observeBookList(source string, subj *base.CallSubject, items []BookItem) {
	if len(items) == 0 {
		return
	}
	subj.ResultCount = len(items)
	// 搜索结果里的书名灌进缓存：用户看完列表就会点进某本书，之后 info/chapter/content
	// 只带标识，靠这里缓存的名字与媒介才能标出来
	for _, it := range items {
		base.RememberBook(source, it.BookId, it.Name, mediaOfItem(it))
	}
}

// observeBookInfo 详情：书名为准，媒介优先看显式的 bookType 写法（"听书"/"漫画"…），
// 其次才用 bookTypeCode——DTO 的码是 int，没填时零值就是 0=小说，不该覆盖明确的名字写法
func observeBookInfo(source string, subj *base.CallSubject, name, bookID, bookType string, typeCode int) {
	media := base.NormalizeMediaName(bookType)
	if media == "" {
		media = base.MediaFromBookTypeCode(typeCode)
	}
	if name == "" {
		return
	}
	subj.BookName = name
	subj.ResultCount = 1
	if subj.BookKey == "" {
		subj.BookKey = bookID
	}
	if media != "" {
		subj.Media = media
	}
	base.RememberBook(source, subj.BookKey, name, media)
}

func observeChapterList(source string, subj *base.CallSubject, items []ChapterItem) {
	if len(items) == 0 {
		return
	}
	subj.ResultCount = len(items)
	for _, it := range items {
		base.RememberChapter(source, it.ItemId, it.Title)
	}
}

func observeContent(source string, subj *base.CallSubject, contentType string, data map[string]interface{}) {
	// 带内错误（P22）：源在参数缺失/上游失败时返回 ContentType=="error" 的正文，HTTP 仍是 200。
	// "error" 不是任何合法媒介，媒介照旧留空；这里只把这次调用标成带内失败，
	// 让监控的成功率/覆盖率读数能把它数成失败。不动响应本身——那是产品口径（P22②）。
	if strings.EqualFold(contentType, "error") {
		subj.InBandError = true
	}
	media := base.MediaFromContentType(contentType)
	if media == "" {
		media = base.NormalizeMediaName(contentType)
	}
	if len(data) > 0 {
		if s := base.ParamString(data, "content", "text", "body"); s != "" {
			subj.ResultCount = 1
		}
	}
	if media != "" {
		subj.Media = media
	}
}

// observeLooseMap 兜底：源自己拼 map（AGENTS.md §12 的传参口径）时按标准信封键识别
func observeLooseMap(source string, subj *base.CallSubject, m map[string]interface{}) {
	if list, ok := m["bookList"].([]interface{}); ok {
		subj.ResultCount = len(list)
		for _, raw := range list {
			item, _ := raw.(map[string]interface{})
			if item == nil {
				continue
			}
			ident := base.ParamString(item, "bookId", "u", "url", "bookUrl")
			name := base.ParamString(item, "name", "bookName", "title")
			media := mediaFromLoose(item)
			base.RememberBook(source, ident, name, media)
		}
		return
	}
	if info, ok := m["bookInfo"].(map[string]interface{}); ok {
		name := base.ParamString(info, "name", "bookName")
		observeBookInfo(source, subj, name, base.ParamString(info, "bookId", "book_id"),
			base.ParamString(info, "bookType"), looseInt(info["bookTypeCode"]))
		return
	}
	if detailName := base.ParamString(m, "name", "bookName"); detailName != "" {
		observeBookInfo(source, subj, detailName, base.ParamString(m, "bookId", "book_id"),
			base.ParamString(m, "bookType"), looseInt(m["bookTypeCode"]))
	}
	if toc, ok := m["chapterList"].([]interface{}); ok {
		subj.ResultCount = len(toc)
		for _, raw := range toc {
			item, _ := raw.(map[string]interface{})
			if item == nil {
				continue
			}
			base.RememberChapter(source,
				base.ParamString(item, "itemId", "u", "url"),
				base.ParamString(item, "title", "n", "name"))
		}
		return
	}
	if ct := base.ParamString(m, "contentType"); ct != "" {
		observeContent(source, subj, ct, asMap(m["data"]))
		if s := base.ParamString(m, "content"); s != "" {
			subj.ResultCount = 1
		}
	}
}

// mediaOfItem 搜索/发现条目的媒介：优先显式的类型码（指针，未填即不下发），其次类型名
func mediaOfItem(it BookItem) string {
	if it.BookTypeCode != nil {
		if m := base.MediaFromBookTypeCode(*it.BookTypeCode); m != "" {
			return m
		}
	}
	return base.NormalizeMediaName(it.BookType)
}

func mediaFromLoose(item map[string]interface{}) string {
	if m := base.MediaFromBookTypeCode(looseInt(item["bookTypeCode"])); m != "" {
		return m
	}
	return base.NormalizeMediaName(base.ParamString(item, "bookType", "kind", "type"))
}

// ---------------------------------------------------------------------------
// 媒介判定与缓存回填
// ---------------------------------------------------------------------------

// resolveMedia 按优先级补齐媒介，并把判定结果写回书名缓存（下一跳只带标识时能用上）
func resolveMedia(source string, params map[string]interface{}, subj *base.CallSubject) {
	if subj.BookKey == "" && subj.ChapterKey != "" {
		subj.BookKey = base.BookIdentFromChapterKey(subj.ChapterKey)
	}
	if subj.Media != "" {
		if subj.BookKey != "" {
			base.RememberBook(source, subj.BookKey, subj.BookName, subj.Media)
		}
		return
	}
	if tab := base.MediaFromSearchTab(source, params); tab != "" {
		subj.Media = tab
	} else if _, cached := base.LookupBook(source, subj.BookKey); subj.BookKey != "" && cached != "" {
		subj.Media = cached
	} else if meta, ok := base.GetSourceMeta(source); ok {
		subj.Media = meta.MediaType
	}
	if subj.Media != "" && subj.BookKey != "" {
		base.RememberBook(source, subj.BookKey, subj.BookName, subj.Media)
	}
}

// backfillFromCache 名称缺失时按标识反查（content/chapter 请求只带 bookId/itemId）
func backfillFromCache(source string, subj *base.CallSubject) {
	if subj.BookName == "" && subj.BookKey != "" {
		if name, _ := base.LookupBook(source, subj.BookKey); name != "" {
			subj.BookName = name
		}
	}
	if subj.ChapterTitle == "" && subj.ChapterKey != "" {
		subj.ChapterTitle = base.LookupChapter(source, subj.ChapterKey)
	}
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

func asMap(v interface{}) map[string]interface{} {
	m, _ := v.(map[string]interface{})
	return m
}

// looseInt 把 JSON 里可能是 float64/int/string 的类型码转成 int；拿不到返回 -1（未知）
func looseInt(v interface{}) int {
	switch t := v.(type) {
	case nil:
		return -1
	case float64:
		return int(t)
	case int:
		return t
	case int64:
		return int(t)
	case string:
		n, err := strconv.Atoi(t)
		if err != nil {
			return -1
		}
		return n
	}
	return -1
}
