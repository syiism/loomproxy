package test

// 上游传输失败的对外口径（脱敏 + 状态码归类）。
//
// 上游请求失败的 err.Error() 形如 `Get "http://<host>/<path>?<签名参数>": EOF`，
// 既暴露上游域名与路径，也可能带出签名凭证参数；按 AGENTS.md §10 的约定，
// 这类详情只进服务端日志，对下游给统一口径。同时它属网关侧问题，
// 不能落进「400 + 原文」的参数错误兜底。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// deadUpstream 接受连接后不写任何响应直接掐断，使客户端得到包装了请求 URL 的传输错误
func deadUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatalf("httptest server 不支持 hijack")
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Fatalf("hijack 失败: %v", err)
		}
		conn.Close()
	}))
	t.Cleanup(up.Close)
	return up
}

func TestUpstreamTransportErrorMasked(t *testing.T) {
	srv := newTestServer(t)
	admin := authHeader(adminToken(t, srv))
	up := deadUpstream(t)
	setPlatformUpstream(t, "fake_b", up.URL)

	status, env := doJSON(t, srv, http.MethodGet, "/fake_b/search?query=脱敏", nil, admin)
	if status != http.StatusBadGateway {
		t.Fatalf("上游传输失败应归 502，实为 %d（msg=%s）", status, env.Msg)
	}
	if env.Code == 0 {
		t.Fatal("失败响应不该是成功信封")
	}
	for _, leak := range []string{up.URL, "127.0.0.1", "://"} {
		if strings.Contains(env.Msg, leak) {
			t.Fatalf("对下游泄漏了上游地址（含 %q）: msg=%q", leak, env.Msg)
		}
	}
	if env.Msg != "上游请求失败，请稍后重试" {
		t.Fatalf("msg = %q, want 通用上游失败文案", env.Msg)
	}
}

// TestUpstreamParamErrorStillVisible 兜底分支的边界：参数缺失一类本地错误信息
// 不含 URL，仍原样给下游（否则 Legado 侧排障会失去提示）
func TestUpstreamParamErrorStillVisible(t *testing.T) {
	srv := newTestServer(t)
	admin := authHeader(adminToken(t, srv))

	status, env := doJSON(t, srv, http.MethodGet, "/fake_b/search", nil, admin)
	if status != http.StatusBadRequest {
		t.Fatalf("缺参应 400，实为 %d（msg=%s）", status, env.Msg)
	}
	if env.Msg == "" || strings.Contains(env.Msg, "://") {
		t.Fatalf("参数错误的文案应保持可读且不含 URL: %q", env.Msg)
	}
}
