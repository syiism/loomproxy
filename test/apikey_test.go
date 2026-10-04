package test

// 用户自助 API 密钥集成测试：创建（明文仅一次）→ 密钥调网关（身份归属 + 计费落账）→
// 列表掩码 → 撤销失效 → 静态 env 键行为不变。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"loomproxy/conf"
	"loomproxy/db"
	"loomproxy/models"
	"loomproxy/utils"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
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
	// 显式配 search 单价：默认口径已是「只有 content 计费」，这条要验的是归属计费而不是默认值
	setSearchCost(t, 1)
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
	// 撤销后**带了**这个密钥，所以答案是「无效的 API Key」(403) 而不是「缺少鉴权凭证」(401)——
	// 两者混为一谈会让调用方以为自己没传参
	if status != http.StatusForbidden {
		t.Fatalf("撤销后仍可用: status = %d, want 403（body=%s）", status, truncate(string(raw), 200))
	}
	if !strings.Contains(string(raw), "无效的 API Key") {
		t.Errorf("撤销后的提示应说明密钥无效，实得 %s", truncate(string(raw), 200))
	}
}

// TestApiKeyLookupQuotesReservedColumn api_keys.key 是 MySQL 保留字：条件写成裸串
// Where("key = ?", plain) 会被 GORM 原样下发 → MySQL 报 Error 1064 → 所有 API Key 都被判成
// 「不存在」。SQLite 容忍裸写，所以用例全绿、只有生产暴露（AGENTS §10）。
//
// 这里不连 MySQL（测试环境没有），而是把真实查询路径挂到 DryRun 会话上，
// 断言**生成出来的 SQL 给列名加了引号**——GORM 在两种方言下都用反引号，
// 所以这个形状断言对 MySQL 同样成立，且改回裸写就会红。
func TestApiKeyLookupQuotesReservedColumn(t *testing.T) {
	newTestServer(t)

	var captured strings.Builder
	prev := db.DB
	db.DB = prev.Session(&gorm.Session{DryRun: true, Logger: &sqlSpy{sb: &captured}})
	t.Cleanup(func() { db.DB = prev })

	_, _ = utils.LookupApiKeyIdentity("lp_not_a_real_key_000000000000000000000000000000")
	sql := captured.String()
	if !strings.Contains(sql, "api_keys") {
		t.Fatalf("未捕获到 api_keys 查询，实得：%q", sql)
	}
	if !strings.Contains(sql, "`key`") { // dialect-allow：断言 GORM 按当前方言加的引号，不是手写 SQL
		t.Errorf("key 列没有加引号，MySQL 会当保留字直接 1064：%s", sql)
	}
}

// sqlSpy 只把 Trace 收到的 SQL 记进 buffer，用于断言生成的语句形态
type sqlSpy struct{ sb *strings.Builder }

func (s *sqlSpy) LogMode(logger.LogLevel) logger.Interface      { return s }
func (s *sqlSpy) Info(context.Context, string, ...interface{})  {}
func (s *sqlSpy) Warn(context.Context, string, ...interface{})  {}
func (s *sqlSpy) Error(context.Context, string, ...interface{}) {}
func (s *sqlSpy) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	if s.sb != nil {
		sql, _ := fc()
		s.sb.WriteString(sql)
		s.sb.WriteString("\n")
	}
}
