package groupb

// 带内失败的业务码（待办清单 P122）：站方信封的 code 此前没有落库格，
// 装配仓 S104 的取证跑到第二步就分不开「会话被拒（401/403）」与「配额/风控 429」——
// reason 只说"是上游回的错误"，业务码才是那半格。
// 这里钉三件事：
//  1. 全链路落库映射（ObserveCall 抽取 → RecentCall → 真 persistCallLogs 进 api_call_logs）；
//  2. /admin/monitor/history 的 in_band_code 等值筛选（明细筛选这一半）；
//  3. 同意位关闭时业务码**不抹**（P37 在 P122 上的判：它不是内容维度）——端到端形状，
//     单元那半钉在 test/inband_error_test.go 的 TestWithholdContentKeepsInBandBusinessCode。

import (
	"net/http"
	"testing"
	"time"

	"loomproxy/app"
	"loomproxy/base"
	"loomproxy/db"
	"loomproxy/models"
)

// reason 与 code 都是编的形状值：reason 用 S20 建议枚举里的一员，code 用站方常见档位。
const inBandCodeEnvelope = `{"contentType":"error","data":{"message":"missing itemId or bookId parameter","reason":"upstream_status","code":401}}`

// TestInBandCodeFlowsToCallLog 全链路：真实调用抽到业务码 → 环形缓冲带出 →
// 真 persistCallLogs 落库 → 库里读得回。
// flusher 由 app.Run 接线而夹具只建 CreateApp（淘汰批在测试里不会自己落库），
// 所以落库那半经导出位 app.PersistCallLogs 把 Drain 出来的**同一批**喂进真映射。
func TestInBandCodeFlowsToCallLog(t *testing.T) {
	srv := newTestServer(t)
	base.ResetMetrics()
	admin := adminToken(t, srv)

	setPlatformUpstream(t, "fake_c", staticUpstream(t, inBandCodeEnvelope).URL)
	mustOK(t, srv, admin, "/fake_c/content?bookId=bk1&itemId=it1")

	rows := base.DrainRecentCalls()
	rc, ok := dimensionsOf(rows, "fake_c", "content")
	if !ok {
		t.Fatal("环形缓冲里没有 fake_c/content 的明细")
	}
	if !rc.InBandError || rc.InBandReason != "upstream_status" || rc.InBandCode != "401" {
		t.Fatalf("RecentCall 业务读数不全: err=%v reason=%q code=%q", rc.InBandError, rc.InBandReason, rc.InBandCode)
	}

	// 落库映射：同一批 rows 喂进真 persistCallLogs，断言对象换成库里的行——
	// 映射行（rc.InBandCode → 列）哪天被删，这里先红，而不是等生产上「列在、值从没存」
	app.PersistCallLogs(rows)
	var row models.ApiCallLog
	if err := db.DB.Where("source = ? AND in_band_code = ?", "fake_c", "401").
		First(&row).Error; err != nil {
		t.Fatalf("落库后读不回带业务码的明细行: %v", err)
	}
	if !row.InBandError || row.InBandReason != "upstream_status" {
		t.Errorf("落库行业务读数不全: err=%v reason=%q", row.InBandError, row.InBandReason)
	}
	if row.InBandCode != "401" {
		t.Errorf("落库行 in_band_code = %q, want 401", row.InBandCode)
	}
}

// TestMonitorHistoryInBandCodeFilter 明细筛选（history 只读库，直插明细驱动）：
// 码是枚举档位，等值匹配；查询参数为空 = 不筛（「源没给」的空码行不能用这条筛出来）。
func TestMonitorHistoryInBandCodeFilter(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)

	mk := func(code string) {
		t.Helper()
		row := models.ApiCallLog{
			Username: "ibc_u", IP: "10.6.6.1", Source: "fake_a", Action: "content",
			Status: http.StatusOK, LatencyMs: 8, CreatedAt: time.Now(),
			InBandError: true, InBandCode: code,
		}
		if err := db.DB.Create(&row).Error; err != nil {
			t.Fatalf("写入明细失败: %v", err)
		}
	}
	mk("401")
	mk("429")
	mk("") // 旧行形状：带内失败但源没给码

	if n := historyTotal(t, srv, admin, map[string]string{"in_band_code": "401"}); n != 1 {
		t.Errorf("in_band_code=401 应命中 1 条，实为 %d", n)
	}
	if n := historyTotal(t, srv, admin, map[string]string{"in_band_code": "429"}); n != 1 {
		t.Errorf("in_band_code=429 应命中 1 条，实为 %d", n)
	}
	if n := historyTotal(t, srv, admin, map[string]string{"in_band_code": "500"}); n != 0 {
		t.Errorf("库里没有的档位应命中 0 条，实为 %d", n)
	}
}

// TestPrivacyConsentOffKeepsInBandCode 同意位关闭的端到端形状：内容维度七列全空，
// 而带内失败的业务读数（err/reason/code）原样留着——
// 否则退出用户的那批 200 请求在明细里连「站方回了哪一档」都答不出来（P122 对 P37 的判）。
func TestPrivacyConsentOffKeepsInBandCode(t *testing.T) {
	srv := newTestServer(t)
	base.ResetMetrics()
	base.ResetNameCaches()

	token, _ := privacyUser(t, srv)
	setPlatformUpstream(t, "fake_c", staticUpstream(t, inBandCodeEnvelope).URL)
	setPrivacyConsent(t, srv, token, false)

	mustOK(t, srv, token, "/fake_c/content?bookId=bk1&itemId=it1")

	rc, ok := dimensionsOf(base.DrainRecentCalls(), "fake_c", "content")
	if !ok {
		t.Fatal("关闭后 fake_c/content 的明细整条没了——计数口径不能被一起抹掉")
	}
	claimDims(t, rc, "关闭后的带内失败")
	if !rc.ContentWithheld {
		t.Error("没打 content_withheld：退出用户的「全空」与「采集在漏」就分不开了")
	}
	if !rc.InBandError || rc.InBandReason != "upstream_status" || rc.InBandCode != "401" {
		t.Errorf("同意位关闭把带内业务读数一起抹了: err=%v reason=%q code=%q（业务码不是内容维度，P122 判）",
			rc.InBandError, rc.InBandReason, rc.InBandCode)
	}
}
