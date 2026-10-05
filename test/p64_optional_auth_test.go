package test

// 可选鉴权（待办清单 P64）：AUTH_ENABLED=false 时网关直通是对的，但直通不等于「解析也跳过」——
// 过去合法 JWT 打 /endpoints 得 401（没人往 context 写 user_id，端点自己回 401），
// 面板「数据源」页整块加载失败。修复后：关着也要把能解析的凭证挂上，解析失败按匿名放行。

import (
	"net/http"
	"testing"

	"loomproxy/conf"
)

func withAuthOff(t *testing.T) {
	t.Helper()
	prev := conf.Config.AuthEnabled
	conf.Config.AuthEnabled = false
	t.Cleanup(func() { conf.Config.AuthEnabled = prev })
}

func TestOptionalAuthStillResolvesCredentials(t *testing.T) {
	srv := newTestServer(t)
	withAuthOff(t)

	// 合法 JWT：注册响应自带 token；必须 200 且不是「缺少登录凭证」（修前是 401）
	st, envReg := doJSON(t, srv, "POST", "/auth/register", map[string]interface{}{
		"username": "p64user", "email": "p64@test.local", "password": "pass1234",
	}, nil)
	if st != http.StatusOK {
		t.Fatalf("注册失败：%d %s", st, envReg.Msg)
	}
	tok, _ := envReg.dataMap(t)["token"].(string)
	if tok == "" {
		t.Fatal("注册响应里没有 token")
	}
	status, env := doJSON(t, srv, "GET", "/endpoints", nil, authHeader(tok))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("AUTH_ENABLED=false + 合法 JWT 打 /endpoints = %d code=%d msg=%s（P64：不该再把网关关闭当未登录）",
			status, env.Code, env.Msg)
	}

	// 无凭证/坏票：按匿名放行——但 /endpoints 是 user_id 过滤的端点，匿名无身份可挂，
	// 端点自己的守卫回 401（这是端点语义，不是网关拦的；P64 修的是合法凭证被网关关闭误伤）
	status, _ = doJSON(t, srv, "GET", "/endpoints", nil, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("AUTH_ENABLED=false + 无凭证 = %d，端点守卫应回 401（匿名无身份）", status)
	}
	status, _ = doJSON(t, srv, "GET", "/endpoints", nil, authHeader("not-a-jwt"))
	if status != http.StatusUnauthorized {
		t.Fatalf("AUTH_ENABLED=false + 坏票 = %d，应按匿名落到端点守卫", status)
	}
}
