package groupb

// 登录接口防爆破集成测试：/auth/login 按客户端 IP 限频（10 次/分钟）+ 连续失败 10 次锁定 1 小时。
// 限频器为进程级状态且按 ClientIP 计数：测试经 X-Forwarded-For 区分来源 IP 实现用例间隔离
// （CreateApp 信任 127.0.0.1 代理，httptest 直连即本机，XFF 会被采纳还原为客户端 IP）。

import (
	"loomproxy/testkit"
	"net/http"
	"net/http/httptest"
	"testing"
)

func loginFromIP(t *testing.T, srv *httptest.Server, username, password, ip string) (int, apiEnvelope) {
	t.Helper()
	status, env := testkit.LoginFromIP(t, srv, username, password, ip)
	return status, apiEnvelope{env} // 信封包装在本包各有一份，见 forward 页
}

// TestLoginFailStreakLockout 连续 10 次密码错误后锁定：第 11 次即使密码正确也返回 429
func TestLoginFailStreakLockout(t *testing.T) {
	srv := newTestServer(t)
	registerUser(t, srv, "lockstreak", "lockstreak@example.com", "passw0rd123")
	ip := "10.66.1.1"

	for i := 1; i <= 10; i++ {
		status, _ := loginFromIP(t, srv, "lockstreak", "wrong-pass", ip)
		if status != http.StatusUnauthorized {
			t.Fatalf("第 %d 次错误登录期望 401，实际 %d", i, status)
		}
	}

	status, env := loginFromIP(t, srv, "lockstreak", "passw0rd123", ip)
	if status != http.StatusTooManyRequests {
		t.Fatalf("连续失败锁定后正确密码仍应 429，实际 %d（%s）", status, env.Msg)
	}
}

// TestLoginPerMinuteLockout 每分钟超过 10 次尝试即锁定（成败混合、失败不连续也计），
// 与连续失败锁定相互独立
func TestLoginPerMinuteLockout(t *testing.T) {
	srv := newTestServer(t)
	registerUser(t, srv, "lockminute", "lockminute@example.com", "passw0rd123")
	ip := "10.66.2.1"

	// 成败交替共 11 次（失败不连续，不触发连败锁定），均不应被提前拦截
	for i := 1; i <= 11; i++ {
		password := "wrong-pass"
		expect := http.StatusUnauthorized
		if i%2 == 0 {
			password = "passw0rd123"
			expect = http.StatusOK
		}
		status, env := loginFromIP(t, srv, "lockminute", password, ip)
		if status != expect {
			t.Fatalf("第 %d 次尝试期望 %d，实际 %d（%s）", i, expect, status, env.Msg)
		}
	}

	status, _ := loginFromIP(t, srv, "lockminute", "passw0rd123", ip)
	if status != http.StatusTooManyRequests {
		t.Fatalf("超过每分钟尝试上限后应 429，实际 %d", status)
	}
}

// TestLoginLockoutIsolatedByIP 锁定按 IP 隔离：一个 IP 被锁不影响其他来源正常登录
func TestLoginLockoutIsolatedByIP(t *testing.T) {
	srv := newTestServer(t)
	registerUser(t, srv, "lockiso", "lockiso@example.com", "passw0rd123")

	for i := 1; i <= 10; i++ {
		loginFromIP(t, srv, "lockiso", "wrong-pass", "10.66.3.1")
	}
	if status, _ := loginFromIP(t, srv, "lockiso", "wrong-pass", "10.66.3.1"); status != http.StatusTooManyRequests {
		t.Fatalf("锁定 IP 的再次尝试应 429，实际 %d", status)
	}

	// 其他 IP 的正常登录不受影响
	status, env := loginFromIP(t, srv, "lockiso", "passw0rd123", "10.66.3.2")
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("未锁定 IP 的正常登录应成功，实际 %d（%s）", status, env.Msg)
	}
}

