package legado

import "loomproxy-go/base"

type ChapterItem struct {
	Title       string `json:"title"`
	ItemId      string `json:"itemId"`
	ChapterInfo string `json:"chapterInfo"`
	IsVip       bool   `json:"isVip"`
}

type ChapterResponse struct {
	ChapterList []ChapterItem `json:"chapterList"`
	NextTocUrl  *string       `json:"nextTocUrl,omitempty"`
}

type ChapterBaseHandler struct {
	base.BaseHandler
}

func NewChapterBaseHandler() *ChapterBaseHandler {
	h := &ChapterBaseHandler{
		BaseHandler: *base.NewBaseHandler(),
	}
	h.UpstreamCacheTTL = upstreamCacheTTL()
	return h
}
