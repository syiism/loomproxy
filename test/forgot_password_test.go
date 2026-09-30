package test

// 找回密码集成测试：/auth/forgot-password（用户名+邮箱验证 → 重置密码 → 吊销旧会话并自动登录）。
// 注意：找回限频器为包级内存态（按 IP，5 次/分钟），本文件记录的尝试次数需控制在阈值内。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func forgotPassword(t *testing.T, srv *httptest.Server, username, email, newPassword, confirmPassword string) (int, apiEnvelope) {
	t.Helper()
	return doJSON(t, srv, http.MethodPost, "/auth/forgot-password", map[string]interface{}{
		"username":         username,
		"email":            email,
		"new_password":     newPassword,
		"confirm_password": confirmPassword,
	}, nil)
}

// TestForgotPasswordFlow 找回成功主路径：重置密码、吊销旧会话、返回新 token 自动登录
func TestForgotPasswordFlow(t *testing.T) {
	srv := newTestServer(t)

	oldToken := registerUser(t, srv, "hank", "hank@example.com", "pass1234")

	status, env := forgotPassword(t, srv, "hank", "hank@example.com", "newpass5678", "newpass5678")
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("找回密码失败（status=%d code=%d msg=%s）", status, env.Code, env.Msg)
	}
	data := env.dataMap(t)
	token, _ := data["token"].(string)
	if token == "" {
		t.Fatal("找回密码响应缺少 token（应自动登录）")
	}
	if got := data["user"].(map[string]interface{})["username"]; got != "hank" {
		t.Fatalf("找回密码响应 user.username = %v, want hank", got)
	}

	// 旧密码已失效
	status, _ = doJSON(t, srv, http.MethodPost, "/auth/login", map[string]interface{}{
		"username": "hank",
		"password": "pass1234",
	}, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("旧密码登录 status = %d, want 401", status)
	}

	// 旧会话已吊销（找回前签发的 token 失效）
	status, _ = doJSON(t, srv, http.MethodGet, "/auth/me", nil, authHeader(oldToken))
	if status != http.StatusUnauthorized {
		t.Fatalf("旧 token 访问 /auth/me status = %d, want 401（旧会话应被吊销）", status)
	}

	// 新 token 可直接使用（自动登录）
	status, env = doJSON(t, srv, http.MethodGet, "/auth/me", nil, authHeader(token))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("新 token 访问 /auth/me 失败（status=%d msg=%s）", status, env.Msg)
	}

	// 新密码可正常登录
	loginUser(t, srv, "hank", "newpass5678")
}

// TestForgotPasswordMismatch 用户名不存在与邮箱不匹配返回统一的模糊错误（防账号枚举），密码不变
func TestForgotPasswordMismatch(t *testing.T) {
	srv := newTestServer(t)

	registerUser(t, srv, "iris", "iris@example.com", "pass1234")

	// 邮箱不匹配
	status, env := forgotPassword(t, srv, "iris", "iris-other@example.com", "newpass5678", "newpass5678")
	if status != http.StatusBadRequest {
		t.Fatalf("邮箱不匹配 status = %d, want 400（msg=%s）", status, env.Msg)
	}
	if !strings.Contains(env.Msg, "不匹配") {
		t.Fatalf("错误提示 = %q, want 含「不匹配」", env.Msg)
	}

	// 用户名不存在：同样的模糊错误
	status, env = forgotPassword(t, srv, "no_such_user", "no_such@example.com", "newpass5678", "newpass5678")
	if status != http.StatusBadRequest {
		t.Fatalf("用户不存在 status = %d, want 400（msg=%s）", status, env.Msg)
	}
	if !strings.Contains(env.Msg, "不匹配") {
		t.Fatalf("错误提示 = %q, want 含「不匹配」（与用户不存在保持一致，防枚举）", env.Msg)
	}

	// 密码未被修改
	loginUser(t, srv, "iris", "pass1234")
}

// TestForgotPasswordValidation 参数校验：两次密码不一致、弱密码、缺字段
func TestForgotPasswordValidation(t *testing.T) {
	srv := newTestServer(t)

	registerUser(t, srv, "judy", "judy@example.com", "pass1234")

	// 两次密码不一致
	status, env := forgotPassword(t, srv, "judy", "judy@example.com", "newpass5678", "otherpass90")
	if status != http.StatusBadRequest || !strings.Contains(env.Msg, "不一致") {
		t.Fatalf("两次密码不一致 status = %d msg = %q, want 400 含「不一致」", status, env.Msg)
	}

	// 弱密码（纯数字）
	status, env = forgotPassword(t, srv, "judy", "judy@example.com", "12345678", "12345678")
	if status != http.StatusBadRequest || !strings.Contains(env.Msg, "字母") {
		t.Fatalf("弱密码 status = %d msg = %q, want 400 含「字母」", status, env.Msg)
	}

	// 缺字段
	status, env = doJSON(t, srv, http.MethodPost, "/auth/forgot-password", map[string]interface{}{
		"username": "judy",
	}, nil)
	if status != http.StatusBadRequest {
		t.Fatalf("缺字段 status = %d, want 400（msg=%s）", status, env.Msg)
	}

	// 均未生效，原密码仍可登录
	loginUser(t, srv, "judy", "pass1234")
}

// TestForgotPasswordDisabledUser 禁用用户找回密码返回 403
func TestForgotPasswordDisabledUser(t *testing.T) {
	srv := newTestServer(t)

	registerUser(t, srv, "kent", "kent@example.com", "pass1234")
	uid := userIDByName(t, "kent")

	status, env := doJSON(t, srv, http.MethodPatch, "/admin/users/"+itoa(uid), map[string]interface{}{
		"status": 0,
	}, authHeader(adminToken(t, srv)))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("禁用用户失败（status=%d code=%d msg=%s）", status, env.Code, env.Msg)
	}

	status, env = forgotPassword(t, srv, "kent", "kent@example.com", "newpass5678", "newpass5678")
	if status != http.StatusForbidden || !strings.Contains(env.Msg, "禁用") {
		t.Fatalf("禁用用户找回密码 status = %d msg = %q, want 403 含「禁用」", status, env.Msg)
	}
}
