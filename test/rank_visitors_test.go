package test

// 书名榜的统计口径（待办清单 P18）：排名数字是「同数据源内的访问人数」，不是章节请求数。
// 一条 content 明细就是一章正文，按次数排会把「谁在读」写成「被翻了多少章」。
// 这里直接打聚合引擎（口径住在引擎里，两个消费口共用），另有一条走公开榜验单位与口径下发。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"loomproxy/base"
	"loomproxy/db"
	"loomproxy/handlers/subjectrank"
	"loomproxy/models"
)

// insertCall 造一条明细，调用者身份可控（用户名 / 匿名 IP）——人数口径的用例全靠这一维。
// media 传空串表示「未判定」，与源没声明媒介的真实形态一致。
func insertCall(t *testing.T, source, action, username, ip, book, keyword, chapter, media string, status int) {
	t.Helper()
	row := models.ApiCallLog{
		Username: username, IP: ip, Source: source, Action: action,
		Status: status, LatencyMs: 10, CreatedAt: time.Now(),
		BookName: book, Keyword: keyword, ChapterTitle: chapter,
		ResultCount: 1, Media: media,
	}
	if err := db.DB.Create(&row).Error; err != nil {
		t.Fatalf("插入明细失败: %v", err)
	}
}

func findItem(t *testing.T, items []subjectrank.Item, name string) subjectrank.Item {
	t.Helper()
	for _, it := range items {
		if it.Name == name {
			return it
		}
	}
	t.Fatalf("榜单里没有 %q：%+v", name, items)
	return subjectrank.Item{}
}

