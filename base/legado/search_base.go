package legado

import "loomproxy-go/base"

type BookItem struct {
	BookId      string `json:"bookId"`
	Name        string `json:"name"`
	Author      string `json:"author"`
	Kind        string `json:"kind"`
	WordCount   string `json:"wordCount"`
	LastChapter string `json:"lastChapter"`
	Intro       string `json:"intro"`
	CoverUrl    string `json:"coverUrl"`
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
