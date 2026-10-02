package test

// 带内失败的观测（待办清单 P22①）：源在参数缺失/上游失败时返回 ContentType=="error"
// 的正文、HTTP 仍是 200——handler 不报错，监控按成功收口，成功率/覆盖率/榜三处读数一起失真。
// 这里钉四层：ObserveCall 识别、RecordCall 计失败、HTTP 全链路落明细、覆盖率表能数到它。
// 计费与 IP 封禁不受带内失败影响（它们看的是 HTTP 与请求本身，产品口径不在本轮）。

import (
	"net/http"
	"testing"
	"time"

	"loomproxy/base"
	"loomproxy/base/legado"
	"loomproxy/db"
	"loomproxy/handlers/subjectrank"
	"loomproxy/models"
)

func TestObserveCallMarksInBandError(t *testing.T) {
	// 规范 DTO 路径：错误正文标带内失败，且 "error" 不是合法媒介、媒介照旧留空
	s := observeOne(t, "fake_a", map[string]interface{}{"itemId": "i1", "bookId": "b1"},
		legado.ContentResponse{ContentType: "error", Data: map[string]interface{}{"message": "missing"}})
	if !s.InBandError {
		t.Error("ContentType==error 的正文应标 InBandError")
	}
	if s.Media != "" {
		t.Errorf("带内错误的媒介应留空（未判定），实为 %q", s.Media)
	}
	if s.BookName != "" {
		t.Errorf("带内错误不该抽到书名，实为 %q", s.BookName)
	}

	// 源自己拼 map（松散信封）的路径要走到同一处标记
	s = observeOne(t, "fake_a", nil, map[string]interface{}{
		"contentType": "error",
		"data":        map[string]interface{}{"message": "missing itemId or bookId parameter"},
	})
	if !s.InBandError {
		t.Error("松散 map 的 contentType==error 也应标 InBandError")
	}

	// 正常正文不受影响
	s = observeOne(t, "fake_a", map[string]interface{}{"itemId": "i1"},
		legado.ContentResponse{ContentType: "text", Data: map[string]interface{}{"content": "正文"}})
	if s.InBandError {
		t.Error("正常正文不该标 InBandError")
	}
}

func TestRecordCallCountsInBandErrorAsFailed(t *testing.T) {
	base.ResetMetrics()
	t.Cleanup(base.ResetMetrics)

	// HTTP 200 但带内失败：监控读数必须计入失败，RecentCall 带出标记
	base.RecordCall("fake_a", "content", "u", "10.9.9.1", 200, time.Millisecond,
		&base.CallSubject{InBandError: true})
	// 对照：HTTP 200 的正常调用、与真正的 4xx，口径不变
	base.RecordCall("fake_a", "content", "u", "10.9.9.1", 200, time.Millisecond,
		&base.CallSubject{})
	base.RecordCall("fake_a", "content", "u", "10.9.9.1", 500, time.Millisecond, nil)

	_, snap := base.MetricsSnapshot()
	m := snap["fake_a"]["content"]
	if m.Total != 3 || m.Success != 1 || m.Failed != 2 {
		t.Fatalf("fake_a/content = total %d success %d failed %d, want 3/1/2（带内失败算失败）",
			m.Total, m.Success, m.Failed)
	}
	var marked bool
	for _, rc := range base.RecentCalls(3) { // 最新在前，三条都在
		if rc.InBandError {
			marked = true
		}
	}
	if !marked {
		t.Error("RecentCall 应带出 in_band_error 标记")
	}
}

