package groupb

// 组 B 的转发层（待办清单 P104 正式拆分）。**正本只有一处：`loomproxy/testkit`**，
// 这一页存在的唯一理由是让 `test/` 搬过来的 44 个存量文件**一个调用点都不用改**——
// 它们写的是 `newTestServer(t)` / `doJSON(...)` / `planIDByCode(...)` 这类同包小写名。
// 新写用例请直接调 testkit 的导出名，不要往这里再加。
//
// 与 `test/testserver_test.go` 是同一份形状的两处实例：跨包要的就这一点样板。
// 每个转发都调 `t.Helper()`：不然夹具里的 t.Fatalf 会报在这一页而不是报在用例行。

import (
	"loomproxy/testkit"
	"net/http/httptest"
	"testing"
	"time"
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

// —— 批次一：实现长在组 A 的文件里、而这里也用到（正本在 testkit）——

func planIDByCode(t *testing.T, code string) uint {
	t.Helper()
	return testkit.PlanIDByCode(t, code)
}
func threeFormUser(t *testing.T, srv *httptest.Server) (string, string) {
	t.Helper()
	return testkit.ThreeFormUser(t, srv)
}
func adminDataSourceID(t *testing.T, srv *httptest.Server, token, name string) int {
	t.Helper()
	return testkit.AdminDataSourceID(t, srv, token, name)
}
func dataSourceIDByName(t *testing.T, name string) uint {
	t.Helper()
	return testkit.DataSourceIDByName(t, name)
}
func datasourceNames(t *testing.T, srv *httptest.Server) map[string]bool {
	t.Helper()
	return testkit.DatasourceNames(t, srv)
}
func dbGetSetting(t *testing.T, key string) string {
	t.Helper()
	return testkit.DBGetSetting(t, key)
}
func delCostCache(sourceCode, action string) {
	testkit.DelCostCache(sourceCode, action)
}
func insertCallLog(t *testing.T, source, action string, status int, createdAt time.Time) {
	t.Helper()
	testkit.InsertCallLog(t, source, action, status, createdAt)
}
func listUserIDs(t *testing.T, srv *httptest.Server, admin, query string) map[uint]bool {
	t.Helper()
	return testkit.ListUserIDs(t, srv, admin, query)
}
func setAUpstream(t *testing.T, upstreamURL string) {
	t.Helper()
	testkit.SetAUpstream(t, upstreamURL)
}
func writeDict(t *testing.T, root, dir, name, body string) {
	t.Helper()
	testkit.WriteDict(t, root, dir, name, body)
}
func writeSettingValue(t *testing.T, key, value string) {
	t.Helper()
	testkit.WriteSettingValue(t, key, value)
}
