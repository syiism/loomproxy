package test

// 参数绑定错误的对外形状（待办清单 P90②）。gin 的绑定错误原文里躺着**结构体名、字段名、期望类型**：
// `json: cannot unmarshal array into Go value of type auth.loginRequest`、
// `Key: 'loginRequest.Password' Error:Field validation for 'Password' failed on the 'required' tag`。
// 这六条都是**用户面**端点（`/auth/*` 与 `/user/source-configs`，登录页与个人中心每天都在打），
// 不需要任何管理员身份就能看到。收口口径与注册那条一致：对外只发固定句「参数错误」，
// 真因进服务端日志（给用户的不泄、给运维的有声，同一条原因两份信息）。

import (
	"net/http"
	"strings"
	"testing"
)

func TestBindingErrorMessagesDoNotLeakStructShape(t *testing.T) {
	srv := newTestServer(t)
	hdr := authHeader(adminToken(t, srv))

	cases := []struct {
		method, path string
		needSession  bool
	}{
		{"POST", "/auth/login", false},
		{"PATCH", "/auth/me", true},
		{"POST", "/auth/privacy", true},
		{"PUT", "/auth/display-alias", true},
		{"POST", "/auth/password", true},
		{"PUT", "/user/source-configs", true},
	}

	// 送一个数组：任何结构体都绑不进它，于是必然走绑定失败那一条分支
	body := []interface{}{}
	for _, c := range cases {
		var h map[string]string
		if c.needSession {
			h = hdr
		}
		status, env := doJSON(t, srv, c.method, c.path, body, h)
		if status != http.StatusBadRequest {
			t.Errorf("%s %s = %d，want 400（送的是数组，理应绑不进结构体）", c.method, c.path, status)
			continue
		}
		for _, leak := range []string{"cannot unmarshal", "Field validation", "Key: '", "struct field", "Request"} {
			if strings.Contains(env.Msg, leak) {
				t.Errorf("%s %s 的 msg 把绑定错误原文发给了调用者：%q（命中特征 %q）", c.method, c.path, env.Msg, leak)
			}
		}
		if env.Msg != "参数错误" {
			t.Errorf("%s %s 的 msg = %q，want 固定句「参数错误」", c.method, c.path, env.Msg)
		}
	}
}
