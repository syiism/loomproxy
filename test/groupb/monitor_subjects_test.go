package groupb

// 内容维度与媒介的端到端用例。
//
// 两条读取路径要分开验：/admin/monitor/subjects 合并「库中明细 + 内存环形缓冲里尚未落库的
// 明细」，可以用真实调用直接驱动；/admin/monitor/history 只读库（缓冲满 250 条才批量落库），
// 所以筛选类断言一律直插明细。
//
// 上游经**平台默认 baseUrl** 接入（请求参数带 127.0.0.1 会被 SSRF 校验拒掉），
// 每种响应形状用一个独立的假源+上游地址：上游响应缓存按 URL 合并，同址复用会串形状。

import (
	"loomproxy/base"
	"loomproxy/db"
	"loomproxy/models"
	"loomproxy/testkit"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const (
	searchEnvelope = testkit.SearchEnvelope // 正本在 testkit（跨组共用）
	detailEnvelope = testkit.DetailEnvelope // 正本在 testkit（跨组共用）
	plainEnvelope  = `{"hello":"world"}`
)

func staticUpstream(t *testing.T, payload string) *httptest.Server {
	t.Helper()
	return testkit.StaticUpstream(t, payload)
}

// setPlatformUpstream 把某假源的平台默认 baseUrl 指向上游（可信来源，豁免 SSRF）
func setPlatformUpstream(t *testing.T, source, upstreamURL string) {
	t.Helper()
	testkit.SetPlatformUpstream(t, source, upstreamURL)
}

func mustOK(t *testing.T, srv *httptest.Server, token, path string) {
	t.Helper()
	testkit.MustOK(t, srv, token, path)
}

// subjectItems 维度榜单的 items（name → total）
func subjectItems(t *testing.T, srv *httptest.Server, admin string, kv map[string]string) map[string]int64 {
	return subjectItemsField(t, srv, admin, kv, "total")
}

// subjectItemsField 按指定字段取榜单条目（total / requests / visitors）。
// total 是「这个维度用来排名的那个数」，书名榜是人数、其余是次数（见 P18），
// 所以断言口径时要显式说要读哪一列。
func subjectItemsField(t *testing.T, srv *httptest.Server, admin string, kv map[string]string, field string) map[string]int64 {
	t.Helper()
	status, env := doJSON(t, srv, "GET", "/admin/monitor/subjects"+buildQuery(kv), nil, authHeader(admin))
	if status != http.StatusOK {
		t.Fatalf("subjects 期望 200，实为 %d（msg=%s）", status, env.Msg)
	}
	raw, _ := env.dataMap(t)["items"].([]interface{})
	out := map[string]int64{}
	for _, r := range raw {
		it, _ := r.(map[string]interface{})
		name, _ := it["name"].(string)
		v, _ := it[field].(float64)
		out[name] = int64(v)
	}
	return out
}

// historyTotal 历史明细分页的命中条数
func historyTotal(t *testing.T, srv *httptest.Server, admin string, kv map[string]string) int64 {
	t.Helper()
	status, env := doJSON(t, srv, "GET", "/admin/monitor/history"+buildQuery(kv), nil, authHeader(admin))
	if status != http.StatusOK {
		t.Fatalf("history 期望 200，实为 %d（msg=%s）", status, env.Msg)
	}
	total, _ := env.dataMap(t)["total"].(float64)
	return int64(total)
}

// insertSubjectCall 直插一条带内容维度的明细（history 只读库，筛选断言靠它）
func insertSubjectCall(t *testing.T, source, action, keyword, book, media string, resultCount, status int) {
	t.Helper()
	testkit.InsertSubjectCall(t, source, action, keyword, book, media, resultCount, status)
}

// TestMonitorSubjectsFromPipeline 真实调用驱动内容维度：搜索词、结果数、tab 声明的媒介、
// 以及「没声明就留在未判定桶」这条纪律
func TestMonitorSubjectsFromPipeline(t *testing.T) {
	srv := newTestServer(t)
	base.ResetMetrics()
	base.ResetNameCaches()
	admin := adminToken(t, srv)

	setPlatformUpstream(t, "fake_a", staticUpstream(t, searchEnvelope).URL)
	setPlatformUpstream(t, "fake_c", staticUpstream(t, plainEnvelope).URL)

	mustOK(t, srv, admin, "/fake_a/search")           // 无搜索词：媒介未判定，result_count=2
	mustOK(t, srv, admin, "/fake_a/search?key=测试词")   // 关键词入榜
	mustOK(t, srv, admin, "/fake_c/search?tabType=2") // fake_c 的 tab 2 声明 comic

	kw := subjectItems(t, srv, admin, map[string]string{"dim": "keyword"})
	if kw["测试词"] != 1 {
		t.Errorf("关键词榜「测试词」应 1 次，实得 %+v", kw)
	}
	if len(kw) != 1 {
		t.Errorf("关键词榜应只有 1 条（另两次调用没有搜索词），实得 %+v", kw)
	}

	media := subjectItems(t, srv, admin, map[string]string{"dim": "media"})
	if media[base.MediaComic] != 1 {
		t.Errorf("媒介榜应有 1 次 comic（来自 tab 声明，响应里没有类型），实得 %+v", media)
	}
	// fake_a 没声明媒介、响应也没类型：留在未判定的空桶，不该被算成小说
	if media[""] != 2 {
		t.Errorf("未判定的两次 fake_a 调用应留在空媒介桶（2 次），实得 %+v", media)
	}

	// 内存明细里应能看到内容维度
	_, env := doJSON(t, srv, "GET", "/admin/monitor", nil, authHeader(admin))
	recent, _ := env.dataMap(t)["recent"].([]interface{})
	var sawKeyword, sawCount bool
	for _, r := range recent {
		it, _ := r.(map[string]interface{})
		if it["keyword"] == "测试词" {
			sawKeyword = true
		}
		if c, _ := it["result_count"].(float64); c == 2 {
			sawCount = true
		}
	}
	if !sawKeyword || !sawCount {
		t.Errorf("/monitor recent 未带上内容维度（keyword=%v result_count=%v）", sawKeyword, sawCount)
	}
}

// TestMonitorSubjectMediaPriorityViaPipeline 响应自带的类型压过源默认声明：
// fake_b 声明了源默认 audio，而它这次的详情响应写的是「漫剧」（平台口径归 video）
func TestMonitorSubjectMediaPriorityViaPipeline(t *testing.T) {
	srv := newTestServer(t)
	base.ResetMetrics()
	base.ResetNameCaches()
	admin := adminToken(t, srv)

	setPlatformUpstream(t, "fake_b", staticUpstream(t, detailEnvelope).URL)
	mustOK(t, srv, admin, "/fake_b/detail")

	media := subjectItems(t, srv, admin, map[string]string{"dim": "media", "source": "fake_b"})
	if media[base.MediaVideo] != 1 {
		t.Errorf("响应自带（漫剧→video）应压过 fake_b 的源默认 audio，实得 %+v", media)
	}
	if len(media) != 1 {
		t.Errorf("fake_b 只该有 video 一桶，实得 %+v", media)
	}
	// 详情不再计入书名榜（口径见 subjectDims：book/chapter 只统计 content）
	book := subjectItems(t, srv, admin, map[string]string{"dim": "book", "source": "fake_b"})
	if _, ok := book["某剧"]; ok {
		t.Errorf("详情不该进书名榜，实得 %+v", book)
	}
}

// TestMonitorSubjectBookCountsContentOnly 书名榜与章节榜只统计正文接口：
// 同一本书的 detail/chapter（Legado 打开书目 + 分多页拉目录）不该把这本书凭空乘几倍。
func TestMonitorSubjectBookCountsContentOnly(t *testing.T) {
	srv := newTestServer(t)
	base.ResetMetrics()
	admin := adminToken(t, srv)

	insert := func(source, action, bookName, chapter string) {
		t.Helper()
		row := models.ApiCallLog{
			Username: "u1", IP: "127.0.0.1", Source: source, Action: action,
			Status: http.StatusOK, LatencyMs: 10, CreatedAt: time.Now(),
			BookName: bookName, ChapterTitle: chapter, Media: base.MediaNovel, ResultCount: 1,
		}
		if err := db.DB.Create(&row).Error; err != nil {
			t.Fatalf("写入调用明细失败: %v", err)
		}
	}
	// 一次阅读会话的真实形状：1 次详情 + 2 页目录 + 3 章正文
	insert("fake_a", "detail", "书名榜只数正文", "")
	insert("fake_a", "chapter", "书名榜只数正文", "")
	insert("fake_a", "chapter", "书名榜只数正文", "")
	insert("fake_a", "content", "书名榜只数正文", "第一章")
	insert("fake_a", "content", "书名榜只数正文", "第二章")
	insert("fake_a", "content", "书名榜只数正文", "第三章")

	bookReq := subjectItemsField(t, srv, admin, map[string]string{"dim": "book", "source": "fake_a"}, "requests")
	if got := bookReq["书名榜只数正文"]; got != 3 {
		t.Errorf("书名榜请求数应只算 3 次正文（detail 与两页目录不计），实得 %d", got)
	}
	// 排名数字按 P18 走人数：一个人读完三章 = 1 个访问者、3 次请求
	bookVis := subjectItemsField(t, srv, admin, map[string]string{"dim": "book", "source": "fake_a"}, "visitors")
	if got := bookVis["书名榜只数正文"]; got != 1 {
		t.Errorf("书名榜人数应只算 1 个访问者（u1 读了三章），实得 %d", got)
	}
	if got := subjectItems(t, srv, admin, map[string]string{"dim": "book", "source": "fake_a"})["书名榜只数正文"]; got != 1 {
		t.Errorf("书名榜 total 应等于人数口径（1），实得 %d", got)
	}
	chap := subjectItems(t, srv, admin, map[string]string{"dim": "chapter", "source": "fake_a"})
	for _, name := range []string{"第一章", "第二章", "第三章"} {
		if chap[name] != 1 {
			t.Errorf("章节榜缺少 %s 或计数不为 1，实得 %+v", name, chap)
		}
	}
	// 媒介维度不受动作限制：6 条明细都该算进 novel 桶
	if got := subjectItems(t, srv, admin, map[string]string{"dim": "media", "source": "fake_a"})[base.MediaNovel]; got != 6 {
		t.Errorf("媒介维度不该被动作过滤，应得 6，实得 %d", got)
	}
}

// TestMonitorHistoryContentFilters 历史明细的内容维度筛选（含 LIKE 通配符转义）
func TestMonitorHistoryContentFilters(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)

	insertSubjectCall(t, "fake_a", "search", "100%纯棉", "测试书", base.MediaNovel, 3, http.StatusOK)
	insertSubjectCall(t, "fake_a", "search", "另寻一本", "另一本书", base.MediaAudio, 0, http.StatusOK)
	insertSubjectCall(t, "fake_a", "content", "", "测试书", "", 1, http.StatusForbidden)

	// 转义生效的判据：传入裸通配符「%」只该匹配**字面含百分号**的那条（100%纯棉），
	// 若 ESCAPE 没起作用，模式会退化成 %%% 把 3 条全捞出来
	if n := historyTotal(t, srv, admin, map[string]string{"keyword": "%"}); n != 1 {
		t.Errorf("转义后传入百分号只应命中字面带百分号的 1 条，实为 %d 条", n)
	}
	if n := historyTotal(t, srv, admin, map[string]string{"keyword": "100%"}); n != 1 {
		t.Errorf("传入带百分号的搜索词原文应精确命中 1 条，实为 %d", n)
	}
	if n := historyTotal(t, srv, admin, map[string]string{"book_name": "测试书"}); n != 2 {
		t.Errorf("book_name contains「测试书」应命中 2 条，实为 %d", n)
	}
	if n := historyTotal(t, srv, admin, map[string]string{"media_type": base.MediaAudio}); n != 1 {
		t.Errorf("media_type=audio 应命中 1 条，实为 %d", n)
	}
	if n := historyTotal(t, srv, admin, map[string]string{"action": "content"}); n != 1 {
		t.Errorf("action=content 应命中 1 条，实为 %d", n)
	}

	// 榜单的空值纪律：媒介未判定的那条不进书名/关键词榜（它们本就只算有值的），
	// 但留在 media 的空桶里
	merged := subjectItems(t, srv, admin, map[string]string{"dim": "media"})
	if merged[""] < 1 {
		t.Errorf("未判定媒介应成桶，实得 %+v", merged)
	}
}

func TestMonitorSubjectsValidation(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)

	status, _ := doJSON(t, srv, "GET", "/admin/monitor/subjects"+buildQuery(map[string]string{"dim": "nope"}), nil, authHeader(admin))
	if status != http.StatusBadRequest {
		t.Errorf("非法 dim 期望 400，实为 %d", status)
	}
}
