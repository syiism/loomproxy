package test

// 验证码超前设计集成测试：场景开关 / 发码限频 / 校验消费 / mock 与通用 http 通道。
// 默认（verify_code_scenes 留空）全部场景关闭，register/forgot 行为与历史一致。

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"loomproxy-go/db"
	"loomproxy-go/handlers/verify"
)

// enableVerifyScene 启用指定场景（写设置 + 失效 10s 缓存）
func enableVerifyScene(t *testing.T, scene string) {
	t.Helper()
	if err := db.DB.Exec("UPDATE system_settings SET value = ? WHERE `key` = 'verify_code_scenes'", scene).Error; err != nil {
		t.Fatalf("启用场景 %s 失败: %v", scene, err)
	}
	db.InvalidateSettingCache("verify_code_scenes")
}

// setVerifySetting 写单个 verify_* 设置并失效缓存
func setVerifySetting(t *testing.T, key, value string) {
	t.Helper()
	if err := db.DB.Exec("UPDATE system_settings SET value = ? WHERE `key` = ?", value, key).Error; err != nil {
		t.Fatalf("写设置 %s 失败: %v", key, err)
	}
	db.InvalidateSettingCache(key)
}

// sendCode 调 /verify/send，返回状态码与信封
func sendCode(t *testing.T, srv *httptest.Server, scene, target string) (int, apiEnvelope) {
	t.Helper()
	return doJSON(t, srv, http.MethodPost, "/verify/send",
		map[string]interface{}{"scene": scene, "target": target}, nil)
}

func countConsumedCodes(t *testing.T, scene, target string) int64 {
	t.Helper()
	var n int64
	db.DB.Table("verification_codes").
		Where("scene = ? AND target = ? AND consumed_at IS NOT NULL", scene, target).
		Count(&n)
	return n
}

func TestVerifyConfigAndDisabledScene(t *testing.T) {
	srv := newTestServer(t)

	// 默认全关：config 返回空场景
	status, env := doJSON(t, srv, http.MethodGet, "/verify/config", nil, nil)
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("verify/config 失败（status=%d code=%d msg=%s）", status, env.Code, env.Msg)
	}
	if scenes, _ := env.dataMap(t)["scenes"].([]interface{}); len(scenes) != 0 {
		t.Fatalf("默认场景应为空，实际 %v", scenes)
	}

	// 场景未启用时发码 400
	if status, _ = sendCode(t, srv, "register", "a@b.cn"); status != http.StatusBadRequest {
		t.Fatalf("场景未启用发码应 400，实际 %d", status)
	}

	// 关闭状态下注册无需验证码（历史行为回归）
	if status, env = doJSON(t, srv, http.MethodPost, "/auth/register", map[string]interface{}{
		"username": "noscode", "email": "noscode@t.cn", "password": "passw0rd1",
	}, nil); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("关闭场景注册应成功（status=%d code=%d msg=%s）", status, env.Code, env.Msg)
	}
}

func TestVerifyRegisterFlow(t *testing.T) {
	srv := newTestServer(t)
	enableVerifyScene(t, "register")

	// config 回显场景
	_, env := doJSON(t, srv, http.MethodGet, "/verify/config", nil, nil)
	if scenes, _ := env.dataMap(t)["scenes"].([]interface{}); len(scenes) != 1 || scenes[0] != "register" {
		t.Fatalf("config 场景回显错误: %v", scenes)
	}

	// 未取码直接注册 → 400
	if status, _ := doJSON(t, srv, http.MethodPost, "/auth/register", map[string]interface{}{
		"username": "vcreg1", "email": "vcreg1@t.cn", "password": "passw0rd1",
	}, nil); status != http.StatusBadRequest {
		t.Fatalf("未取码注册应 400，实际 %d", status)
	}

	// 发码（mock）→ 错误码被拒
	if status, _ := sendCode(t, srv, "register", "vcreg2@t.cn"); status != http.StatusOK {
		t.Fatalf("发码失败: %d", status)
	}
	code := verify.MockLastCode("register", "vcreg2@t.cn")
	if len(code) != 6 {
		t.Fatalf("mock 验证码应为 6 位数字，实际 %q", code)
	}
	if status, _ := doJSON(t, srv, http.MethodPost, "/auth/register", map[string]interface{}{
		"username": "vcreg2", "email": "vcreg2@t.cn", "password": "passw0rd1", "verification_code": "000000",
	}, nil); status != http.StatusBadRequest {
		t.Fatalf("错误验证码注册应 400，实际 %d", status)
	}

	// 冷却期内重复发码 → 429
	if status, _ := sendCode(t, srv, "register", "vcreg2@t.cn"); status != http.StatusTooManyRequests {
		t.Fatalf("冷却期内发码应 429，实际 %d", status)
	}

	// 正确验证码 → 注册成功且码被消费
	if status, env := doJSON(t, srv, http.MethodPost, "/auth/register", map[string]interface{}{
		"username": "vcreg2", "email": "vcreg2@t.cn", "password": "passw0rd1", "verification_code": code,
	}, nil); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("正确验证码注册失败（status=%d code=%d msg=%s）", status, env.Code, env.Msg)
	}
	if countConsumedCodes(t, "register", "vcreg2@t.cn") == 0 {
		t.Fatal("注册成功后验证码应已消费")
	}
}

