package test

// auth 模块集成测试：注册 / 登录（用户名+邮箱双通道）/ 会话管理 / 禁用与软删除。
// 覆盖历史回归：fcb8c43（邮箱登录）、be4f9b1（软删除黑名单）、44db90f（禁用吊销会话）。

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestRegisterAndLoginFlow 注册 → 用户名登录 → GET /auth/me 返回本人资料
func TestRegisterAndLoginFlow(t *testing.T) {
	srv := newTestServer(t)

	token := registerUser(t, srv, "alice", "alice@example.com", "pass1234")
	if token == "" {
		t.Fatal("注册未返回 token")
	}

	token2 := loginUser(t, srv, "alice", "pass1234")

	status, env := doJSON(t, srv, http.MethodGet, "/auth/me", nil, authHeader(token2))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("GET /auth/me 失败（status=%d code=%d msg=%s）", status, env.Code, env.Msg)
	}
	if got := env.dataMap(t)["username"]; got != "alice" {
		t.Fatalf("/auth/me username = %v, want alice", got)
	}
}

// TestLoginWithEmail 回归 fcb8c43：登录支持邮箱（前端单输入框统一填 username 字段）
func TestLoginWithEmail(t *testing.T) {
	srv := newTestServer(t)

	registerUser(t, srv, "bob", "bob@example.com", "pass1234")

	// 前端登录框只提交 username 字段，邮箱也填在这里
	token := loginUser(t, srv, "bob@example.com", "pass1234")
	if token == "" {
		t.Fatal("邮箱登录未返回 token")
	}

	status, env := doJSON(t, srv, http.MethodGet, "/auth/me", nil, authHeader(token))
	if status != http.StatusOK {
		t.Fatalf("邮箱登录后 /auth/me 失败（status=%d）", status)
	}
	if got := env.dataMap(t)["username"]; got != "bob" {
		t.Fatalf("/auth/me username = %v, want bob", got)
	}
}

// TestLoginWrongPassword 错误密码返回 401
func TestLoginWrongPassword(t *testing.T) {
	srv := newTestServer(t)

	registerUser(t, srv, "carol", "carol@example.com", "pass1234")

	status, env := doJSON(t, srv, http.MethodPost, "/auth/login", map[string]interface{}{
		"username": "carol",
		"password": "wrongpass1",
	}, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("错误密码登录 status = %d, want 401（msg=%s）", status, env.Msg)
	}
	if !strings.Contains(env.Msg, "用户名或密码错误") {
		t.Fatalf("错误提示 = %q, want 含「用户名或密码错误」", env.Msg)
	}
}

// TestLoginDisabledUser 回归 44db90f：禁用用户登录 403，且已签发的 token 随会话吊销失效
func TestLoginDisabledUser(t *testing.T) {
	srv := newTestServer(t)

	token := registerUser(t, srv, "dave", "dave@example.com", "pass1234")
	uid := userIDByName(t, "dave")

	// 管理员禁用（status=0，同步吊销全部会话）
	status, env := doJSON(t, srv, http.MethodPatch, "/admin/users/"+itoa(uid), map[string]interface{}{
		"status": 0,
	}, authHeader(adminToken(t, srv)))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("禁用用户失败（status=%d code=%d msg=%s）", status, env.Code, env.Msg)
	}

	// 禁用后登录 403
	status, env = doJSON(t, srv, http.MethodPost, "/auth/login", map[string]interface{}{
		"username": "dave",
		"password": "pass1234",
	}, nil)
	if status != http.StatusForbidden {
		t.Fatalf("禁用用户登录 status = %d, want 403（msg=%s）", status, env.Msg)
	}
	if !strings.Contains(env.Msg, "禁用") {
		t.Fatalf("错误提示 = %q, want 含「禁用」", env.Msg)
	}

	// 禁用前签发的 token 已随会话吊销失效
	status, _ = doJSON(t, srv, http.MethodGet, "/auth/me", nil, authHeader(token))
	if status != http.StatusUnauthorized {
		t.Fatalf("禁用后旧 token 访问 /auth/me status = %d, want 401", status)
	}
}

// TestRegisterDuplicate 重复用户名/邮箱注册返回 409
func TestRegisterDuplicate(t *testing.T) {
	srv := newTestServer(t)

	registerUser(t, srv, "erin", "erin@example.com", "pass1234")

	// 重复用户名
	status, _ := doJSON(t, srv, http.MethodPost, "/auth/register", map[string]interface{}{
		"username": "erin",
		"email":    "erin2@example.com",
		"password": "pass1234",
	}, nil)
	if status != http.StatusConflict {
		t.Fatalf("重复用户名注册 status = %d, want 409", status)
	}

	// 重复邮箱
	status, _ = doJSON(t, srv, http.MethodPost, "/auth/register", map[string]interface{}{
		"username": "erin2",
		"email":    "erin@example.com",
		"password": "pass1234",
	}, nil)
	if status != http.StatusConflict {
		t.Fatalf("重复邮箱注册 status = %d, want 409", status)
	}
}

