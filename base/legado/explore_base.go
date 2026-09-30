package legado

import "loomproxy-go/base"

type ExploreResponse struct {
	BookList []BookItem `json:"bookList"`
}

type ExploreBaseHandler struct {
	base.BaseHandler
}

func NewExploreBaseHandler() *ExploreBaseHandler {
	h := &ExploreBaseHandler{
		BaseHandler: *base.NewBaseHandler(),
	}
	h.UpstreamCacheTTL = upstreamCacheTTL()
	return h
}
