package test

// 待办清单 P84：章节名的键必须带书维度。
//
// 现网实测（14 天内的 `api_call_logs`，只读计数）：`(source, chapter_ident)` 有 **123 组跨书重复**，
// 极端的例子是 uxx 的标识 "0"——出现在 **96 本不同的书**下、对应 **35 个不同标题**。
// 键不含书时，后登记的那本会覆盖前一本：面板章节榜会显示**另一本书**的标题，
// 而监控回填还会把那个错标题**写进别的书的明细行**（那就是把错读数固化了）。
// 标识是否全局唯一由各上游决定（章节 URL 唯一、章号不唯一），框架不去猜：
// 没有书标识就整条不登记、不反查——名称留空是合法状态，猜一个才是错（同 `SubjectStore` 的契约）。

import (
	"testing"

	"loomproxy/app"
	"loomproxy/base"
	"loomproxy/db"
	"loomproxy/models"
)

func TestChapterNameCacheIsScopedPerBook(t *testing.T) {
	newTestServer(t)
	base.ResetNameCaches()

	const (
		source = "fake_a"
		ident  = "0" // 刻意用现网最撞的那个值
	)
	base.RememberChapter(source, "book-one", ident, "第一章 起风了")
	base.RememberChapter(source, "book-two", ident, "序言 雪")

	if got := base.LookupChapter(source, "book-one", ident); got != "第一章 起风了" {
		t.Errorf("book-one 的章名查到了 %q，want「第一章 起风了」——键没带书维度时后登记的会把它覆盖掉", got)
	}
	if got := base.LookupChapter(source, "book-two", ident); got != "序言 雪" {
		t.Errorf("book-two 的章名查到了 %q, want「序言 雪」", got)
	}

	// 不猜：没有书标识（或没有章标识）时既登记不进也查不出
	base.RememberChapter(source, "", ident, "无主的标题")
	if got := base.LookupChapter(source, "", ident); got != "" {
		t.Errorf("缺书标识时反查竟然返回了 %q——框架不该猜这是哪本书的章", got)
	}
	if got := base.LookupChapter(source, "book-three", ident); got != "" {
		t.Errorf("没登记过的书 book-three 拿到了 %q——同标识跨书必须各自独立", got)
	}
}

func TestBackfillDoesNotBorrowAnotherBooksChapterTitle(t *testing.T) {
	newTestServer(t)
	base.ResetNameCaches()
	base.ResetMetrics()

	const (
		source = "fake_a"
		ident  = "0"
	)
	// 只有 book-two 登记过这个章标识；book-one 的明细行缺章名
	base.RememberChapter(source, "book-two", ident, "序言 雪")
	row := insertIdentRow(t, source, "book-one", "", ident, "")

	app.RunSubjectBackfillTick()

	var got *string
	if err := db.DB.Model(&models.ApiCallLog{}).Where("id = ?", row).
		Select("chapter_title").Row().Scan(&got); err != nil {
		t.Fatalf("读回明细行失败: %v", err)
	}
	if got != nil && *got != "" {
		t.Errorf("book-one 的章名被填成了另一本书的 %q——回填必须按（书, 章）取，取不到就留空", *got)
	}

	// 反向自检：同一轮里 book-two 自己的行应当能被填上，否则「留空」可能只是整条回填通路坏了
	base.RememberBook(source, "book-two", "雪集", base.MediaNovel)
	rowTwo := insertIdentRow(t, source, "book-two", "", ident, "")
	app.RunSubjectBackfillTick()
	var title string
	if err := db.DB.Model(&models.ApiCallLog{}).Where("id = ?", rowTwo).
		Select("IFNULL(chapter_title,'')").Row().Scan(&title); err != nil {
		t.Fatalf("读回 book-two 的明细行失败: %v", err)
	}
	if title != "序言 雪" {
		t.Fatalf("book-two 自己的行也没填上（得到 %q）——那上面那句「留空」就没意义了，先修回填通路", title)
	}
}
