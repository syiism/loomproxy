package test

// 用户自助 API 密钥集成测试：创建（明文仅一次）→ 密钥调网关（身份归属 + 计费落账）→
// 列表掩码 → 撤销失效 → 静态 env 键行为不变。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"loomproxy-go/conf"
	"loomproxy-go/db"
	"loomproxy-go/models"
)

func TestApiKeyLifecycle(t *testing.T) {
	srv := newTestServer(t)
	token := registerUser(t, srv, "ak_u1", "ak_u1@example.com", "pass1234")

	// 假上游（fake_a 经平台默认 baseUrl 注入，{} 归一化为空书单 200）
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(upstream.Close)
	setAUpstream(t, upstream.URL)

	// 1. 创建：明文仅返回一次
	status, env := doJSON(t, srv, http.MethodPost, "/apikey", map[string]string{"name": "ci"}, authHeader(token))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("创建密钥 status = %d code = %d（msg=%s）", status, env.Code, env.Msg)
	}
	data := env.dataMap(t)
	plain, _ := data["key"].(string)
	if !strings.HasPrefix(plain, "lp_") || len(plain) < 40 {
		t.Fatalf("密钥格式异常: %q", plain)
	}
	keyID := uint(0)
	if v, ok := data["id"].(float64); ok {
		keyID = uint(v)
	}

	// 2. 密钥调网关：应通过鉴权并归属用户（计费落账）
	status, raw := doRaw(t, srv, http.MethodGet, "/fake_a/search"+buildQuery(map[string]string{
		"query": "测试",
	}), nil, map[string]string{"X-API-Key": plain})
	if status != http.StatusOK {
		t.Fatalf("密钥调网关 status = %d, want 200（body=%s）", status, truncate(string(raw), 200))
	}
	uid := userIDByName(t, "ak_u1")
	var usage int64
	if err := db.DB.Model(&models.QuotaUsageLog{}).Where("user_id = ?", uid).Count(&usage).Error; err != nil {
		t.Fatalf("计数查询失败: %v", err)
	}
	if usage != 1 {
		t.Fatalf("密钥调用未按归属用户计费：usage_logs = %d, want 1", usage)
	}

	// 3. 列表：明文可随时查回
	status, env = doJSON(t, srv, http.MethodGet, "/apikey", nil, authHeader(token))
	if status != http.StatusOK {
		t.Fatalf("列表 status = %d", status)
	}
	if rawEnv, err := json.Marshal(env); err != nil || !strings.Contains(string(rawEnv), plain) {
		t.Fatal("列表未返回完整密钥（明文可查回语义破坏）")
	}

	// 4. 静态 env 键行为不变（匿名、可用）
	old := conf.Config.APIKeys
	conf.Config.APIKeys = []string{"sk_static_test"}
	t.Cleanup(func() { conf.Config.APIKeys = old })
	status, raw = doRaw(t, srv, http.MethodGet, "/fake_a/search"+buildQuery(map[string]string{
		"query": "测试",
	}), nil, map[string]string{"X-API-Key": "sk_static_test"})
	if status != http.StatusOK {
		t.Fatalf("静态 env 键失效: status = %d（body=%s）", status, truncate(string(raw), 200))
	}

	// 5. 他人不可撤销
	token2 := registerUser(t, srv, "ak_u2", "ak_u2@example.com", "pass1234")
	status, _ = doJSON(t, srv, http.MethodDelete, "/apikey/"+fmt.Sprintf("%d", int64(keyID)), nil, authHeader(token2))
	if status != http.StatusNotFound {
		t.Fatalf("他人撤销应 404, got %d", status)
	}

	// 6. 撤销后密钥立即失效
	status, _ = doJSON(t, srv, http.MethodDelete, "/apikey/"+fmt.Sprintf("%d", int64(keyID)), nil, authHeader(token))
	if status != http.StatusOK {
		t.Fatalf("撤销 status = %d", status)
	}
	status, raw = doRaw(t, srv, http.MethodGet, "/fake_a/search"+buildQuery(map[string]string{
		"query": "测试",
	}), nil, map[string]string{"X-API-Key": plain})
	if status != http.StatusUnauthorized {
		t.Fatalf("撤销后仍可用: status = %d, want 401（body=%s）", status, truncate(string(raw), 200))
	}
}
