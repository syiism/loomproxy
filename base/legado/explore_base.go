package legado

import "loomproxy/base"

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