func TestVerifyMaxAttempts(t *testing.T) {
	srv := newTestServer(t)
	enableVerifyScene(t, "register")

	if status, _ := sendCode(t, srv, "register", "vcmax@t.cn"); status != http.StatusOK {
		t.Fatalf("发码失败: %d", status)
	}
	// 连续错 5 次后作废（verify_code_max_attempts 默认 5，第 5 次错误即提示作废）
	for i := 1; i <= 5; i++ {
		status, _ := doJSON(t, srv, http.MethodPost, "/auth/register", map[string]interface{}{
			"username": "vcmax", "email": "vcmax@t.cn", "password": "passw0rd1", "verification_code": "000000",
		}, nil)
		if status != http.StatusBadRequest {
			t.Fatalf("第 %d 次错误验证码应 400，实际 %d", i, status)
		}
	}
	// 作废后即使蒙对也拒绝（此处用真码，应已被锁）
	code := verify.MockLastCode("register", "vcmax@t.cn")
	if status, _ := doJSON(t, srv, http.MethodPost, "/auth/register", map[string]interface{}{
		"username": "vcmax", "email": "vcmax@t.cn", "password": "passw0rd1", "verification_code": code,
	}, nil); status != http.StatusBadRequest {
		t.Fatalf("超过失败上限后真码也应被拒，实际 %d", status)
	}
}

func TestVerifyForgotPasswordFlow(t *testing.T) {
	srv := newTestServer(t)
	enableVerifyScene(t, "forgot_password")

	// 造一个用户（注册场景未启用，直接注册）
	if status, env := doJSON(t, srv, http.MethodPost, "/auth/register", map[string]interface{}{
		"username": "vcforgot", "email": "vcforgot@t.cn", "password": "passw0rd1",
	}, nil); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("造用户失败（msg=%s）", env.Msg)
	}

	// 账号不匹配时不消耗验证码：发码 → 错误用户名 + 正确码 → 统一模糊错误
	if status, _ := sendCode(t, srv, "forgot_password", "vcforgot@t.cn"); status != http.StatusOK {
		t.Fatalf("发码失败")
	}
	code := verify.MockLastCode("forgot_password", "vcforgot@t.cn")
	if status, _ := doJSON(t, srv, http.MethodPost, "/auth/forgot-password", map[string]interface{}{
		"username": "nouser", "email": "vcforgot@t.cn", "new_password": "passw0rd2",
		"confirm_password": "passw0rd2", "verification_code": code,
	}, nil); status != http.StatusBadRequest {
		t.Fatalf("账号不匹配应 400，实际 %d", status)
	}
	if countConsumedCodes(t, "forgot_password", "vcforgot@t.cn") != 0 {
		t.Fatal("账号不匹配时验证码不应被消费")
	}

	// 匹配用户 + 正确码 → 重置成功并自动登录，码被消费
	if status, env := doJSON(t, srv, http.MethodPost, "/auth/forgot-password", map[string]interface{}{
		"username": "vcforgot", "email": "vcforgot@t.cn", "new_password": "passw0rd2",
		"confirm_password": "passw0rd2", "verification_code": code,
	}, nil); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("带验证码找回密码失败（status=%d code=%d msg=%s）", status, env.Code, env.Msg)
	}
	if countConsumedCodes(t, "forgot_password", "vcforgot@t.cn") == 0 {
		t.Fatal("重置成功后验证码应已消费")
	}
	// 新密码可登录
	loginUser(t, srv, "vcforgot", "passw0rd2")
}

func TestVerifyHTTPSender(t *testing.T) {
	srv := newTestServer(t)
	enableVerifyScene(t, "register")

	// 通用 http 通道：上游断言占位符替换与成功关键字判定
	var gotTarget, gotCode string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTarget = r.URL.Query().Get("mobile")
		gotCode = r.URL.Query().Get("code")
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer upstream.Close()

	setVerifySetting(t, "verify_provider", "http")
	setVerifySetting(t, "verify_http_url", upstream.URL+"/send?mobile={{target}}&code={{code}}&scene={{scene}}")
	setVerifySetting(t, "verify_http_method", "GET")
	setVerifySetting(t, "verify_http_success_keyword", `"status":"ok"`)

	if status, env := sendCode(t, srv, "register", "httpvc@t.cn"); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("http 通道发码失败（status=%d code=%d msg=%s）", status, env.Code, env.Msg)
	}
	if gotTarget != "httpvc@t.cn" || len(gotCode) != 6 {
		t.Fatalf("http 通道占位符替换异常: target=%q code=%q", gotTarget, gotCode)
	}

	// 成功关键字不匹配 → 发码失败 400
	setVerifySetting(t, "verify_http_success_keyword", `"status":"fail"`)
	if status, _ := sendCode(t, srv, "register", "httpvc2@t.cn"); status != http.StatusBadRequest {
		t.Fatalf("成功关键字不匹配应 400，实际 %d", status)
	}
}