// TestRankBookBoardCountsVisitorsNotChapters 人数与次数分列、排名取人数：
// 连读多章算一个人、匿名按 IP 认人、跨源相加但不再跨源去重；成功率分母仍是请求数
func TestRankBookBoardCountsVisitorsNotChapters(t *testing.T) {
	newTestServer(t)
	base.DrainRecentCalls() // 别的用例留在环形缓冲里的明细会串进统计

	// 三人各读一章 → 3 人 3 次（榜单应把它排在只被两人读过的书前面）
	insertCall(t, "fake_a", "content", "reader1", "10.0.0.1", "三人都在读", "", "第1章", base.MediaNovel, 200)
	insertCall(t, "fake_a", "content", "reader2", "10.0.0.2", "三人都在读", "", "第1章", base.MediaNovel, 200)
	insertCall(t, "fake_a", "content", "reader3", "10.0.0.3", "三人都在读", "", "第1章", base.MediaNovel, 200)
	// 一个人连读 4 章（其中一次失败）+ 另一个人读 1 章 → 2 人 5 次
	insertCall(t, "fake_a", "content", "deep_reader", "10.0.0.7", "长篇被误读的书", "", "第1章", base.MediaNovel, 200)
	insertCall(t, "fake_a", "content", "deep_reader", "10.0.0.7", "长篇被误读的书", "", "第2章", base.MediaNovel, 200)
	insertCall(t, "fake_a", "content", "deep_reader", "10.0.0.7", "长篇被误读的书", "", "第3章", base.MediaNovel, 500)
	insertCall(t, "fake_a", "content", "deep_reader", "10.0.0.7", "长篇被误读的书", "", "第4章", base.MediaNovel, 200)
	insertCall(t, "fake_a", "content", "casual", "10.0.0.8", "长篇被误读的书", "", "第5章", base.MediaNovel, 200)
	// 匿名按 IP 认人：两个 IP 共读三章 → 2 人 3 次
	insertCall(t, "fake_a", "content", "", "10.0.0.9", "匿名在读的书", "", "第1章", base.MediaNovel, 200)
	insertCall(t, "fake_a", "content", "", "10.0.0.9", "匿名在读的书", "", "第2章", base.MediaNovel, 200)
	insertCall(t, "fake_a", "content", "", "10.0.0.4", "匿名在读的书", "", "第3章", base.MediaNovel, 200)
	// 同一用户在两个源都读过 → 人数 2（同数据源内去重、跨源相加），筛到单源后又回到 1
	insertCall(t, "fake_a", "content", "cross_user", "10.0.0.5", "跨源被读的书", "", "第1章", base.MediaNovel, 200)
	insertCall(t, "fake_b", "content", "cross_user", "10.0.0.5", "跨源被读的书", "", "第1章", base.MediaNovel, 200)

	items, err := subjectrank.Query("book", 7, "", "", 0, nil)
	if err != nil {
		t.Fatalf("书名榜查询失败: %v", err)
	}

	deep := findItem(t, items, "长篇被误读的书")
	if deep.Total != 2 || deep.Visitors != 2 || deep.Requests != 5 {
		t.Errorf("长篇：total=%d visitors=%d requests=%d, want 2/2/5（按次数排名正是 P18 的问题）",
			deep.Total, deep.Visitors, deep.Requests)
	}
	// 成功率与平均耗时分母必须是请求数：拿人数算会把 5 次里的 1 次失败稀释
	if deep.Success != 4 || deep.Failed != 1 {
		t.Errorf("长篇：success=%d failed=%d, want 4/1", deep.Success, deep.Failed)
	}
	if deep.SuccessRate < 79 || deep.SuccessRate > 81 {
		t.Errorf("长篇：success_rate=%.1f, want 80.0（分母是 5 次请求而非 2 个人）", deep.SuccessRate)
	}
	if deep.AvgLatencyMs != 10 {
		t.Errorf("长篇：avg_latency=%d, want 10", deep.AvgLatencyMs)
	}

	anon := findItem(t, items, "匿名在读的书")
	if anon.Visitors != 2 || anon.Requests != 3 {
		t.Errorf("匿名书：visitors=%d requests=%d, want 2/3（空用户名要退到 IP）", anon.Visitors, anon.Requests)
	}

	cross := findItem(t, items, "跨源被读的书")
	if cross.Visitors != 2 || len(cross.Sources) != 2 {
		t.Errorf("跨源书：visitors=%d sources=%v, want 2 与两个源", cross.Visitors, cross.Sources)
	}
	onlyA, err := subjectrank.Query("book", 7, "fake_a", "", 0, nil)
	if err != nil {
		t.Fatalf("按源筛书名榜失败: %v", err)
	}
	if got := findItem(t, onlyA, "跨源被读的书"); got.Visitors != 1 {
		t.Errorf("按 fake_a 筛后 visitors=%d, want 1（去重范围跟着筛选收紧）", got.Visitors)
	}

	// 排序按人数：3 人的书排在 2 人的书前面——按章节数时它是输的那本
	if items[0].Name != "三人都在读" {
		t.Errorf("榜首 = %q（total=%d），want 三人都在读", items[0].Name, items[0].Total)
	}

	// 搜索词榜维持次数口径，人数只是附带的一列
	insertCall(t, "fake_a", "search", "repeat_user", "10.0.0.6", "", "同人重复搜的词", "", base.MediaNovel, 200)
	insertCall(t, "fake_a", "search", "repeat_user", "10.0.0.6", "", "同人重复搜的词", "", base.MediaNovel, 200)
	kw, err := subjectrank.Query("keyword", 7, "", "", 0, nil)
	if err != nil {
		t.Fatalf("搜索词榜查询失败: %v", err)
	}
	word := findItem(t, kw, "同人重复搜的词")
	if word.Total != 2 || word.Requests != 2 || word.Visitors != 1 {
		t.Errorf("搜索词：total=%d requests=%d visitors=%d, want 2/2/1（排名口径不变，人数附带）",
			word.Total, word.Requests, word.Visitors)
	}

	// 章节榜要的就是章节被打开的次数（同一个人重复打开也各算一次），人数只作附带列。
	// 章节名取本用例独占的名字：整个包的用例共用一张明细表，同名章节会被别人的数据加进来
	insertCall(t, "fake_a", "content", "chapter_repeat", "10.0.0.21", "章节口径书", "", "P18独占章节", base.MediaNovel, 200)
	insertCall(t, "fake_a", "content", "chapter_repeat", "10.0.0.21", "章节口径书", "", "P18独占章节", base.MediaNovel, 200)
	ch, err := subjectrank.Query("chapter", 7, "fake_a", "", 0, nil)
	if err != nil {
		t.Fatalf("章节榜查询失败: %v", err)
	}
	if got := findItem(t, ch, "P18独占章节"); got.Total != 2 || got.Requests != 2 || got.Visitors != 1 {
		t.Errorf("章节榜 total=%d requests=%d visitors=%d, want 2/2/1（章节维度按次数排名）",
			got.Total, got.Requests, got.Visitors)
	}
}

