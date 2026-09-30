package legado

import "loomproxy/base"

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
}

type SearchResponse struct {
	BookList []BookItem `json:"bookList"`
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
