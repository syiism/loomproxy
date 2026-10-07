package test

// 组 A 的跨组转发层（待办清单 P104 正式拆分）。
//
// 这一页只装一类东西：**实现原本长在搬到组 B 的那些文件里、而组 A 的用例还在调它**的 14 条夹具
// （12 函数 + 2 类型别名 + 2 常量别名，正本全在 `loomproxy/testkit/helpers2.go`）。
// 与 `test/groupb/forward_test.go` 是同一份形状的两处实例——两组的小写名都只是为了不动存量用例。

import (
	"loomproxy/handlers/subjectrank"
	"loomproxy/models"
	"loomproxy/testkit"
	"net/http/httptest"
	"testing"
)

func setPlatformUpstream(t *testing.T, source, upstreamURL string) {
	t.Helper()
	testkit.SetPlatformUpstream(t, source, upstreamURL)
}

func staticUpstream(t *testing.T, payload string) *httptest.Server {
	t.Helper()
	return testkit.StaticUpstream(t, payload)
}

func insertSubjectCall(t *testing.T, source, action, keyword, book, media string, resultCount, status int) {
	t.Helper()
	testkit.InsertSubjectCall(t, source, action, keyword, book, media, resultCount, status)
}

func mustOK(t *testing.T, srv *httptest.Server, token, path string) {
	t.Helper()
	testkit.MustOK(t, srv, token, path)
}

func coverageRow(t *testing.T, rows []subjectrank.CoverageRow, source, action string) subjectrank.CoverageRow {
	t.Helper()
	return testkit.CoverageRow(t, rows, source, action)
}

func insertIdentRow(t *testing.T, source, bookIdent, bookName, chapterIdent, chapterTitle string) uint {
	t.Helper()
	return testkit.InsertIdentRow(t, source, bookIdent, bookName, chapterIdent, chapterTitle)
}

func setVerifySetting(t *testing.T, key, value string) {
	t.Helper()
	testkit.SetVerifySetting(t, key, value)
}

func enableVerifyScene(t *testing.T, scene string) {
	t.Helper()
	testkit.EnableVerifyScene(t, scene)
}

func sendCode(t *testing.T, srv *httptest.Server, scene, target string) (int, apiEnvelope) {
	t.Helper()
	status, env := testkit.SendCode(t, srv, scene, target)
	return status, apiEnvelope{env} // 信封包装在本包各有一份，见 forward 页
}

func loginFromIP(t *testing.T, srv *httptest.Server, username, password, ip string) (int, apiEnvelope) {
	t.Helper()
	status, env := testkit.LoginFromIP(t, srv, username, password, ip)
	return status, apiEnvelope{env} // 信封包装在本包各有一份，见 forward 页
}

func grantRow(t *testing.T, planID uint) models.QuotaLimit {
	t.Helper()
	return testkit.GrantRow(t, planID)
}

func dashboard(t *testing.T, srv *httptest.Server, token string) dashResp {
	t.Helper()
	return testkit.Dashboard(t, srv, token)
}

type dashResp = testkit.DashResp
type dashSource = testkit.DashSource

const searchEnvelope = testkit.SearchEnvelope
const detailEnvelope = testkit.DetailEnvelope

func settingValue(t *testing.T, key string) (value, typ string) {
	t.Helper()
	return testkit.SettingValue(t, key)
}