// TestRegisterReuseSoftDeleted 回归 be4f9b1：软删除用户的用户名/邮箱进黑名单不可注册；
// 恢复（restore）后可正常登录
func TestRegisterReuseSoftDeleted(t *testing.T) {
	srv := newTestServer(t)

	registerUser(t, srv, "frank", "frank@example.com", "pass1234")
	uid := userIDByName(t, "frank")
	admin := authHeader(adminToken(t, srv))

	// 管理员软删除
	status, env := doJSON(t, srv, http.MethodDelete, "/admin/users/"+itoa(uid), nil, admin)
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("删除用户失败（status=%d code=%d msg=%s）", status, env.Code, env.Msg)
	}

	// 同名注册 409「已被注销账号占用」
	status, env = doJSON(t, srv, http.MethodPost, "/auth/register", map[string]interface{}{
		"username": "frank",
		"email":    "frank2@example.com",
		"password": "pass1234",
	}, nil)
	if status != http.StatusConflict {
		t.Fatalf("软删除后同名注册 status = %d, want 409", status)
	}
	if !strings.Contains(env.Msg, "注销") {
		t.Fatalf("错误提示 = %q, want 含「注销」", env.Msg)
	}

	// 同邮箱注册 409
	status, _ = doJSON(t, srv, http.MethodPost, "/auth/register", map[string]interface{}{
		"username": "frank2",
		"email":    "frank@example.com",
		"password": "pass1234",
	}, nil)
	if status != http.StatusConflict {
		t.Fatalf("软删除后同邮箱注册 status = %d, want 409", status)
	}

	// 恢复后可正常登录
	status, env = doJSON(t, srv, http.MethodPost, "/admin/users/"+itoa(uid)+"/restore", nil, admin)
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("恢复用户失败（status=%d code=%d msg=%s）", status, env.Code, env.Msg)
	}
	loginUser(t, srv, "frank", "pass1234")
}

// TestUpdateUserEmailConflict 回归：管理侧改邮箱撞唯一索引时必须 409 说明占用者，
// 而不是让约束错误一路撞到驱动变成没头没尾的 500（users.email 的唯一索引覆盖软删除行）
func TestUpdateUserEmailConflict(t *testing.T) {
	srv := newTestServer(t)
	admin := authHeader(adminToken(t, srv))

	registerUser(t, srv, "helen", "helen@example.com", "pass1234")
	registerUser(t, srv, "ivan", "ivan@example.com", "pass1234")
	ivan := userIDByName(t, "ivan")

	// 活用户的邮箱被抢：409 且点名占用者
	status, env := doJSON(t, srv, http.MethodPatch, "/admin/users/"+itoa(ivan), map[string]interface{}{
		"email": "helen@example.com",
	}, admin)
	if status != http.StatusConflict {
		t.Fatalf("改生存者邮箱 status = %d, want 409（msg=%s）", status, env.Msg)
	}
	if !strings.Contains(env.Msg, itoa(userIDByName(t, "helen"))) {
		t.Errorf("文案应点名占用者 id，实得 %q", env.Msg)
	}

	// 注销用户的邮箱同样被占用：409 且提示「注销」
	doJSON(t, srv, http.MethodDelete, "/admin/users/"+itoa(userIDByName(t, "helen")), nil, admin)
	status, env = doJSON(t, srv, http.MethodPatch, "/admin/users/"+itoa(ivan), map[string]interface{}{
		"email": "helen@example.com",
	}, admin)
	if status != http.StatusConflict {
		t.Fatalf("改成注销账号的邮箱 status = %d, want 409（msg=%s）", status, env.Msg)
	}
	if !strings.Contains(env.Msg, "注销") {
		t.Errorf("文案应区分注销账号占用，实得 %q", env.Msg)
	}

	// 自己填回自己的邮箱不算冲突
	registerUser(t, srv, "judy", "judy@example.com", "pass1234")
	status, env = doJSON(t, srv, http.MethodPatch, "/admin/users/"+itoa(userIDByName(t, "judy")), map[string]interface{}{
		"email": "judy@example.com",
	}, admin)
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("邮箱不变应成功（status=%d code=%d msg=%s）", status, env.Code, env.Msg)
	}

	// 置空邮箱：users.email 声明 not null，写 NULL 会撞约束变 500，必须在入口挡成 400
	status, env = doJSON(t, srv, http.MethodPatch, "/admin/users/"+itoa(ivan), map[string]interface{}{
		"email": "",
	}, admin)
	if status != http.StatusBadRequest {
		t.Fatalf("清空邮箱 status = %d, want 400（msg=%s）", status, env.Msg)
	}
	if !strings.Contains(env.Msg, "邮箱不能清空") {
		t.Errorf("文案应给出可操作提示而不是裸校验错误，实得 %q", env.Msg)
	}

	// 非法邮箱同样 400，不落库
	status, env = doJSON(t, srv, http.MethodPatch, "/admin/users/"+itoa(ivan), map[string]interface{}{
		"email": "not-an-email",
	}, admin)
	if status != http.StatusBadRequest {
		t.Fatalf("非法邮箱 status = %d, want 400（msg=%s）", status, env.Msg)
	}
	if !strings.Contains(env.Msg, "邮箱格式不正确") {
		t.Errorf("文案应中文化 gin validator 的英文原文，实得 %q", env.Msg)
	}
}

