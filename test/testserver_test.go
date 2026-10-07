package test

// 集成测试脚手架的**转发层**：实现已下沉到 `loomproxy/testkit`（待办清单 P104 第一步）。
//
// 为什么留这一页而不是把 98 个文件全改一遍：老用例里的 `newTestServer(t)` /
// `doJSON(...)` / `adminToken(...)` 是同包可见的小写名，一次改名要动 86 个文件；
// 而拆包要的只是「第二个包也能拿到夹具」，那件事由 testkit 满足，不需要动存量。
// 新切的子包**也留一份同名转发**（`test/groupb/forward_test.go`）——这一句是 2026-10-07 正式拆分时改的口：
// 原话写的是"新包直接用 testkit 的导出名，不要再造一层同包转发，那等于把耦合又请回来"，
// 而照它做要在 B 侧改 713 处调用点、A 侧再改 268 处，换来的只是少 60 个三行转发函数；
// **更要紧的是那 713 处按名字的批量改写本身会造出事**（`dashboard` → `Dashboard` 同时改到了
// `"/quota/dashboard"` 这个 URL 字符串，红两条用例，见判据页《按符号名做批量改名》与报告 §15.1）。
// 耦合的正本仍然只有一处（`testkit`），两组的转发都只是名字——**新写用例请直接调 testkit 的导出名**。
//
// 每个转发都调 `t.Helper()`：调它的话，夹具里的 t.Fatalf 会报在这一页而不是报在用例行，
// 用例失败时第一眼看到的位置就错了。

import (
	"net/http/httptest"
	"testing"

	"loomproxy/testkit"
)

const (
	testAdminUser = testkit.AdminUser
	testAdminPass = testkit.AdminPass
	testJWTSecret = testkit.JWTSecret
)

// apiEnvelope 是本包内的信封包装：字段由内嵌提升（Code/Msg/Data 照原样可访问），
// 小写方法 dataMap 只能定义在这个包里的类型上，所以用包装而不是类型别名。
type apiEnvelope struct {
	testkit.Envelope
}

func (e apiEnvelope) dataMap(t *testing.T) map[string]interface{} {
	t.Helper()
	return e.Envelope.DataMap(t)
}

func setupTestConf(t *testing.T, dataDir string) {
	t.Helper()
	testkit.SetupConf(t, dataDir)
}

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	return testkit.NewServer(t)
}

func doRaw(t *testing.T, srv *httptest.Server, method, path string, body interface{}, headers map[string]string) (int, []byte) {
	t.Helper()
	return testkit.DoRaw(t, srv, method, path, body, headers)
}

func doJSON(t *testing.T, srv *httptest.Server, method, path string, body interface{}, headers map[string]string) (int, apiEnvelope) {
	t.Helper()
	status, env := testkit.DoJSON(t, srv, method, path, body, headers)
	return status, apiEnvelope{env}
}

func authHeader(token string) map[string]string {
	return testkit.AuthHeader(token)
}

func buildQuery(kv map[string]string) string {
	return testkit.BuildQuery(kv)
}

func registerUser(t *testing.T, srv *httptest.Server, username, email, password string) string {
	t.Helper()
	return testkit.RegisterUser(t, srv, username, email, password)
}

func loginUser(t *testing.T, srv *httptest.Server, identity, password string) string {
	t.Helper()
	return testkit.LoginUser(t, srv, identity, password)
}

func adminToken(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	return testkit.AdminToken(t, srv)
}

func userIDByName(t *testing.T, username string) uint {
	t.Helper()
	return testkit.UserIDByName(t, username)
}

func itoa(v uint) string {
	return testkit.Itoa(v)
}

func truncate(s string, n int) string {
	return testkit.Truncate(s, n)
}
