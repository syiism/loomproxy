package test

// 路由层的必填参数校验（待办清单 P22②-a）：源声明 RequiredParams 后，缺参请求必须在
// 进 handler **之前**收口成 400。
//
// 钉住三件事：① 400 而不是「200 + 一句话说缺参数」——源里原来的写法是返回
// ContentType=="error" 的正文，Legado 会把那句话当正文渲染进阅读器，而监控看到的是成功；
// ② 被挡下的请求仍然进监控明细（reqparams 排在 monitor 之后，这是它必须被看见的理由）；
// ③ 没声明的动作一律不受影响——声明位缺失不等于校验缺失。

import (
	"net/http"
	"strings"
	"testing"

	"loomproxy/base"
	"loomproxy/testkit/fakesource"
)

// fake_a 的 content 在夹具里声明了必填 bookId+itemId（testkit/fakesource/fakesource.go）
func TestRequiredParamsRejectedBeforeHandler(t *testing.T) {
	srv := newTestServer(t)
	base.ResetMetrics()
	t.Cleanup(base.ResetMetrics)
	admin := adminToken(t, srv)

	status, env := doJSON(t, srv, http.MethodGet, "/fake_a/content?bookId=1", nil, authHeader(admin))
	if status != http.StatusBadRequest {
		t.Fatalf("缺 itemId 应 400（不该带着空参数进 handler），实得 %d：msg=%q", status, env.Msg)
	}
	if !strings.Contains(env.Msg, "itemId") {
		t.Errorf("400 的文案应点名缺的是哪个参数，实得 %q", env.Msg)
	}
	if env.Code == 0 {
		t.Error("失败响应的 code 不该是 0——下游按 code 判失败")
	}

	// 挡下的请求要在明细里看得见，且是一条 400、不是「200 + 带内失败」
	var sawRejected bool
	for _, rc := range base.RecentCalls(20) {
		if rc.Source != fakesource.A || rc.Action != "content" {
			continue
		}
		if rc.Status != http.StatusBadRequest {
			continue
		}
		if rc.InBandError {
			t.Error("400 不该被标成带内失败：它根本没进 handler")
		}
		sawRejected = true
	}
	if !sawRejected {
		t.Error("reqparams 挡下的请求应出现在调用明细里（它排在 monitor 之后就是为了这个）")
	}
	_, snap := base.MetricsSnapshot()
	if m := snap[fakesource.A]["content"]; m.Failed != 1 {
		t.Errorf("fake_a/content 的失败数 = %d, want 1（这次 400 必须计入失败）", m.Failed)
	}

	// 纯空白等于没带：源侧普遍是 TrimSpace 后判空，校验不跟着 trim 就会漏进 handler
	status, env = doJSON(t, srv, http.MethodGet, "/fake_a/content?bookId=1&itemId=%20", nil, authHeader(admin))
	if status != http.StatusBadRequest || !strings.Contains(env.Msg, "itemId") {
		t.Errorf("itemId 只给一个空格应判缺参并 400，实得 %d：msg=%q", status, env.Msg)
	}
}

func TestRequiredParamsOnlyAppliesToDeclaredActions(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)
	setPlatformUpstream(t, fakesource.A, staticUpstream(t, searchEnvelope).URL)

	// 参数齐 → 放行到 handler
	mustOK(t, srv, admin, "/fake_a/content?bookId=1&itemId=2")

	// search 没声明必填参数：无 key 的「空搜」照样进 handler，不该被这条校验挡
	status, env := doJSON(t, srv, http.MethodGet, "/fake_a/search", nil, authHeader(admin))
	if strings.Contains(env.Msg, "缺少必填参数") {
		t.Errorf("未声明的动作不该被 reqparams 挡（status=%d msg=%q）", status, env.Msg)
	}
	if status != http.StatusOK {
		t.Errorf("空搜应保持原有行为（由 handler 自己决定），实得 %d", status)
	}
}
