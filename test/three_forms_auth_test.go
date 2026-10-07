package test

// 三形态鉴权（待办清单 P26）：token（Bearer / ?token=）、cookie、apiKey 在用户面端点都要能用；
// 凭证引导类与管理面端点**只认会话**——这条反向断言是安全不变量，必须会红。
//
// 为什么两边都要测：只测「能进去」会漏掉真正危险的那一半（一把长期密钥能铸造别的密钥、
// 能改密码、能进管理面 = 提权且无法靠登出止血）。

import (
	"loomproxy/conf"
	"loomproxy/db"
	"loomproxy/testkit"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// threeFormUser 注册一个用户并为它建一把 API Key，返回 (会话 token, 密钥明文)
func threeFormUser(t *testing.T, srv *httptest.Server) (string, string) {
	t.Helper()
	return testkit.ThreeFormUser(t, srv)
}

// forms 五种凭证形态 → 该形态的 header 与 query 追加。query 形态返回要拼到 path 上的串。
func forms(token, key string) []struct {
	name    string
	headers map[string]string
	query   string
} {
	return []struct {
		name    string
		headers map[string]string
		query   string
	}{
		{"Bearer", authHeader(token), ""},
		{"?token=", nil, "?token=" + token},
		{"Cookie", map[string]string{"Cookie": "loomproxy_token=" + token}, ""},
		{"X-API-Key", map[string]string{"X-API-Key": key}, ""},
		{"?api_key=", nil, "?api_key=" + key},
	}
}

// TestThreeFormsOnUserPlane 用户面端点：五种凭证形态都必须进得去
func TestThreeFormsOnUserPlane(t *testing.T) {
	srv := newTestServer(t)
	token, key := threeFormUser(t, srv)

	// /rank/boards 还有一层**授权**门槛（放行名单为空时全员 403），
	// 不先放行就会把「鉴权失败」和「不该看榜单」混成同一个 403，测不出身份形态
	if st, env := doJSON(t, srv, http.MethodPut, "/admin/settings/rank_public_sources",
		map[string]string{"value": "fake_a"}, authHeader(adminToken(t, srv))); st != http.StatusOK || env.Code != 0 {
		t.Fatalf("放行榜单失败: status=%d code=%d msg=%s", st, env.Code, env.Msg)
	}
	db.InvalidateSettingCache("rank_public_sources")

	for _, path := range []string{"/quota/dashboard", "/user/import-config", "/rank/boards"} {
		for _, f := range forms(token, key) {
			status, raw := doRaw(t, srv, http.MethodGet, path+f.query, nil, f.headers)
			if status == http.StatusUnauthorized || status == http.StatusForbidden {
				t.Errorf("%s 用 %s 被拒：status=%d body=%s（P26 要求三形态都可用）",
					path, f.name, status, truncate(string(raw), 160))
			} else if status != http.StatusOK {
				t.Errorf("%s 用 %s status = %d, want 200（body=%s）",
					path, f.name, status, truncate(string(raw), 160))
			}
		}
	}
}

// TestSessionOnlyEndpointsRejectApiKey 反向断言：引导类与管理面端点拿密钥必须被拒。
// 管理面那两条用**管理员自己的密钥**——要钉的正是「管理员的长期密钥也进不去管理面」，
// 普通用户的密钥在这里被拒是套餐/角色的自然结果，测不出什么。
func TestSessionOnlyEndpointsRejectApiKey(t *testing.T) {
	srv := newTestServer(t)
	token, userKey := threeFormUser(t, srv)

	adminSession := adminToken(t, srv)
	_, adminKey := func() (string, string) {
		st, env := doJSON(t, srv, http.MethodPost, "/apikey", map[string]string{"name": "admin-ci"}, authHeader(adminSession))
		if st != http.StatusOK || env.Code != 0 {
			t.Fatalf("给管理员建密钥失败: status=%d code=%d msg=%s", st, env.Code, env.Msg)
		}
		plain, _ := env.dataMap(t)["key"].(string)
		return "", plain
	}()

	cases := []struct {
		method, path string
		key, session string
	}{
		{http.MethodGet, "/apikey", userKey, token},
		{http.MethodPost, "/apikey", userKey, token},
		{http.MethodGet, "/auth/me", userKey, token},
		{http.MethodPost, "/auth/password", userKey, token},
		{http.MethodGet, "/auth/sessions", userKey, token},
		{http.MethodGet, "/admin/stats", adminKey, adminSession},
		{http.MethodGet, "/admin/users", adminKey, adminSession},
	}

	for _, tc := range cases {
		for _, f := range []struct {
			name    string
			headers map[string]string
			query   string
		}{
			{"X-API-Key", map[string]string{"X-API-Key": tc.key}, ""},
			{"?api_key=", nil, "?api_key=" + tc.key},
		} {
			status, raw := doRaw(t, srv, tc.method, tc.path+f.query, nil, f.headers)
			if status == http.StatusOK {
				t.Errorf("%s %s 用 %s 竟然放行——长期密钥不该能铸造别的密钥、改密码或进管理面（P26）",
					tc.method, tc.path, f.name)
			}
			if status != http.StatusUnauthorized && status != http.StatusForbidden {
				t.Errorf("%s %s 用 %s status = %d, want 401/403（body=%s）",
					tc.method, tc.path, f.name, status, truncate(string(raw), 160))
			}
		}
		// 同一端点用会话凭证必须照常可用：否则上面的「拒绝」可能是守卫整个坏了
		status, raw := doRaw(t, srv, tc.method, tc.path, nil, authHeader(tc.session))
		if status == http.StatusUnauthorized {
			t.Errorf("%s %s 用会话凭证被拒（status=%d, body=%s），守卫本身坏了",
				tc.method, tc.path, status, truncate(string(raw), 160))
		}
	}
}

// TestEnvStaticKeyStaysAnonymous 静态 env 键按设计匿名：能过网关，但进不了任何需要归属的端点
func TestEnvStaticKeyStaysAnonymous(t *testing.T) {
	srv := newTestServer(t)
	_, key := threeFormUser(t, srv)
	// 这条只验归属，不看榜单放行名单，所以取到 403 就算「被身份层挡住」
	old := conf.Config.APIKeys
	conf.Config.APIKeys = []string{"sk_static_p26"}
	t.Cleanup(func() { conf.Config.APIKeys = old })

	for _, path := range []string{"/quota/dashboard", "/user/import-config", "/rank/boards"} {
		status, raw := doRaw(t, srv, http.MethodGet, path+"?api_key=sk_static_p26", nil, nil)
		if status != http.StatusForbidden {
			t.Errorf("%s 用静态 env 键 status = %d, want 403（匿名凭证没有归属账号）body=%s",
				path, status, truncate(string(raw), 160))
		}
	}

	// 用户自己的密钥仍然进得去（证明上一条不是「整组端点都挂了」）
	if status, _ := doRaw(t, srv, http.MethodGet, "/quota/dashboard?api_key="+key, nil, nil); status != http.StatusOK {
		t.Errorf("用户密钥打 /quota/dashboard status = %d, want 200", status)
	}
}

// TestLoginCookieAttributes cookie 参与鉴权，属性就是 CSRF 的第一道闸：改松了必须红
func TestLoginCookieAttributes(t *testing.T) {
	srv := newTestServer(t)
	registerUser(t, srv, "tf_cookie", "tf_cookie@example.com", "pass1234")

	resp, err := http.Post(srv.URL+"/auth/login", "application/json",
		strings.NewReader(`{"username":"tf_cookie","password":"pass1234"}`))
	if err != nil {
		t.Fatalf("登录请求失败: %v", err)
	}
	defer resp.Body.Close()

	sc := resp.Header.Get("Set-Cookie")
	if sc == "" {
		t.Fatalf("登录没有下发 Cookie：%q —— 三形态里的 cookie 那条已经不存在了", sc)
	}
	for _, want := range []string{"loomproxy_token=", "HttpOnly", "SameSite=Lax", "Path=/"} {
		if !strings.Contains(sc, want) {
			t.Errorf("Set-Cookie 缺 %q：%s（cookie 参与鉴权，放宽 Lax 或去掉 HttpOnly 就是 CSRF 面）", want, sc)
		}
	}
}
