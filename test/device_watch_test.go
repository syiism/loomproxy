package test

// 多设备登录监控与自动处置（待办清单 P44）。
//
// 这一组用例里最重要的一条是**反向断言**：IP 再多也不许踢人。
// 一个出口 IP 后面可能是整家公司或一整个家庭，按 IP 处置必然大面积误伤；
// IP 数只用于面板标红，处置的唯一判据是活跃会话数。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"loomproxy/db"
	"loomproxy/models"
)

var watchSeq atomic.Int64

// setSetting 写设置项并立刻失效缓存（`db.GetSetting` 有 10 秒内存缓存，不失效会读到旧值）
func setSetting(t *testing.T, srv *httptest.Server, admin, key, value string) {
	t.Helper()
	status, env := doJSON(t, srv, http.MethodPut, "/admin/settings/"+key,
		map[string]string{"value": value}, authHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("设置 %s=%s 失败: status=%d code=%d msg=%s", key, value, status, env.Code, env.Msg)
	}
	db.InvalidateSettingCache(key)
}

// loginFrom 从指定 IP 登录一次，返回该会话的 token
func loginFrom(t *testing.T, srv *httptest.Server, username, password, ip string) string {
	t.Helper()
	status, env := doJSON(t, srv, http.MethodPost, "/auth/login",
		map[string]string{"username": username, "password": password},
		map[string]string{"X-Forwarded-For": ip})
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("从 %s 登录失败: status=%d msg=%s", ip, status, env.Msg)
	}
	data := env.dataMap(t)
	token, _ := data["token"].(string)
	if token == "" {
		t.Fatalf("登录响应没有 token: %+v", data)
	}
	return token
}

func activeSessionCount(t *testing.T, userID uint) int64 {
	t.Helper()
	var n int64
	if err := db.DB.Model(&models.AuthSession{}).
		Where("user_id = ? AND revoked_at IS NULL AND expires_at > ?", userID, time.Now()).
		Count(&n).Error; err != nil {
		t.Fatalf("数活跃会话失败: %v", err)
	}
	return n
}

func watchUser(t *testing.T, srv *httptest.Server, admin string) (uint, string) {
	t.Helper()
	name := fmt.Sprintf("dw_%d", watchSeq.Add(1))
	registerUser(t, srv, name, name+"@example.com", "pass1234")
	uid := userIDByName(t, name)
	return uid, name
}

// TestDeviceWatchDisabledByDefault 默认档必须实测：开关关着时，超上限一个都不许移出。
// 这条不是形式——「配置默认关」如果没人读它到底关着会做什么，它就只是个字符串。
func TestDeviceWatchDisabledByDefault(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)
	uid, name := watchUser(t, srv, admin)

	setSetting(t, srv, admin, "max_active_sessions", "2")
	// 不写 device_watch_enabled：它默认 false，用例要验的就是这个默认值
	if db.GetSetting("device_watch_enabled") != "false" {
		t.Fatalf("device_watch_enabled 默认不是 false，实得 %q", db.GetSetting("device_watch_enabled"))
	}

	// 注册本身就签发一个会话（Register 也走 issueToken），所以是 1 + 5 = 6 个
	for i := 1; i <= 5; i++ {
		loginFrom(t, srv, name, "pass1234", fmt.Sprintf("10.77.1.%d", i))
	}
	if got := activeSessionCount(t, uid); got != 6 {
		t.Errorf("开关关闭时活跃会话应原样保留 6 个（注册 1 + 登录 5），实得 %d——默认档就在偷偷踢人", got)
	}
	if got := activeSessionCount(t, uid); got <= 2 {
		t.Errorf("会话数掉到上限（2）以内了（实得 %d），说明开关关着仍在收口", got)
	}
}

