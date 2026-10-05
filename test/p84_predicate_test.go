package test

// 待办清单 P84 的②（清存量 912 行）已拍 B，执行预案在 `docs/运维/跨书串名存量清理预案.md`。
// 这条用例验的是**预案 §2 的口径 SQL 本身**：它在真实 schema（`models.ApiCallLog`）上跑得动、
// 且只选中该选的行。跑在 SQLite 上是因为集成测试没有 MySQL；口径那段 SQL 是两种方言都通的
// （派生表 + GROUP BY/HAVING + JOIN），预案里唯一方言相关的是抽样那条 `ORDER BY RAND()`
// （MySQL 写法，SQLite 是 RANDOM()），它不参与执行，所以不在本用例范围内。

import (
	"testing"
	"time"

	"loomproxy/db"
	"loomproxy/models"
)

// 与预案 §2② 同形的谓词（把 JOIN 子查询原样抄过来，改一个字就算白验）
const p84DoomedCount = `
SELECT COUNT(*) FROM api_call_logs l
JOIN (SELECT source, chapter_ident FROM api_call_logs
      WHERE action='content' AND chapter_ident <> '' AND book_ident <> ''
      GROUP BY source, chapter_ident HAVING COUNT(DISTINCT book_ident) > 1) d
  ON d.source=l.source AND d.chapter_ident=l.chapter_ident
WHERE l.action='content' AND l.chapter_title <> '' AND l.book_ident <> ''`

const p84GroupsCount = `
SELECT COUNT(*) FROM (
  SELECT source, chapter_ident FROM api_call_logs
  WHERE action='content' AND chapter_ident <> '' AND book_ident <> ''
  GROUP BY source, chapter_ident HAVING COUNT(DISTINCT book_ident) > 1
) g`

func insertLog(t *testing.T, source, action, bookIdent, chapterIdent, chapterTitle string) {
	t.Helper()
	row := models.ApiCallLog{
		Source: source, Action: action, BookIdent: bookIdent,
		ChapterIdent: chapterIdent, ChapterTitle: chapterTitle,
		CreatedAt: time.Now(),
	}
	if err := db.DB.Create(&row).Error; err != nil {
		t.Fatalf("插入明细失败: %v", err)
	}
}

func countSQL(t *testing.T, q string) int64 {
	t.Helper()
	var n int64
	if err := db.DB.Raw(q).Scan(&n).Error; err != nil {
		t.Fatalf("预案里的 SQL 跑不动（列名或谓词写错了）: %v", err)
	}
	return n
}

func TestP84CleanupPredicateSelectsExactlyTheCrossBookRows(t *testing.T) {
	newTestServer(t)

	// 夹具按现网形状造：一组撞键、一组同名同书（合法）、一组只有一个书（没撞）、
	// 一组撞键但 chapter_title 为空（本来就没被骗，不该算进待清）
	// A：同一 chapter_ident 挂在 3 本书下，其中 2 行带着借来的同名标题、1 行标题为空
	insertLog(t, "fake_a", "content", "book-1", "chap-7", "第七章")
	insertLog(t, "fake_a", "content", "book-2", "chap-7", "第七章")
	insertLog(t, "fake_a", "content", "book-3", "chap-7", "")
	// B：撞键组但只有一个书标识 → 不属于这一族
	insertLog(t, "fake_a", "content", "book-9", "chap-1", "第一章")
	insertLog(t, "fake_a", "content", "book-9", "chap-1", "第一章")
	// C：跨书但章节标识为空 → 键的第二段就没有，不参与
	insertLog(t, "fake_b", "content", "book-1", "", "某章")
	insertLog(t, "fake_b", "content", "book-2", "", "某章")
	// D：search 动作带同名 chapter_title → action 不是 content，不该被清
	insertLog(t, "fake_c", "search", "book-1", "chap-3", "第三章")
	insertLog(t, "fake_c", "search", "book-2", "chap-3", "第三章")
	// E：撞键但 book_ident 为空 → 清了永远补不回来，口径明确排除
	insertLog(t, "fake_c", "content", "", "chap-5", "第五章")
	insertLog(t, "fake_c", "content", "", "chap-5", "第五章")
	// F：第二组真撞键（2 本书都带着各自的标题），加进期望计数
	insertLog(t, "fake_d", "content", "book-A", "chap-2", "第二章")
	insertLog(t, "fake_d", "content", "book-B", "chap-2", "第二章")

	if got := countSQL(t, p84GroupsCount); got != 2 {
		t.Errorf("撞键组数 = %d, want 2（A 与 F；B/C/D/E 都不该算进来）", got)
	}
	if got := countSQL(t, p84DoomedCount); got != 4 {
		t.Errorf("待清行数 = %d, want 4（A 的 2 行已带标题 + F 的 2 行）", got)
	}

	// 时间跨度那条（§2③）也必须能跑：它决定验收时走自动回填还是手动 90 天入口
	// MIN()/MAX() 在 SQLite 回来的是字符串，扫进 time.Time 会被 database/sql 拒——
	// 这里要验的是"这条 SQL 跑得动、列名对"，所以扫进字符串（MySQL 上两种都能扫）
	var span struct {
		Oldest string
		Newest string
	}
	err := db.DB.Raw(`SELECT MIN(l.created_at) AS oldest, MAX(l.created_at) AS newest FROM api_call_logs l
JOIN (SELECT source, chapter_ident FROM api_call_logs
      WHERE action='content' AND chapter_ident <> '' AND book_ident <> ''
      GROUP BY source, chapter_ident HAVING COUNT(DISTINCT book_ident) > 1) d
  ON d.source=l.source AND d.chapter_ident=l.chapter_ident
WHERE l.action='content' AND l.chapter_title <> '' AND l.book_ident <> ''`).
		Scan(&span).Error
	if err != nil {
		t.Fatalf("§2③ 时间跨度那条跑不动: %v", err)
	}
	if span.Oldest == "" || span.Newest == "" {
		t.Errorf("§2③ 数出来是空（夹具的行没被谓词选中？oldest=%q newest=%q）", span.Oldest, span.Newest)
	}
}