// TestLogoutRevokesSession logout 吊销当前会话，原 token 立即失效
func TestLogoutRevokesSession(t *testing.T) {
	srv := newTestServer(t)

	token := registerUser(t, srv, "grace", "grace@example.com", "pass1234")

	status, env := doJSON(t, srv, http.MethodPost, "/auth/logout", nil, authHeader(token))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("logout 失败（status=%d code=%d msg=%s）", status, env.Code, env.Msg)
	}

	status, _ = doJSON(t, srv, http.MethodGet, "/auth/me", nil, authHeader(token))
	if status != http.StatusUnauthorized {
		t.Fatalf("logout 后旧 token 访问 /auth/me status = %d, want 401", status)
	}
}

// TestMeRequiresAuth 无凭证访问受保护接口返回 401
func TestMeRequiresAuth(t *testing.T) {
	srv := newTestServer(t)

	status, _ := doJSON(t, srv, http.MethodGet, "/auth/me", nil, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("无 token 访问 /auth/me status = %d, want 401", status)
	}
}

// jwtPayload 解析 JWT 的 payload（不验签，仅测试用）
func jwtPayload(t *testing.T, token string) map[string]interface{} {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT 格式错误: %d 段", len(parts))
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("解码 JWT payload 失败: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("解析 JWT payload 失败: %v", err)
	}
	return m
}

// TestLoginNeverExpires 系统设置 jwt_expire_hours=-1 时签发无 exp 的永久 token
func TestLoginNeverExpires(t *testing.T) {
	srv := newTestServer(t)

	status, env := doJSON(t, srv, http.MethodPut, "/admin/settings/jwt_expire_hours",
		map[string]interface{}{"value": "-1"}, authHeader(adminToken(t, srv)))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("设置 jwt_expire_hours=-1 失败（status=%d msg=%s）", status, env.Msg)
	}

	// 注册（注册即签发 token）
	status, env = doJSON(t, srv, http.MethodPost, "/auth/register", map[string]interface{}{
		"username": "ne_user", "email": "ne_user@example.com", "password": "pass1234",
	}, nil)
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("注册失败（status=%d msg=%s）", status, env.Msg)
	}
	data := env.dataMap(t)
	if data["expires_at"] != nil {
		t.Fatalf("expires_at = %v, want null（永不过期）", data["expires_at"])
	}
	token := data["token"].(string)
	if _, hasExp := jwtPayload(t, token)["exp"]; hasExp {
		t.Fatal("token 不应包含 exp 声明（永不过期）")
	}

	// 永久 token 可用
	status, _ = doJSON(t, srv, http.MethodGet, "/auth/me", nil, authHeader(token))
	if status != http.StatusOK {
		t.Fatalf("永久 token 访问 /auth/me status = %d, want 200", status)
	}

	// 个人设置 -1 同样生效（覆盖系统默认值）：先把系统改回 168
	doJSON(t, srv, http.MethodPut, "/admin/settings/jwt_expire_hours",
		map[string]interface{}{"value": "168"}, authHeader(adminToken(t, srv)))
	status, env = doJSON(t, srv, http.MethodPatch, "/auth/me",
		map[string]interface{}{"email": "ne_user@example.com", "token_expire_hours": -1}, authHeader(token))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("个人设置 token_expire_hours=-1 失败（status=%d msg=%s）", status, env.Msg)
	}
	token2 := loginUser(t, srv, "ne_user", "pass1234")
	if _, hasExp := jwtPayload(t, token2)["exp"]; hasExp {
		t.Fatal("个人设置 -1 后签发的 token 不应包含 exp 声明")
	}

	// 个人正值覆盖系统 -1：设回个人 2 小时，应恢复带 exp
	doJSON(t, srv, http.MethodPatch, "/auth/me",
		map[string]interface{}{"email": "ne_user@example.com", "token_expire_hours": 2}, authHeader(token2))
	token3 := loginUser(t, srv, "ne_user", "pass1234")
	if _, hasExp := jwtPayload(t, token3)["exp"]; !hasExp {
		t.Fatal("个人设置 2 小时后签发的 token 应包含 exp 声明")
	}
}