func TestDeviceWatchRevokesExcessOnLogin(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)
	uid, name := watchUser(t, srv, admin)

	setSetting(t, srv, admin, "device_watch_enabled", "true")
	setSetting(t, srv, admin, "max_active_sessions", "2")
	t.Cleanup(func() { setSetting(t, srv, admin, "device_watch_enabled", "false") })

	// 上限 2：注册那次会话 + 第一次登录 = 2 个，都还在名额内
	first := loginFrom(t, srv, name, "pass1234", "10.77.2.1")
	if got := activeSessionCount(t, uid); got != 2 {
		t.Fatalf("上限 2 时注册+一次登录后应恰好 2 个活跃会话，实得 %d", got)
	}
	// 再登两次：每次都把名额压回 2 个，被移出的总是最久未活跃的那个
	loginFrom(t, srv, name, "pass1234", "10.77.2.2")
	third := loginFrom(t, srv, name, "pass1234", "10.77.2.3")
	if got := activeSessionCount(t, uid); got != 2 {
		t.Fatalf("登录后应收敛到上限 2 个，实得 %d", got)
	}

	// 被移出的是**最久未活跃**的那个，不是最近的
	if status, _ := doJSON(t, srv, http.MethodGet, "/auth/me", nil, authHeader(first)); status != http.StatusUnauthorized {
		t.Errorf("最早那个会话应已被移出（期望 401），实得 %d", status)
	}
	if status, env := doJSON(t, srv, http.MethodGet, "/auth/me", nil, authHeader(third)); status != http.StatusOK || env.Code != 0 {
		t.Errorf("当前会话不该被移出，实得 status=%d msg=%s", status, env.Msg)
	}

	// 处置留痕在 journal 里，库里只有 revoked_at：日志是唯一能分清「系统踢的」还是「用户自己点的」的地方
	var revoked int64
	db.DB.Model(&models.AuthSession{}).Where("user_id = ? AND revoked_at IS NOT NULL", uid).Count(&revoked)
	if revoked < 1 {
		t.Errorf("库里没有任何 revoked_at 被写上，revoked=%d", revoked)
	}
}

// TestDeviceWatchNoticeOnNextLogin 移出提示走登录响应带回，不建站内信表。
//
// 顺序敏感的地方：提示算的是「**上次登录以来**被移出的会话数」，所以旧值必须在
// `last_login_at` 被本次覆盖之前取。写成覆盖之后取，本次请求内发生的移出照样能算到
// （revoked_at 比 now 只晚几微秒），**漏掉的是「本次登录之前」那一段**——
// 也就是最需要提醒的那一段。所以这条用例故意把移出放在两次登录之间，而不是靠登录时的自动收口。
func TestDeviceWatchNoticeOnNextLogin(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)
	_, name := watchUser(t, srv, admin)

	// 上限调宽，让这次登录**不触发**自动收口：提示必须来自「上次登录之后、这次之前」的移出
	setSetting(t, srv, admin, "max_active_sessions", "10")

	t1 := loginFrom(t, srv, name, "pass1234", "10.77.3.1") // 这次登录更新 last_login_at
	loginFrom(t, srv, name, "pass1234", "10.77.3.2")       // 再来一个会话
	// 用 t1 点「登出其他设备」：移出发生在上次登录之后、下一次登录之前
	if status, env := doJSON(t, srv, http.MethodPost, "/auth/sessions/revoke-others", nil, authHeader(t1)); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("登出其他设备失败: %d %s", status, env.Msg)
	}

	status, env := doJSON(t, srv, http.MethodPost, "/auth/login",
		map[string]string{"username": name, "password": "pass1234"},
		map[string]string{"X-Forwarded-For": "10.77.3.3"})
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("第三次登录失败: %d %s", status, env.Msg)
	}
	notice, _ := env.dataMap(t)["notice"].(string)
	if notice == "" {
		t.Errorf("登录响应没有 notice：上次登录之后明明移出过设备——取旧 last_login_at 的顺序写反了")
	}
}

