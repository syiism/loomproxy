package legado

import "loomproxy/base"

type BookDetail struct {
	BookType       string `json:"bookType"`
	BookTypeCode   int    `json:"bookTypeCode"`
	AuthorId       string `json:"authorId"`
	BookId         string `json:"bookId"`
	Name           string `json:"name"`
	AliasName      string `json:"aliasName"`
	Author         string `json:"author"`
	Status         string `json:"status"`
	CreateTime     string `json:"createTime"`
	LastUpdateTime string `json:"lastUpdateTime"`
	WordCount      string `json:"wordCount"`
	Category       string `json:"category"`
	Tags           string `json:"tags"`
	Roles          string `json:"roles"`
	Tones          string `json:"tones"`
	Score          string `json:"score"`
	ReadCount      string `json:"readCount"`
	Source         string `json:"source"`
	Intro          string `json:"intro"`
	Copyright      string `json:"copyright"`
	BookReview     string `json:"bookReview"`
	CoverUrl       string `json:"coverUrl"`
	LastChapter    string `json:"lastChapter"`
}

type DetailBaseHandler struct {
	base.BaseHandler
}

func NewDetailBaseHandler() *DetailBaseHandler {
	h := &DetailBaseHandler{
		BaseHandler: *base.NewBaseHandler(),
	}
	h.UpstreamCacheTTL = upstreamCacheTTL()
	return h
}
