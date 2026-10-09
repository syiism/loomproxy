package groupb

// 待办清单 P118 ①：内容维度榜单原来在聚合之后**逐条目**回取两栏（`last_called_at` 一条、
// 书名维度再加一条 `book_ident`），公开榜一次请求就是 40~60 条单查（现网实测每条 ~20ms）。
// 现在这两栏并进主聚合的 `MAX(created_at)` / `MAX(CASE WHEN book_ident <> '' THEN id END)`，
// 标识的值整页一次 `IN` 取回。
//
// 断言有两条腿，缺一不可（形状沿用 `redeem_list_queries_test.go`，判据正本在踩坑判据
// 「列表端点的关联名称要整页一次取回」）：
//   - **增长率**：两个不同页长的调用发出**相同条数**的查询。写死"≤N 条"会被这条路径以外的
//     读数（后台名称回填循环也读 `api_call_logs`）污染成假红，而"随行数变不变"是缺陷本身的定义；
//   - **值还要对**：只测条数的话，把两栏整个删掉也能"变快"，那不是守卫。
//     `book_ident` 那一栏的三个向量都是当初 `ORDER BY id DESC` 的语义细节（最新的没标识就取次新的），
//     改成批量后最容易在这里说谎，所以逐个钉。

import (
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"loomproxy/base"
	"loomproxy/db"
	"loomproxy/handlers/subjectrank"
	"loomproxy/models"
)

// tailProbeUser 本用例造的明细行的归属名：既是访问者身份的取样，也是清理时的过滤键
// （**不整表删**——同一张表上还留着别的用例的样本）。
const tailProbeUser = "p118_u"

// newCallLogCounter 数「这一趟调用对 api_call_logs 发了几条查询」，并带上 SQL 文本
// 供失败时第一眼看出是谁的查询（同 redeem 那条用例的教训：全局回调会被后台巡检污染）。
// 注册在 `gorm:query` **之后**才读得到渲染好的 SQL。
func newCallLogCounter(t *testing.T) func(func()) (int, []string) {
	t.Helper()
	n := 0
	var sqls []string
	if err := db.DB.Callback().Query().After("gorm:query").Register("test/count-calllog", func(tx *gorm.DB) {
		tbl := tx.Statement.Table
		if tbl == "" && tx.Statement.Schema != nil {
			tbl = tx.Statement.Schema.Table
		}
		if tbl != "api_call_logs" {
			return
		}
		n++
		sqls = append(sqls, tx.Statement.SQL.String())
	}); err != nil {
		t.Fatalf("注册计数回调失败: %v", err)
	}
	t.Cleanup(func() { _ = db.DB.Callback().Query().Remove("test/count-calllog") })
	return func(fn func()) (int, []string) {
		n, sqls = 0, nil
		fn()
		return n, sqls
	}
}

// insertBoardLog 直插一条明细（榜单读的是已落库明细，不经假源调用链）
func insertBoardLog(t *testing.T, row *models.ApiCallLog) {
	t.Helper()
	if err := db.DB.Create(row).Error; err != nil {
		t.Fatalf("写入明细失败: %v", err)
	}
}

// TestSubjectRankBoardQueriesDoNotGrowWithRows P118 ① 的腿一：查询条数不随榜面条目数增长。
func TestSubjectRankBoardQueriesDoNotGrowWithRows(t *testing.T) {
	newTestServer(t)
	base.DrainRecentCalls() // 内存缓冲里那些不参与榜尾那圈；先排空，免得 pending 的回查掺进来
	t.Cleanup(func() { db.DB.Where("username = ?", tailProbeUser).Delete(&models.ApiCallLog{}) })

	const src = "fake_a"
	// 三个互不相同的书名，各自 1~3 行：limit=1 与 limit=3 的**条目数不同**，
	// 改之前榜尾单查会跟着条目数一起长（书名维度还翻倍：标识一条 + 时间一条）
	names := []string{"P118页数甲", "P118页数乙", "P118页数丙"}
	for i, name := range names {
		for k := 0; k <= i; k++ {
			insertBoardLog(t, &models.ApiCallLog{
				Source: src, Action: "content", Status: 200, LatencyMs: 5,
				CreatedAt: time.Now(), BookName: name, BookIdent: "bk-" + name,
				Media: base.MediaNovel, Username: tailProbeUser,
			})
		}
	}

	count := newCallLogCounter(t)
	var one, three []subjectrank.Item
	var oneErr, threeErr error
	oneN, oneSQL := count(func() { one, oneErr = subjectrank.Query("book", 7, src, "", 1, nil) })
	threeN, threeSQL := count(func() { three, threeErr = subjectrank.Query("book", 7, src, "", 3, nil) })
	if oneErr != nil || threeErr != nil {
		t.Fatalf("榜单查询失败: %v / %v", oneErr, threeErr)
	}
	if len(one) != 1 || len(three) != 3 {
		t.Fatalf("样本没凑出两种页长：limit=1 得 %d 条、limit=3 得 %d 条", len(one), len(three))
	}
	if oneN != threeN {
		t.Errorf("榜尾回查仍随条目数增长：1 条页长发 %d 次、3 条页长发 %d 次\nlimit=1: %s\nlimit=3: %s",
			oneN, threeN, strings.Join(oneSQL, "\n"), strings.Join(threeSQL, "\n"))
	}
	// 缺陷的形状本身：一条都不该有（原写法每个条目一条 `ORDER BY id DESC LIMIT 1`）
	for _, batch := range []struct {
		label string
		sqls  []string
	}{{"limit=1", oneSQL}, {"limit=3", threeSQL}} {
		for _, s := range batch.sqls {
			if strings.Contains(s, "ORDER BY id DESC") {
				t.Errorf("%s 这条查询还在按条目逐条回取（`ORDER BY id DESC`）：%s", batch.label, s)
			}
		}
	}
}