// TestSecurityAttemptsReadsAndResetsLock 管理面能把「谁被锁了」读出来，并且解锁真起作用。
// 最后那一条是关键：只断言读数好看，端点就可能变成一个更贵的「什么都解不了」的按钮。
func TestSecurityAttemptsReadsAndResetsLock(t *testing.T) {
	srv := newTestServer(t)
	registerUser(t, srv, "lockpanel", "lockpanel@example.com", "passw0rd123")
	admin := adminToken(t, srv)
	attacker := "10.66.4.1"

	for i := 1; i <= 10; i++ {
		loginFromIP(t, srv, "lockpanel", "wrong-pass", attacker)
	}
	if status, _ := loginFromIP(t, srv, "lockpanel", "passw0rd123", attacker); status != http.StatusTooManyRequests {
		t.Fatalf("前置条件没成立：这个 IP 应该已经被锁（期望 429，实际 %d）", status)
	}

	status, env := doJSON(t, srv, http.MethodGet, "/admin/security/attempts", nil, authHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("attempts 期望 200，实际 %d（%s）", status, env.Msg)
	}
	items, _ := env.dataMap(t)["items"].([]interface{})
	var hit map[string]interface{}
	for _, r := range items {
		it, _ := r.(map[string]interface{})
		if it["kind"] == "login" && it["ip"] == attacker {
			hit = it
		}
	}
	if hit == nil {
		t.Fatalf("快照里没有刚被锁的 %s（共 %d 条）", attacker, len(items))
	}
	if locked, _ := hit["locked"].(bool); !locked {
		t.Errorf("快照说它没在锁里，可它刚吃过 429：%+v", hit)
	}
	if streak, _ := hit["fail_streak"].(float64); streak < 10 {
		t.Errorf("失败连击应 ≥10，实得 %v", hit["fail_streak"])
	}
	// 锁着的排前面：第一条必须是 locked=true（面板靠这个顺序把「正在发生的」放在最上）
	if first, _ := items[0].(map[string]interface{}); first["kind"] == "login" {
		if locked, _ := first["locked"].(bool); !locked {
			t.Errorf("排序判据没生效：login 的第一条不是锁定项 %+v", first)
		}
	}

	status, env = doJSON(t, srv, http.MethodPost, "/admin/security/attempts/reset",
		map[string]string{"ip": attacker}, authHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("reset 期望 200，实际 %d（%s）", status, env.Msg)
	}
	if cleared, _ := env.dataMap(t)["cleared"].(float64); cleared < 1 {
		t.Fatalf("reset 说清掉 %v 条——没清掉任何一条就等于这个按钮是空的", cleared)
	}

	// 清完再读：那条记录应已消失（不是「还在但 locked=false」——删行才是 reset 的语义）。
	// 必须紧挨着 reset 做：后面那次成功登录会重新建一条，顺序颠倒了就测出个假红。
	_, env = doJSON(t, srv, http.MethodGet, "/admin/security/attempts", nil, authHeader(admin))
	items, _ = env.dataMap(t)["items"].([]interface{})
	for _, r := range items {
		it, _ := r.(map[string]interface{})
		if it["kind"] == "login" && it["ip"] == attacker {
			t.Errorf("reset 之后快照里仍有 %s 的登录记录：%+v", attacker, it)
		}
	}

	// 解锁后同一 IP 必须立刻能登录：被测的是「生效」，不是「返回 200」
	if status, env := loginFromIP(t, srv, "lockpanel", "passw0rd123", attacker); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("解锁后同一 IP 仍被拒：status=%d msg=%s", status, env.Msg)
	}
}

// TestSecurityAttemptsIsSessionOnlyAndValidates 端点边界：与管理面其余部分同一门槛（只认会话），
// 且非法 IP 必须当场 400——否则「写错一个字的解锁」会伪装成「解锁成功但清掉 0 条」。
func TestSecurityAttemptsIsSessionOnlyAndValidates(t *testing.T) {
	srv := newTestServer(t)
	_, key := threeFormUser(t, srv)
	admin := adminToken(t, srv)

	for _, tc := range []struct {
		method, path string
		body         map[string]string
	}{
		{http.MethodGet, "/admin/security/attempts", nil},
		{http.MethodPost, "/admin/security/attempts/reset", map[string]string{"ip": "10.66.5.1"}},
	} {
		for _, k := range []map[string]string{{"X-API-Key": key}, nil} {
			query := ""
			headers := k
			if headers == nil {
				query = "?api_key=" + key
				headers = map[string]string{}
			}
			status, _ := doJSON(t, srv, tc.method, tc.path+query, tc.body, headers)
			if status == http.StatusOK {
				t.Errorf("%s %s 用 API Key 竟然放行（P26：/admin/* 只认会话）", tc.method, tc.path)
			}
			if status != http.StatusUnauthorized && status != http.StatusForbidden {
				t.Errorf("%s %s 用 API Key 期望 401/403，实际 %d", tc.method, tc.path, status)
			}
		}
	}

	// 非法与缺失的 IP：400；没有记录的合法 IP：200 且 cleared=0
	for _, bad := range []string{"1.2.3", "not-an-ip", ""} {
		status, env := doJSON(t, srv, http.MethodPost, "/admin/security/attempts/reset",
			map[string]string{"ip": bad}, authHeader(admin))
		if status != http.StatusBadRequest {
			t.Errorf("ip=%q 应 400，实际 %d（msg=%s）", bad, status, env.Msg)
		}
	}
	status, env := doJSON(t, srv, http.MethodPost, "/admin/security/attempts/reset",
		map[string]string{"ip": "10.66.9.9"}, authHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("清一个没有记录的合法 IP 应 200，实际 %d（%s）", status, env.Msg)
	}
	if cleared, _ := env.dataMap(t)["cleared"].(float64); cleared != 0 {
		t.Errorf("没有记录时 cleared 应为 0，实得 %v", cleared)
	}
}