// TestRankVisitorsDedupeAcrossPendingBuffer 人数不能把「已落库的人」与「还在环里的人」各数一次：
// 同一个人读过的旧章节已落库、刚读的新章节还在缓冲里，直接相加就把一个人算成两个人
func TestRankVisitorsDedupeAcrossPendingBuffer(t *testing.T) {
	newTestServer(t)
	base.DrainRecentCalls()
	t.Cleanup(func() { base.DrainRecentCalls() })

	const book = "半落库的书"
	insertCall(t, "fake_a", "content", "solo_reader", "10.1.1.1", book, "", "第1章", base.MediaNovel, 200)

	// 同一个人的第二次请求还没落库（在内存环形缓冲里）
	base.RecordCall("fake_a", "content", "solo_reader", "10.1.1.1", http.StatusOK, 5*time.Millisecond,
		&base.CallSubject{BookName: book, ChapterTitle: "第2章", Media: base.MediaNovel, ResultCount: 1})

	items, err := subjectrank.Query("book", 7, "", "", 0, nil)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	got := findItem(t, items, book)
	if got.Requests != 2 {
		t.Errorf("requests=%d, want 2（请求数两侧都要计入）", got.Requests)
	}
	if got.Visitors != 1 || got.Total != 1 {
		t.Errorf("visitors=%d total=%d, want 1/1（同一个人被环缓冲重复数了一次）", got.Visitors, got.Total)
	}

	// 另一个人的明细只在环里（库里没有他的行），该被数到
	base.RecordCall("fake_a", "content", "new_reader", "10.1.1.2", http.StatusOK, 5*time.Millisecond,
		&base.CallSubject{BookName: book, ChapterTitle: "第9章", Media: base.MediaNovel, ResultCount: 1})
	items, err = subjectrank.Query("book", 7, "", "", 0, nil)
	if err != nil {
		t.Fatalf("二次查询失败: %v", err)
	}
	if got = findItem(t, items, book); got.Visitors != 2 {
		t.Errorf("新增一个只在环里的人后 visitors=%d, want 2（去重不该把人吃掉）", got.Visitors)
	}
}

// TestRankBoardsCarryUnit 公开榜要把口径与单位一起下发：两张榜的数字不同义，
// 面板不该自己猜「20」是人数还是次数
func TestRankBoardsCarryUnit(t *testing.T) {
	srv := newTestServer(t)
	base.DrainRecentCalls()
	admin := adminToken(t, srv)

	insertCall(t, "fake_a", "content", "u_unit", "10.2.2.1", "单位测试书", "", "第1章", base.MediaNovel, 200)
	insertCall(t, "fake_a", "content", "u_unit", "10.2.2.1", "单位测试书", "", "第2章", base.MediaNovel, 200)
	if status, env := doJSON(t, srv, "PUT", "/admin/settings/rank_public_sources",
		map[string]string{"value": "fake_a,fake_b"}, authHeader(admin)); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("开放榜单源失败: status=%d msg=%s", status, env.Msg)
	}

	status, env := doJSON(t, srv, "GET", "/rank/boards?days=7", nil, authHeader(admin))
	if status != http.StatusOK {
		t.Fatalf("读榜失败: %d", status)
	}
	data := env.dataMap(t)
	list, _ := data["boards"].([]interface{})
	byDim := map[string]map[string]interface{}{}
	for _, raw := range list {
		b := raw.(map[string]interface{})
		byDim[b["dim"].(string)] = b
	}
	book := byDim["book"]
	if book["unit"] != "人" {
		t.Errorf("阅读榜 unit = %v, want 人", book["unit"])
	}
	if book["metric"] != "访问人数（同数据源内按用户去重）" {
		t.Errorf("阅读榜 metric = %v, want 口径说明", book["metric"])
	}
	if kw := byDim["keyword"]; kw["unit"] != "次" {
		t.Errorf("搜索榜 unit = %v, want 次（两张榜不同义，缺一个就会统一被读成热度）", kw["unit"])
	}

	rows, _ := book["rows"].([]interface{})
	found := false
	for _, r := range rows {
		it := r.(map[string]interface{})
		if it["name"] == "单位测试书" {
			found = true
			if it["total"].(float64) != 1 {
				t.Errorf("公开榜阅读榜 total=%v, want 1（同一人读两章算一个人）", it["total"])
			}
		}
	}
	if !found {
		t.Error("公开榜阅读榜里没有那条测试书")
	}
}