// TestDeviceWatchNeverRevokesOnIpCount 判据的反向断言：来源 IP 再多也不是处置理由。
// 上限设得很宽（10），但窗口内有 8 个不同 IP——一个会话都不许被移出。
func TestDeviceWatchNeverRevokesOnIpCount(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)
	uid, name := watchUser(t, srv, admin)

	setSetting(t, srv, admin, "device_watch_enabled", "true")
	setSetting(t, srv, admin, "max_active_sessions", "10")
	setSetting(t, srv, admin, "suspect_distinct_ips", "3") // 故意调低：让这条只影响标红，不影响处置
	t.Cleanup(func() { setSetting(t, srv, admin, "device_watch_enabled", "false") })

	for i := 1; i <= 8; i++ {
		loginFrom(t, srv, name, "pass1234", fmt.Sprintf("10.77.4.%d", i))
	}
	// 顺手再制造一批调用来源 IP（密钥调用也归属到用户名下，读数含它）
	for i := 1; i <= 4; i++ {
		token := loginFrom(t, srv, name, "pass1234", fmt.Sprintf("10.88.9.%d", i))
		if status, _ := doJSON(t, srv, http.MethodGet, "/fake_a/search"+buildQuery(map[string]string{"key": "x"}), nil, authHeader(token)); status == 0 {
			t.Fatalf("假源调用未发出")
		}
	}

	if got := activeSessionCount(t, uid); got < 9 {
		t.Errorf("活跃会话从 %d 掉到低于 9——按 IP 数处置了？P44 的判据是只按会话数", got)
	}
}

func TestDeviceActivityEndpointShapeAndPermissions(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)
	uid, name := watchUser(t, srv, admin)
	loginFrom(t, srv, name, "pass1234", "10.77.5.7")

	status, env := doJSON(t, srv, http.MethodGet, "/admin/devices", nil, authHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("/admin/devices status=%d msg=%s", status, env.Msg)
	}
	data := env.dataMap(t)
	cfg, _ := data["config"].(map[string]interface{})
	if cfg == nil {
		t.Fatalf("响应没有 config——IP 数不写口径就没法读（阈值是多少、窗口多长、开关开没开）")
	}
	for _, k := range []string{"device_watch_enabled", "max_active_sessions", "suspect_distinct_ips", "window_days"} {
		if _, ok := cfg[k]; !ok {
			t.Errorf("config 缺 %s 字段：%+v", k, cfg)
		}
	}
	rows, _ := data["list"].([]interface{})
	var hit map[string]interface{}
	for _, r := range rows {
		m, _ := r.(map[string]interface{})
		if m["username"] == name {
			hit = m
		}
	}
	if hit == nil {
		t.Fatalf("列表里没有刚登录过的 %s（%d 行）", name, len(rows))
	}
	if n, _ := hit["login_ips"].(float64); n < 1 {
		t.Errorf("login_ips = %v, want ≥1", hit["login_ips"])
	}
	if v := hit["user_id"]; v != float64(uid) {
		t.Errorf("user_id = %v, want %d", v, uid)
	}

	// 普通用户不该看见全站的设备分布；管理员的长期密钥也不行（/admin/* 只认会话）
	_, key := threeFormUser(t, srv)
	userTok := registerUser(t, srv, "dw_other", "dw_other@example.com", "pass1234")
	for _, tc := range []struct {
		name    string
		headers map[string]string
		query   string
	}{
		{"普通用户会话", authHeader(userTok), ""},
		{"管理员密钥（头）", map[string]string{"X-API-Key": key}, ""},
		{"管理员密钥（query）", nil, "?api_key=" + key},
	} {
		st, _ := doJSON(t, srv, http.MethodGet, "/admin/devices"+tc.query, nil, tc.headers)
		if st == http.StatusOK {
			t.Errorf("%s 竟能读全站设备分布", tc.name)
		}
		if st != http.StatusUnauthorized && st != http.StatusForbidden {
			t.Errorf("%s 期望 401/403，实得 %d", tc.name, st)
		}
	}
}
