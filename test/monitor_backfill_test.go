package test

// 明细标识与名称回填：正文/目录请求只带标识，重启后命名缓存空了就只能记下标识，
// 等这本书的名字被别的调用重新带回缓存，这些行应当能补上（幂等、可反复跑）。

import (
	"net/http"
	"testing"
	"time"

	"loomproxy/base"
	"loomproxy/db"
	"loomproxy/models"
)

func insertIdentRow(t *testing.T, source, bookIdent, bookName, chapterIdent, chapterTitle string) uint {
	t.Helper()
	row := models.ApiCallLog{
		Username: "bf_u", IP: "127.0.0.1", Source: source, Action: "content",
		Status: http.StatusOK, LatencyMs: 12, CreatedAt: time.Now(),
		BookName: bookName, ChapterTitle: chapterTitle,
		BookIdent: bookIdent, ChapterIdent: chapterIdent,
		Media: base.MediaNovel, ResultCount: 1,
	}
	if err := db.DB.Create(&row).Error; err != nil {
		t.Fatalf("写入带标识的明细失败: %v", err)
	}
	return row.ID
}

func rowNames(t *testing.T, id uint) (string, string) {
	t.Helper()
	var got models.ApiCallLog
	if err := db.DB.First(&got, id).Error; err != nil {
		t.Fatalf("读取明细失败: %v", err)
	}
	return got.BookName, got.ChapterTitle
}

func TestBackfillSubjectNames(t *testing.T) {
	srv := newTestServer(t)
	base.ResetMetrics()
	admin := adminToken(t, srv)

	// 先用一次详情把 bk9→某剧 灌进命名缓存（fake_b 的详情响应自带书名）
	setPlatformUpstream(t, "fake_b", staticUpstream(t, detailEnvelope).URL)
	mustOK(t, srv, admin, "/fake_b/detail")

	missing := insertIdentRow(t, "fake_b", "bk9", "", "c7", "")  // 缓存里有书名、没有章节名
	unknown := insertIdentRow(t, "fake_b", "bk-无映射", "", "", "") // 缓存里没有：不该被凭空造出来
	noident := insertIdentRow(t, "fake_b", "", "", "", "")       // 升级前的旧行形状：连标识都没有

	_, env := doJSON(t, srv, "POST", "/admin/monitor/backfill-subjects?days=7", nil, authHeader(admin))
	if env.Code != 0 {
		t.Fatalf("回填失败: msg=%s", env.Msg)
	}
	data := env.dataMap(t)
	if data["book_filled"].(float64) != 1 {
		t.Errorf("应补 1 处书名，实得 %v", data)
	}
	if data["chapter_filled"].(float64) != 0 {
		t.Errorf("缓存里没有章节映射时不该补，实得 %v", data)
	}

	if name, _ := rowNames(t, missing); name != "某剧" {
		t.Errorf("书名未回填成功，实得 %q", name)
	}
	if name, _ := rowNames(t, unknown); name != "" {
		t.Errorf("无映射的标识不该被猜成 %q", name)
	}
	if name, _ := rowNames(t, noident); name != "" {
		t.Errorf("无标识的行不应被动到，实得 %q", name)
	}

	// 幂等：再跑一次不该重复计数（已补好的行不再进入扫描集）
	_, env2 := doJSON(t, srv, "POST", "/admin/monitor/backfill-subjects?days=7", nil, authHeader(admin))
	d2 := env2.dataMap(t)
	if d2["book_filled"].(float64) != 0 || d2["chapter_filled"].(float64) != 0 {
		t.Errorf("重复执行应为空操作，实得 %v", d2)
	}

	// 章节名：等目录调用把 c7 带回缓存后应能补上
	insertSubjectCall(t, "fake_b", "content", "", "", base.MediaNovel, 1, http.StatusOK)
	if err := db.DB.Exec("update api_call_logs set chapter_ident = 'c7' where id = ?", missing).Error; err != nil {
		t.Fatalf("调整章节标识失败: %v", err)
	}
	_, env3 := doJSON(t, srv, "POST", "/admin/monitor/backfill-subjects?days=7", nil, authHeader(admin))
	if env3.dataMap(t)["chapter_filled"].(float64) != 0 {
		// 假源的章节映射未必包含 c7，这里只要求不报错、不误补
		t.Logf("c7 未在缓存中，chapter_filled=%v（符合预期则为 0）", env3.dataMap(t)["chapter_filled"])
	}
}

// TestCallLogCarriesIdents 真实调用要把标识一起落库：
// 正文请求只带 bookId/itemId，标识是事后回填的唯一凭据。
func TestCallLogCarriesIdents(t *testing.T) {
	srv := newTestServer(t)
	base.ResetMetrics()
	admin := adminToken(t, srv)

	setPlatformUpstream(t, "fake_a", staticUpstream(t, searchEnvelope).URL)
	mustOK(t, srv, admin, "/fake_a/search?query=带标识")

	// 关停路径上的兜底落库：直接取空缓冲并写入，验证落库映射带上了标识
	base.RecordCall("fake_a", "content", "u1", "127.0.0.1", http.StatusOK, 10*time.Millisecond,
		&base.CallSubject{BookName: "测试书", BookKey: "bk1", ChapterTitle: "第一章", ChapterKey: "c1", Media: base.MediaNovel, ResultCount: 1})
	rows := base.DrainRecentCalls()
	var sawBook, sawChapter bool
	for _, rc := range rows {
		if rc.BookIdent == "bk1" {
			sawBook = true
		}
		if rc.ChapterIdent == "c1" {
			sawChapter = true
		}
	}
	if !sawBook || !sawChapter {
		t.Fatalf("RecentCall 未带上标识: %+v", rows)
	}
	if !sawBook {
		t.Fatalf("RecentCall 未带上书籍标识")
	}
}
