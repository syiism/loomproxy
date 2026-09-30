// Package fakesource 数据源路由管线的测试夹具。
//
// 底座项目不携带任何书源实现，但数据源链路（网关鉴权 → 调用监控 → baseUrl 解析
// → 访问控制 → 计费 → 速率限制 → handler）与 seed 播种（data_sources / quota_costs /
// quota_limits / 套餐关联）都必须有真实挂载的路由才能被集成测试覆盖。
//
// 这里提供三个假源（fake_a / fake_b / fake_c），处理器只做一件事：按解析出的 baseUrl
// 请求上游 `<baseUrl>/api` 并原样返回——上游由用例自带（Go 侧 httptest，跨进程侧内置
// http.server）。三个源而非一个：限流器与计费状态是进程级共享的（key 含 数据源+接口），
// 用例按源隔离，避免相互污染。
//
// 三个源各自带一项声明，用于覆盖「形态差异靠声明、不靠底座分支」：
// fake_a 的 LegacyGroups（seed 展开存量组行）、fake_b 的 DataFiles（/data 说明）、
// fake_c 的 SearchTabs（/datasources 分类）。
//
// 用法：`Register()` 只调一次——Go 集成测试由 test 包 init 调用；跨进程用例经
// cmd/fakegateway 起同一套声明。夹具不属于产品功能，产品入口不调用。
package fakesource

import (
	"context"
	"errors"
	"strings"

	"loomproxy/base"
)

const (
	A = "fake_a"
	B = "fake_b"
	C = "fake_c"

	// LegacyGroup 假源声明的历史平台组码（仅供 seed 迁移用例构造存量行）
	LegacyGroup = "fake_legacy_group"

	// DictFile fake_b 声明的附属数据字典文件名（不含 .json）
	DictFile = "fake_dict"
)

// Actions 假源声明的动作集：seed 据此播种 quota_costs，app 据此对账路由
var Actions = []string{"search", "detail", "chapter", "content", "explore"}

// handler 最小可用的数据源处理器：走 base 弹性栈取上游，不做任何归一化
type handler struct {
	base.BaseHandler
}

func newHandler(source, action string) base.Handler {
	h := &handler{BaseHandler: *base.NewBaseHandler()}
	h.Path = "/" + source + "/" + action
	h.Name = source + "_" + action
	h.Methods = []string{"GET"}
	h.QueryParams = []string{"key", "query", "tabType", "bookId", "itemId", "baseUrl"}
	h.Description = "假数据源 " + source + " " + action
	h.Auth = true
	return h
}

func (h *handler) Handle(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	baseURL := base.GetParam(params, "baseUrl")
	if baseURL == "" {
		// 与真实源一致：缺 baseUrl 属参数问题（经 handleError 归 400），
		// 中间件用例只关心请求是否被限流/拦截，不关心 200 内容
		return nil, errors.New("missing baseUrl parameter")
	}
	data, err := h.FetchJSON(ctx, strings.TrimRight(baseURL, "/")+"/api", nil)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// Register 登记三个假源的声明与处理器（同进程只应调用一次，重复登记会 panic）。
// 每个源额外钉一项「内容维度」的来源，覆盖三条判定路径（见 base/media.go 的优先级）：
// fake_a 不声明媒介 → 只能由响应自带类型判定；fake_b 声明源默认 audio；
// fake_c 在 tab 上声明 comic → 由请求的 tabType 判定。
func Register() {
	sources := []struct {
		code, display string
		legacyGroups  []string
		searchTabs    []base.SearchTab
		dataFiles     []base.DataFileDesc
		mediaType     string
	}{
		{A, "假数据源A", []string{LegacyGroup}, nil, nil, ""},
		{B, "假数据源B", nil, nil, []base.DataFileDesc{{Name: DictFile, Description: "假源字典"}}, base.MediaAudio},
		{C, "假数据源C", nil, []base.SearchTab{
			{TabType: 1, BdID: "fa", Name: "假分类一"},
			{TabType: 2, BdID: "fb", Name: "假分类二", MediaType: base.MediaComic},
		}, nil, ""},
	}
	for i, s := range sources {
		if err := base.RegisterSource(base.SourceMeta{
			Code:         s.code,
			Display:      s.display,
			Category:     "fake",
			Description:  "集成测试夹具，不属于底座功能",
			SortOrder:    i + 1,
			Status:       1,
			Actions:      Actions,
			LegacyGroups: s.legacyGroups,
			SearchTabs:   s.searchTabs,
			DataFiles:    s.dataFiles,
			MediaType:    s.mediaType,
		}); err != nil {
			panic("注册假数据源失败: " + err.Error())
		}
		for _, action := range Actions {
			src, act := s.code, action
			factory := func(_ *base.APIConfig) base.Handler { return newHandler(src, act) }
			if err := base.Register(src+"_"+action, factory, 10, nil); err != nil {
				panic("注册假数据源处理器失败: " + err.Error())
			}
		}
	}
}