// TestMonitorSubjectsBookCarriesBookID 书名维度要把书目标识带在响应里（面板不展示，
// 但拿榜单去检索的人需要它：书名会重名，取详情与正文只认标识）。
// 公开榜仍只给名称与次数——标识是稳定的内部主键，给普通用户等于多给一条可关联的线索。
func TestMonitorSubjectsBookCarriesBookID(t *testing.T) {
	srv := newTestServer(t)
	base.DrainRecentCalls()
	admin := adminToken(t, srv)

	insert := func(chapter, ident string) {
		t.Helper()
		row := models.ApiCallLog{
			Username: "ident_reader", IP: "10.3.3.1", Source: "fake_a", Action: "content",
			Status: http.StatusOK, LatencyMs: 12, CreatedAt: time.Now(),
			BookName: "带标识的书", ChapterTitle: chapter, BookIdent: ident,
			Media: base.MediaNovel, ResultCount: 1,
		}
		if err := db.DB.Create(&row).Error; err != nil {
			t.Fatalf("写入明细失败: %v", err)
		}
	}
	insert("第一章", "7400000000000000001")
	insert("第二章", "")                    // 旧行只记到名称：不能被它把标识判成空
	insert("第三章", "7400000000000000002") // 最近一条带标识的取它

	_, env := doJSON(t, srv, "GET", "/admin/monitor/subjects?dim=book&days=7", nil, authHeader(admin))
	items, _ := env.dataMap(t)["items"].([]interface{})
	var got map[string]interface{}
	for _, raw := range items {
		it := raw.(map[string]interface{})
		if it["name"] == "带标识的书" {
			got = it
		}
	}
	if got == nil {
		t.Fatalf("书名榜里没有那条测试书：%v", items)
	}
	if got["book_id"] != "7400000000000000002" {
		t.Errorf("book_id = %v, want 最近一条非空标识 7400000000000000002", got["book_id"])
	}
	if got["last_called_at"] == nil {
		t.Error("last_called_at 与 book_id 是两件事，前者不该被后者挤掉")
	}

	// 公开榜：放行 fake_a 后读榜，响应里不许出现 book_id
	if status, env := doJSON(t, srv, "PUT", "/admin/settings/rank_public_sources",
		map[string]string{"value": "fake_a"}, authHeader(admin)); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("开放榜单源失败: %d %s", status, env.Msg)
	}
	// 公开榜的阅读榜给同一个 book_id：下游看到榜就想按 id 检索，拿书名去撞重名是白绕路。
	// 收窄的仍是那几样能反推到人的东西——时间与数据源归属不出这个门。
	_, env = doJSON(t, srv, "GET", "/rank/boards?days=7", nil, authHeader(admin))
	raw := string(env.Data)
	if !strings.Contains(raw, `"book_id":"7400000000000000002"`) {
		t.Errorf("公开榜阅读榜没带 book_id：%s", raw[:min(len(raw), 400)])
	}
	// 逐条目只看键集：顶层的 `sources` 是筛选候选（面板下拉要用），不是明细归属字段，
	// 所以这条断言必须落在条目上而不是整份响应上
	data := env.dataMap(t)
	var boards []struct {
		Dim  string                   `json:"dim"`
		Rows []map[string]interface{} `json:"rows"`
	}
	raw2, err := json.Marshal(data["boards"])
	if err != nil {
		t.Fatalf("重编码 boards 失败: %v", err)
	}
	if err := json.Unmarshal(raw2, &boards); err != nil {
		t.Fatalf("解析公开榜条目失败: %v", err)
	}
	for _, b := range boards {
		for _, row := range b.Rows {
			for k := range row {
				switch k {
				case "name", "total", "book_id":
				default:
					t.Errorf("%s 榜的条目多出了字段 %q（公开榜只给名称、次数与阅读榜的 book_id）", b.Dim, k)
				}
			}
			switch b.Dim {
			case "book":
				if row["name"] == "带标识的书" && row["book_id"] != "7400000000000000002" {
					t.Errorf("阅读榜 book_id = %v, want 与明细一致的标识", row["book_id"])
				}
			case "keyword":
				if _, ok := row["book_id"]; ok {
					t.Errorf("搜索榜不该带 book_id：%v", row)
				}
			}
		}
	}
	if strings.Contains(raw, "ident_reader") || strings.Contains(raw, "10.3.3.1") {
		t.Error("公开榜漏出了调用者身份")
	}

	// 其它维度不带这个字段（keyword 维度没有书目标识可言）
	_, env = doJSON(t, srv, "GET", "/admin/monitor/subjects?dim=keyword&days=7", nil, authHeader(admin))
	if raw := string(env.Data); strings.Contains(raw, `"book_id"`) {
		t.Error("搜索词维度不该带 book_id")
	}
}