// TestSubjectRankBoardTailFields P118 ① 的腿二：并进聚合之后，两栏的值仍然是原语义。
//
// `book_ident` 的原语义是「窗口内**最近一条带标识的**」，不是「最新一条的标识」——
// 旧行可能只记到名称（升级前写入），正文与目录也可能反查不到标识（缓存未命中）。
// 三个向量分别钉住：最新那行缺标识、两行都带标识取较新、同名跨源取较新。
func TestSubjectRankBoardTailFields(t *testing.T) {
	newTestServer(t)
	base.DrainRecentCalls()
	t.Cleanup(func() { db.DB.Where("username = ?", tailProbeUser).Delete(&models.ApiCallLog{}) })

	base1 := time.Now().Add(-time.Hour)
	mk := func(src, name, ident string, age time.Duration) {
		insertBoardLog(t, &models.ApiCallLog{
			Source: src, Action: "content", Status: 200, LatencyMs: 5,
			CreatedAt: base1.Add(age), BookName: name, BookIdent: ident,
			Media: base.MediaNovel, Username: tailProbeUser,
		})
	}
	mk("fake_a", "P118缺标识最新行", "bk-old", 0)
	mk("fake_a", "P118缺标识最新行", "", 2*time.Hour) // 最新的一行**没有**标识
	mk("fake_a", "P118两行都有", "bk-A", 0)
	mk("fake_a", "P118两行都有", "bk-B", 3*time.Hour)
	mk("fake_a", "P118跨源书名", "", 4*time.Hour) // fake_a 最新一行没标识
	mk("fake_b", "P118跨源书名", "bk-cross", 1*time.Hour)

	// 关键词维度：last_called_at 取窗口内最新一条的时间
	insertBoardLog(t, &models.ApiCallLog{
		Source: "fake_a", Action: "search", Status: 200, LatencyMs: 5,
		CreatedAt: base1, Keyword: "P118搜索词", Media: base.MediaNovel, Username: tailProbeUser,
	})
	latest := base1.Add(30 * time.Minute)
	insertBoardLog(t, &models.ApiCallLog{
		Source: "fake_a", Action: "search", Status: 200, LatencyMs: 5,
		CreatedAt: latest, Keyword: "P118搜索词", Media: base.MediaNovel, Username: tailProbeUser,
	})

	kw, err := subjectrank.Query("keyword", 7, "", "", 0, nil)
	if err != nil {
		t.Fatalf("关键词榜失败: %v", err)
	}
	var found bool
	for _, it := range kw {
		if it.Name != "P118搜索词" {
			continue
		}
		found = true
		if it.LastCalledAt == nil {
			t.Errorf("关键词榜的 last_called_at 为空（最新一行 %s）", latest)
		} else if !it.LastCalledAt.Equal(latest) {
			t.Errorf("last_called_at 应等于窗口内最新一条 %s，实为 %s", latest, *it.LastCalledAt)
		}
	}
	if !found {
		t.Fatalf("关键词榜里没有那条搜索词（共 %d 条）", len(kw))
	}

	books, err := subjectrank.Query("book", 7, "", "", 0, nil)
	if err != nil {
		t.Fatalf("书名榜失败: %v", err)
	}
	want := map[string]string{
		"P118缺标识最新行": "bk-old",   // 最新一行没标识 → 取次新的带标识那条
		"P118两行都有":   "bk-B",     // 都带标识 → 取较新的
		"P118跨源书名":   "bk-cross", // 同名跨源 → 取带标识里较新的那一条（不分源）
	}
	got := map[string]string{}
	calledAt := map[string]*time.Time{}
	for _, it := range books {
		got[it.Name] = it.BookID
		calledAt[it.Name] = it.LastCalledAt
	}
	for name, ident := range want {
		if got[name] != ident {
			t.Errorf("%s 的 book_id 应为 %q（口径是「最近一条带标识的」），实为 %q", name, ident, got[name])
		}
		if calledAt[name] == nil {
			t.Errorf("%s 的 last_called_at 为空", name)
		}
	}
}
