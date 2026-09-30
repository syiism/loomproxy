package test

// 假数据源（测试夹具）。
//
// 底座项目不携带任何书源实现，但数据源路由管线（网关鉴权 → 调用监控 → baseUrl 解析
// → 访问控制 → 计费 → 速率限制 → handler）与 seed 播种（data_sources / quota_costs /
// quota_limits / 套餐关联）都必须有真实挂载的路由才能被集成测试覆盖。
// 这里注册三个假源（fake_a/fake_b/fake_c），处理器只做一件事：按解析出的 baseUrl
// 请求上游 `<baseUrl>/api` 并原样返回——上游由用例自带的 httptest 提供。
//
// 三个源而非一个：限流器与计费状态是进程级共享的（key 含 数据源+接口），
// 用例按源隔离，避免相互污染。

import (
	"context"
	"errors"
	"strings"

	"loomproxy-go/base"
)

const (
	fakeA = "fake_a"
	fakeB = "fake_b"
	fakeC = "fake_c"
)

// fakeActions 假源声明的动作集：seed 据此播种 quota_costs，app 据此对账路由
var fakeActions = []string{"search", "detail", "chapter", "content", "explore"}

// fakeSourceHandler 最小可用的数据源处理器：走 base 弹性栈取上游，不做任何归一化
type fakeSourceHandler struct {
	base.BaseHandler
}

func newFakeSourceHandler(source, action string) base.Handler {
	h := &fakeSourceHandler{BaseHandler: *base.NewBaseHandler()}
	h.Path = "/" + source + "/" + action
	h.Name = source + "_" + action
	h.Methods = []string{"GET"}
	h.QueryParams = []string{"query", "bookId", "itemId", "baseUrl"}
	h.Description = "假数据源 " + source + " " + action
	h.Auth = true
	return h
}

func (h *fakeSourceHandler) Handle(ctx context.Context, params map[string]interface{}) (interface{}, error) {
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

func init() {
	sources := []struct{ code, display string }{
		{fakeA, "假数据源A"},
		{fakeB, "假数据源B"},
		{fakeC, "假数据源C"},
	}
	for i, s := range sources {
		if err := base.RegisterSource(base.SourceMeta{
			Code:        s.code,
			Display:     s.display,
			Category:    "fake",
			Description: "集成测试夹具，不属于底座功能",
			SortOrder:   i + 1,
			Status:      1,
			Actions:     fakeActions,
		}); err != nil {
			panic("注册假数据源失败: " + err.Error())
		}
		for _, action := range fakeActions {
			src, act := s.code, action
			factory := func(_ *base.APIConfig) base.Handler { return newFakeSourceHandler(src, act) }
			if err := base.Register(src+"_"+action, factory, 10, nil); err != nil {
				panic("注册假数据源处理器失败: " + err.Error())
			}
		}
	}
}
