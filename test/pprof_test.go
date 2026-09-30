package test

// pprof 端点集成测试：/debug/pprof 整组由 AdminRequired 保护——
// 匿名 401、普通用户 403、管理员 200。

import (
	"net/http"
	"strings"
	"testing"
)

func TestPprofRequiresAdmin(t *testing.T) {
	srv := newTestServer(t)

	status, _ := doRaw(t, srv, http.MethodGet, "/debug/pprof/", nil, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("匿名访问 /debug/pprof/ 期望 401，实际 %d", status)
	}

	userToken := registerUser(t, srv, "pprofuser", "pprofuser@example.com", "passw0rd123")
	status, _ = doRaw(t, srv, http.MethodGet, "/debug/pprof/", nil, authHeader(userToken))
	if status != http.StatusForbidden {
		t.Fatalf("普通用户访问 /debug/pprof/ 期望 403，实际 %d", status)
	}

	token := adminToken(t, srv)
	status, body := doRaw(t, srv, http.MethodGet, "/debug/pprof/", nil, authHeader(token))
	if status != http.StatusOK {
		t.Fatalf("管理员访问 /debug/pprof/ 期望 200，实际 %d", status)
	}
	if !strings.Contains(string(body), "goroutine") {
		t.Fatalf("pprof 索引页应包含 goroutine 条目，实际: %s", truncate(string(body), 200))
	}

	status, _ = doRaw(t, srv, http.MethodGet, "/debug/pprof/goroutine?debug=1", nil, authHeader(token))
	if status != http.StatusOK {
		t.Fatalf("管理员访问 goroutine profile 期望 200，实际 %d", status)
	}
}
