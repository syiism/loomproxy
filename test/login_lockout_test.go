package test

// 登录接口防爆破集成测试：/auth/login 按客户端 IP 限频（10 次/分钟）+ 连续失败 10 次锁定 1 小时。
// 限频器为进程级状态且按 ClientIP 计数：测试经 X-Forwarded-For 区分来源 IP 实现用例间隔离
// （CreateApp 信任 127.0.0.1 代理，httptest 直连即本机，XFF 会被采纳还原为客户端 IP）。

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func loginFromIP(t *testing.T, srv *httptest.Server, username, password, ip string) (int, apiEnvelope) {
	t.Helper()
	return doJSON(t, srv, http.MethodPost, "/auth/login", map[string]interface{}{
		"username": username,
		"password": password,
	}, map[string]string{"X-Forwarded-For": ip})
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
