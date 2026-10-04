package test

// 集成测试脚手架：真实 gin 引擎（app.CreateApp）+ 临时 SQLite 库 + httptest。
//
// 设计要点：
//   - 每个用例 newTestServer 得到一个全新临时库（t.TempDir），db.Init 自动
//     AutoMigrate + seed（角色/设置/套餐/数据源/套餐-数据源关联/QuotaCost），
//     并按 conf 的 AdminUsername/AdminPassword 创建 admin 用户，无需手工准备数据。
//   - 不走 conf.Load()/app.Run()：避免读取 .env、起监听、起号池与代理协程。
//     conf.Config 为全局指针，直接赋值（与 resilience_test.go 的 setupConf 同模式）。
//   - 全局态（conf.Config、db.DB、utils.DefaultCache、限流器）跨用例共享，
//     因此集成用例一律不 t.Parallel()；直接改库后需清对应缓存（见 delCostCache）。

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"

	"loomproxy/app"
	"loomproxy/conf"
	"loomproxy/db"
	"loomproxy/handlers/auth"
	"loomproxy/utils"
)

const (
	testAdminUser = "admin"
	testAdminPass = "admin1234"
	testJWTSecret = "test-jwt-secret"
)

// apiEnvelope 统一响应信封 {"code","msg","data"}；data 可能缺省（错误时）
type apiEnvelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// setupTestConf 构造测试用全局 conf（dataDir 为 SQLite 所在目录）。
// AuthEnabled=true（网关注鉴权生效），admin/admin1234 将由 seed 创建。
func setupTestConf(t *testing.T, dataDir string) {
	t.Helper()

	conf.Config = &conf.ConfMgr{
		TimeoutConnect: 5,
		TimeoutPool:    10,

		CacheTTL:     300,
		CacheMaxSize: 128,

		UpstreamCacheTTL:     0, // 集成测试默认关闭上游缓存，避免用例间串扰
		UpstreamCacheMaxSize: 512,

		CircuitBreakerEnabled:  true,
		CircuitBreakerFailures: 5,
		CircuitBreakerCooldown: 30,

		ServerHost:     "127.0.0.1",
		ServerLogLevel: "release",

		TZOffsetHours: 8,
		ErrorCode:     -1,

		DataDir:      dataDir,
		DataFileGlob: "*.json",

		AuthEnabled: true,
		AuthWhitelist: []string{
			"/", "/datasources", "/data", "/auth/register", "/auth/login", "/panel",
		},

		DBType: "sqlite",
		DBName: "test",

		JWTSecret:      testJWTSecret,
		JWTExpireHours: 168,

		AdminUsername: testAdminUser,
		AdminPassword: testAdminPass,
	}
}

// newTestServer 构造全新测试服务：临时目录 SQLite + 完整 gin 引擎
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()

	setupTestConf(t, t.TempDir())

	gin.SetMode(gin.TestMode)
	// 全局缓存单例跨用例共享（sync.Once），而数据库每用例全新——
	// 必须清空缓存避免上一用例的 quota:cost 等条目污染本用例
	if conf.Config.CacheMaxSize > 0 {
		utils.DefaultCache().Clear()
	}
	// db.GetSetting 另有一层进程内的 sync.Map（10s TTL），它跟着旧库的值残留：
	// 不清的话，本用例读到的设置是上一个用例写进那个库的（比如榜单放行名单），
	// 表现为「新库默认值读出来是非默认值」这种查不出原因的失败
	db.InvalidateSettingCache("")
	// 登录/找回密码的按 IP 限频器同为进程级状态（全部用例共享 127.0.0.1），
	// 逐用例清零，避免累计尝试次数触发锁定导致无关用例失败
	auth.ResetAttemptLimitersForTest()
	// 账号侧的登录尝试观察（只读）也是进程级状态，同样逐用例清零
	auth.ResetLoginAccountWatchForTest()
	srv := httptest.NewServer(app.CreateApp())
	t.Cleanup(func() {
		srv.Close()
		if db.DB != nil {
			if sqlDB, err := db.DB.DB(); err == nil {
				sqlDB.Close()
			}
		}
	})
	return srv
}

// doRaw 发送请求（body 为 nil 时不带请求体），返回状态码与原始响应体
func doRaw(t *testing.T, srv *httptest.Server, method, path string, body interface{}, headers map[string]string) (int, []byte) {
	t.Helper()

	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequest(method, srv.URL+path, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return resp.StatusCode, raw
}

// doJSON 发送请求并解析统一响应信封；响应非信封格式（如数据源裸 JSON）时 Fatal
func doJSON(t *testing.T, srv *httptest.Server, method, path string, body interface{}, headers map[string]string) (int, apiEnvelope) {
	t.Helper()
	status, raw := doRaw(t, srv, method, path, body, headers)
	var env apiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("%s %s: 响应非 JSON 信封（status=%d）: %s", method, path, status, truncate(string(raw), 200))
	}
	return status, env
}

// dataMap 解析信封 data 字段为 map
func (e apiEnvelope) dataMap(t *testing.T) map[string]interface{} {
	t.Helper()
	if len(e.Data) == 0 {
		t.Fatalf("响应无 data 字段（code=%d msg=%s）", e.Code, e.Msg)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(e.Data, &m); err != nil {
		t.Fatalf("解析 data 失败: %v", err)
	}
	return m
}

func authHeader(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

// buildQuery 拼接 URL query
func buildQuery(kv map[string]string) string {
	vals := url.Values{}
	for k, v := range kv {
		vals.Set(k, v)
	}
	return "?" + vals.Encode()
}

// registerUser 注册普通用户并返回其 JWT（注册成功即签发 token）
func registerUser(t *testing.T, srv *httptest.Server, username, email, password string) string {
	t.Helper()
	status, env := doJSON(t, srv, http.MethodPost, "/auth/register", map[string]interface{}{
		"username": username,
		"email":    email,
		"password": password,
	}, nil)
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("注册 %s 失败（status=%d code=%d msg=%s）", username, status, env.Code, env.Msg)
	}
	token, _ := env.dataMap(t)["token"].(string)
	if token == "" {
		t.Fatalf("注册 %s 响应缺少 token", username)
	}
	return token
}

// loginUser 登录（identity 可为用户名或邮箱）并返回 JWT
func loginUser(t *testing.T, srv *httptest.Server, identity, password string) string {
	t.Helper()
	status, env := doJSON(t, srv, http.MethodPost, "/auth/login", map[string]interface{}{
		"username": identity,
		"password": password,
	}, nil)
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("登录 %s 失败（status=%d code=%d msg=%s）", identity, status, env.Code, env.Msg)
	}
	token, _ := env.dataMap(t)["token"].(string)
	if token == "" {
		t.Fatalf("登录 %s 响应缺少 token", identity)
	}
	return token
}

// adminToken 返回 seed 创建的 admin 用户 JWT
func adminToken(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	return loginUser(t, srv, testAdminUser, testAdminPass)
}

// userIDByName 直接查库取用户 ID（测试断言辅助）
func userIDByName(t *testing.T, username string) uint {
	t.Helper()
	var id uint
	if err := db.DB.Table("users").Select("id").Where("username = ?", username).Scan(&id).Error; err != nil {
		t.Fatalf("查询用户 %s ID 失败: %v", username, err)
	}
	if id == 0 {
		t.Fatalf("用户 %s 不存在", username)
	}
	return id
}

func itoa(v uint) string {
	return strconv.FormatUint(uint64(v), 10)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
