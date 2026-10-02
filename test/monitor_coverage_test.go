package test

// 内容维度覆盖率的自我观测（痛点清单 #6）：哪一格没采到，不该靠人肉写 SQL 才发现。
// 这条同时钉住两件事：判据必须 COALESCE（`book_ident` 是后加的列，旧行是 NULL，
// 用 <> '' 会把 NULL 判成"没标识"——我就被这个坑过一次，把 99% 的源读成 50%），
// 以及「search/explore 没有书名」是合法状态、不该被标成缺失。

import (
	"net/http"
	"testing"
	"time"

	"loomproxy/base"
	"loomproxy/db"
	"loomproxy/handlers/subjectrank"
	"loomproxy/models"
)

func coverageRow(t *testing.T, rows []subjectrank.CoverageRow, source, action string) subjectrank.CoverageRow {
	t.Helper()
	for _, r := range rows {
		if r.Source == source && r.Action == action {
			return r
		}
	}
	t.Fatalf("覆盖率里没有 %s/%s：%+v", source, action, rows)
	return subjectrank.CoverageRow{}
}

func TestContentDimensionCoverage(t *testing.T) {
	srv := newTestServer(t)
	base.DrainRecentCalls()

	mk := func(action, book, ident, chapter, keyword, media string, status int) {
		t.Helper()
		row := models.ApiCallLog{
			Username: "cov_u", IP: "10.4.4.1", Source: "fake_a", Action: action,
			Status: status, LatencyMs: 9, CreatedAt: time.Now(),
			BookName: book, BookIdent: ident, ChapterTitle: chapter, Keyword: keyword,
			Media: media, ResultCount: 1,
		}
		if err := db.DB.Create(&row).Error; err != nil {
			t.Fatalf("写入明细失败: %v", err)
		}
	}
	// content：3 行全维度 + 1 行书名标识都缺（再叠加下面那条 NULL 行，凑过 5 行展示门槛）
	mk("content", "覆盖率测试书", "bk-1", "第一章", "", base.MediaNovel, http.StatusOK)
	mk("content", "覆盖率测试书", "bk-1", "第一章", "", base.MediaNovel, http.StatusOK)
	mk("content", "覆盖率测试书", "bk-1", "第一章", "", base.MediaNovel, http.StatusOK)
	mk("content", "", "", "第一章", "", "", http.StatusInternalServerError)
	// NULL 与空串是两回事：这条直接写 SQL，把 book_ident 留成 NULL（模拟后加列之前的旧行）
	if err := db.DB.Exec(`INSERT INTO api_call_logs (source, action, username, ip, status, latency_ms, book_name, chapter_title, media, result_count, created_at)
		VALUES ('fake_a','content','cov_u','10.4.4.1',200,9,'覆盖率测试书','第二章','novel',1,?)`, time.Now()).Error; err != nil {
		t.Fatalf("写入 NULL 标识行失败: %v", err)
	}
	// search 没有单一书名，属合法留空；keyword 才是它的维度
	for i := 0; i < 6; i++ {
		mk("search", "", "", "", "覆盖率测试词", base.MediaNovel, http.StatusOK)
	}
	// 门槛以下：零星请求不该刷出百分比
	mk("detail", "零星书", "bk-0", "", "", base.MediaNovel, http.StatusOK)

	rows, err := subjectrank.Coverage(7, "", nil)
	if err != nil {
		t.Fatalf("覆盖率统计失败: %v", err)
	}

	content := coverageRow(t, rows, "fake_a", "content")
	if content.Rows != 5 {
		t.Errorf("content rows = %d, want 5（含 NULL 标识那一行）", content.Rows)
	}
	// 三条 bk-1 + 一条空串 + 一条 NULL → 「有标识」只能数出 3；
	// 缺失数由 rows-有 推出（5-2=2），NULL 与空串因此不需要两套判据
	if content.HasBookIdent != 3 {
		t.Errorf("content has_book_ident = %d, want 3（NULL 行算没标识，但不能被当成第三类）", content.HasBookIdent)
	}
	if missing := content.Rows - content.HasBookIdent; missing != 2 {
		t.Errorf("缺失标识的行数 = %d, want 2（空串与 NULL 各一条，相减即得，不需要两套判据）", missing)
	}
	if content.HasBookName != 4 || content.HasChapterTitle != 5 {
		t.Errorf("content 书名=%d 章节=%d, want 4/5", content.HasBookName, content.HasChapterTitle)
	}
	if content.Failed != 1 {
		t.Errorf("content failed = %d, want 1", content.Failed)
	}
	if !content.ExpectBook {
		t.Error("content 理应带书名，ExpectBook 应为真（面板据此才标红）")
	}

	search := coverageRow(t, rows, "fake_a", "search")
	if search.HasBookName != 0 || search.ExpectBook {
		t.Errorf("search 不该被要求带书名：has_book_name=%d expect_book=%v", search.HasBookName, search.ExpectBook)
	}
	if search.HasKeyword != 6 {
		t.Errorf("search has_keyword = %d, want 6（它的维度是搜索词）", search.HasKeyword)
	}

	for _, r := range rows {
		if r.Action == "detail" {
			t.Errorf("单行 detail 越过了展示门槛（rows=%d），零星请求的百分比只会制造噪声", r.Rows)
		}
	}

	_, env := doJSON(t, srv, "GET", "/admin/monitor/subjects?dim=book&days=7", nil, authHeader(adminToken(t, srv)))
	if _, ok := env.dataMap(t)["coverage"]; !ok {
		t.Error("/admin/monitor/subjects 应带 coverage 块")
	}
}

func TestContentDimensionCoverageHonoursAllowList(t *testing.T) {
	newTestServer(t)
	base.DrainRecentCalls()
	for i := 0; i < 6; i++ {
		db.DB.Create(&models.ApiCallLog{
			Source: "fake_b", Action: "content", Status: 200, LatencyMs: 3, CreatedAt: time.Now(),
			BookName: "未授权源的书", BookIdent: "bk-x", Media: base.MediaNovel,
		})
		db.DB.Create(&models.ApiCallLog{
			Source: "fake_a", Action: "content", Status: 200, LatencyMs: 3, CreatedAt: time.Now(),
			BookName: "授权源的书", BookIdent: "bk-y", Media: base.MediaNovel,
		})
	}
	rows, err := subjectrank.Coverage(7, "", []string{"fake_a"})
	if err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if len(rows) == 0 {
		t.Error("白名单内应有统计结果")
	}
	for _, r := range rows {
		if r.Source != "fake_a" {
			t.Errorf("白名单只放 fake_a，却出现了 %s", r.Source)
		}
	}
	if empty, err := subjectrank.Coverage(7, "", []string{}); err != nil || len(empty) != 0 {
		t.Errorf("空白名单应返回空集，实得 %d 条 err=%v", len(empty), err)
	}
}