// TestInBandErrorEndToEnd 全链路：源返回带内错误的正文 → HTTP 200 → 明细带标记、
// 会话聚合计入失败。上游用 staticUpstream 的假 JSON（contentType:error），
// 挂到平台默认 baseUrl（可信来源，豁免 SSRF）。
func TestInBandErrorEndToEnd(t *testing.T) {
	srv := newTestServer(t)
	base.ResetMetrics()
	base.DrainRecentCalls()

	up := staticUpstream(t, `{"contentType":"error","data":{"message":"missing itemId or bookId parameter"}}`)
	setPlatformUpstream(t, "fake_b", up.URL)

	admin := authHeader(adminToken(t, srv))
	status, env := doJSON(t, srv, http.MethodGet, "/fake_b/content?bookId=b1&itemId=i1", nil, admin)
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("带内错误应仍以 200 出下游（status=%d code=%d msg=%s）——P22② 改口径是另一回事", status, env.Code, env.Msg)
	}

	// 会话聚合：这次调用要计成失败
	_, snap := base.MetricsSnapshot()
	m := snap["fake_b"]["content"]
	if m.Total != 1 || m.Failed != 1 {
		t.Fatalf("fake_b/content = total %d failed %d, want 1/1", m.Total, m.Failed)
	}

	// 最近调用明细带标记（面板据此显示「带内失败」）
	for _, rc := range base.RecentCalls(10) {
		if rc.Source == "fake_b" && rc.Action == "content" {
			if !rc.InBandError {
				t.Fatal("全链路后 RecentCall 丢了 in_band_error 标记")
			}
			return
		}
	}
	t.Fatal("环形缓冲里没有 fake_b/content 的明细")
}

// TestCoverageCountsInBandError 覆盖率表要能数到带内失败：一格不满到底是因为
// 「请求在失败」还是「采集在漏」，靠 failed/in_band_failed 两列区分（P21 的直接受益者）。
func TestCoverageCountsInBandError(t *testing.T) {
	newTestServer(t)
	base.DrainRecentCalls()

	mk := func(book string, inBand bool, status int) {
		t.Helper()
		row := models.ApiCallLog{
			Username: "ib_u", IP: "10.5.5.1", Source: "fake_a", Action: "content",
			Status: status, LatencyMs: 8, CreatedAt: time.Now(),
			Media: base.MediaNovel, ResultCount: 1,
		}
		if inBand {
			row.InBandError = true
		} else {
			row.BookName = book
		}
		if err := db.DB.Create(&row).Error; err != nil {
			t.Fatalf("写入明细失败: %v", err)
		}
	}
	// 4 行正常（带书名）+ 2 行带内失败（200、全维度空）+ 1 行真 500，凑过 5 行门槛
	mk("带内覆盖率的书", false, http.StatusOK)
	mk("带内覆盖率的书", false, http.StatusOK)
	mk("带内覆盖率的书", false, http.StatusOK)
	mk("带内覆盖率的书", false, http.StatusOK)
	mk("", true, http.StatusOK)
	mk("", true, http.StatusOK)
	mk("", false, http.StatusInternalServerError)

	rows, err := subjectrank.Coverage(7, "", nil)
	if err != nil {
		t.Fatalf("覆盖率统计失败: %v", err)
	}
	content := coverageRow(t, rows, "fake_a", "content")
	if content.Rows != 7 {
		t.Errorf("content rows = %d, want 7", content.Rows)
	}
	if content.Failed != 3 || content.InBandFailed != 2 {
		t.Errorf("content failed=%d in_band_failed=%d, want 3/2（failed 含带内失败，且单列出带内的那部分）",
			content.Failed, content.InBandFailed)
	}
	if content.HasBookName != 4 {
		t.Errorf("content has_book_name = %d, want 4", content.HasBookName)
	}

	// 维度榜同口径：带书名但带内失败的行（构造形态，真实来源不会带名）计入请求数、不计入成功
	for i := 0; i < 3; i++ {
		db.DB.Create(&models.ApiCallLog{
			Username: "ib_u", IP: "10.5.5.1", Source: "fake_a", Action: "content",
			Status: 200, LatencyMs: 8, CreatedAt: time.Now(),
			BookName: "带内也算请求数的书", Media: base.MediaNovel, ResultCount: 1,
		})
	}
	db.DB.Create(&models.ApiCallLog{
		Username: "ib_u", IP: "10.5.5.1", Source: "fake_a", Action: "content",
		Status: 200, LatencyMs: 8, CreatedAt: time.Now(),
		BookName: "带内也算请求数的书", Media: base.MediaNovel, ResultCount: 1,
		InBandError: true,
	})
	items, err := subjectrank.Query("book", 7, "fake_a", "", 0, nil)
	if err != nil {
		t.Fatalf("书名榜查询失败: %v", err)
	}
	it := findItem(t, items, "带内也算请求数的书")
	if it.Requests != 4 || it.Success != 3 || it.Failed != 1 {
		t.Errorf("书名榜 requests=%d success=%d failed=%d, want 4/3/1（IS NOT TRUE 判据钉住带内失败）",
			it.Requests, it.Success, it.Failed)
	}
}
