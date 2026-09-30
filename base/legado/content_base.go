package legado

import (
	"loomproxy-go/base"
)

type ContentResponse struct {
	ContentType string                 `json:"contentType"`
	Data        map[string]interface{} `json:"data"`
}

type ContentBaseHandler struct {
	base.BaseHandler
}

func NewContentBaseHandler() *ContentBaseHandler {
	return &ContentBaseHandler{
		BaseHandler: *base.NewBaseHandler(),
	}
}
