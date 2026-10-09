package groupb

// 待办清单 P118 ③：`/rank/boards` 原来零缓存——每次请求真算两趟主聚合（现网 p50 522ms / p90 1.49s）。
// 现在在**出口层**缓存那份要发出去的行，TTL 由 `RANK_CACHE_SEC` 决定。
//
// 断言为什么走「看得见的陈旧」而不是「数 SQL 条数」：本包那两条榜用例的计数器
// （`newCallLogCounter`）注册在 `gorm:query` 之后，而 **GORM 的 `Scan` 收尾不跑 query 回调链**——
// 榜单主聚合正是 `Scan` 收尾。探针实测：同一棵测试库上 `First` 收尾被数到、`Scan` 收尾报 0 条。
// 用那条通路数出来的 0 不是「没查库」，是「没数到」——拿它当缓存命中的证据就是断言空转。
// 所以这里钉的是**行为**：命中之后新写入的行看不见、换窗口看得见、关掉缓存立刻看得见。
//
// 四条用例各钉一件事：
//   ① 命中不重算（新写的词在 TTL 内不可见）+ 响应如实带 `cache_ttl_sec` + **换窗口不许复用旧格**；
//   ② 键里必须有「这条响应属于谁」那一段（P84 那一族）：管理员那次是「不限源」的合并结果，
//      少 `admin`/`allow` 任一段，未开放源的热度就发给普通用户——本文件里最贵的一条；
//   ③ `RANK_CACHE_SEC<=0` 回到每次真算（配置不许只写不读，AGENTS §10）；
//   ④ `sources`/`medias` 不进缓存：改名或下架一个源，下拉框立刻跟着现实走，
//      陈旧只许发生在榜本身的计数上。它失败说明有人把整个响应体塞进缓存了。

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"loomproxy/base"
	"loomproxy/conf"
	"loomproxy/db"
	"loomproxy/handlers/rank"
	"loomproxy/models"
	"loomproxy/utils"
)

// rankBoard 发一次榜并抽出两张榜合起来的条目名（外加 data 那层，读 cache_ttl_sec / sources 用）
func rankBoard(t *testing.T, srv *httptest.Server, token, path string) (int, map[string]bool, map[string]interface{}) {
	t.Helper()
	status, env := doJSON(t, srv, http.MethodGet, path, nil, authHeader(token))
	if status != http.StatusOK {
		return status, nil, nil
	}
	data := env.dataMap(t)
	names := map[string]bool{}
	list, _ := data["boards"].([]interface{})
	for _, raw := range list {
		b, _ := raw.(map[string]interface{})
		rows, _ := b["rows"].([]interface{})
		for _, r := range rows {
			it, _ := r.(map[string]interface{})
			if nm, _ := it["name"].(string); nm != "" {
				names[nm] = true
			}
		}
	}
	return status, names, data
}

// isolateRankCache 把 TTL 与缓存格收到本用例手里：前后各清一次（缓存是进程级单例，同树用例共用）。
// **走 DelPrefix(`rank.BoardsCachePrefix`) 而不是在测试里抄键的拼法**——
// 拼法长出第二份，用例就会去验一个不存在的东西（P39 那一族），所以前缀段由产品自己导出。
func isolateRankCache(t *testing.T, sec int) {
	t.Helper()
	prev := conf.Config.RankCacheSec
	conf.Config.RankCacheSec = sec
	utils.DefaultCache().DelPrefix(rank.BoardsCachePrefix)
	t.Cleanup(func() {
		conf.Config.RankCacheSec = prev
		utils.DefaultCache().DelPrefix(rank.BoardsCachePrefix)
	})
}

// openRankTo 按源开放公开榜（管理员唯一的入口就是这条设置项）
func openRankTo(t *testing.T, srv *httptest.Server, admin, value string) {
	t.Helper()
	status, env := doJSON(t, srv, http.MethodPut, "/admin/settings/rank_public_sources",
		map[string]string{"value": value}, authHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("开放公开榜失败: status=%d msg=%s", status, env.Msg)
	}
}

func TestRankBoardsCacheHitKeepsNewRowsOut(t *testing.T) {
	srv := newTestServer(t)
	base.ResetMetrics()
	admin := adminToken(t, srv)
	user := registerUser(t, srv, "p118c_u1", "p118c_u1@example.com", "pass1234")
	isolateRankCache(t, 60)
	openRankTo(t, srv, admin, "fake_a,fake_b")

	insertSubjectCall(t, "fake_a", "search", "缓存甲词", "", base.MediaNovel, 1, http.StatusOK)

	// 第一次没有格：必须看得见当下已落库的行
	status, names, data := rankBoard(t, srv, user, "/rank/boards?days=7")
	if status != http.StatusOK {
		t.Fatalf("普通用户读公开榜应 200，实为 %d", status)
	}
	if !names["缓存甲词"] {
		t.Fatalf("第一次请求应看到「缓存甲词」，实得 %+v——这条用例的前提没了", names)
	}
	if got := data["cache_ttl_sec"]; got != float64(60) {
		t.Errorf("响应应如实带出 cache_ttl_sec=60（陈旧窗口由服务端说，不让面板自己抄——与 P46 的 filters_meta 同一口径），实得 %v", got)
	}

	// 第二次命中那一格：新写入的行在 TTL 内不该出现
	insertSubjectCall(t, "fake_a", "search", "缓存乙词", "", base.MediaNovel, 1, http.StatusOK)
	status, names, _ = rankBoard(t, srv, user, "/rank/boards?days=7")
	if status != http.StatusOK {
		t.Fatalf("第二次应 200，实为 %d", status)
	}
	if !names["缓存甲词"] {
		t.Errorf("命中的那一格应仍带「缓存甲词」，实得 %+v", names)
	}
	if names["缓存乙词"] {
		t.Errorf("第二次请求看见了新写入的「缓存乙词」——这一格没命中（缓存没生效，或写入侧根本没写）")
	}

	// 换一个窗口就是另一个总体：days 与窗口起点都在键里，跨档不许复用
	status, names3, _ := rankBoard(t, srv, user, "/rank/boards?days=1")
	if status != http.StatusOK {
		t.Fatalf("days=1 那次应 200，实为 %d", status)
	}
	if !names3["缓存乙词"] {
		t.Errorf("days=1 与 days=7 不许共用一格：换了窗口还看不见「缓存乙词」，说明键里少了窗口那一段（实得 %+v）", names3)
	}

	// 加一个媒介筛选同样是另一个总体（明细都带 novel，所以换档必须重算才看得见新词）
	status, names4, _ := rankBoard(t, srv, user, "/rank/boards?days=7&media=novel")
	if status != http.StatusOK {
		t.Fatalf("media=novel 那次应 200，实为 %d", status)
	}
	if !names4["缓存乙词"] {
		t.Errorf("带 media 与不带 media 不许共用一格：换了筛选还看不见「缓存乙词」，说明键里少了 media 那一段（实得 %+v）", names4)
	}
}

func TestRankBoardsCacheKeyCarriesIdentity(t *testing.T) {
	srv := newTestServer(t)
	base.ResetMetrics()
	admin := adminToken(t, srv)
	user := registerUser(t, srv, "p118c_u2", "p118c_u2@example.com", "pass1234")
	isolateRankCache(t, 60)
	openRankTo(t, srv, admin, "fake_a") // 只开放 fake_a

	// fake_a 与 fake_b 各一条搜索词：管理员那次「不限源」的合并结果里带着未开放那个源的词
	insertSubjectCall(t, "fake_a", "search", "身份键甲词", "", base.MediaNovel, 1, http.StatusOK)
	insertSubjectCall(t, "fake_b", "search", "身份键乙源词", "", base.MediaNovel, 1, http.StatusOK)

	status, adminNames, _ := rankBoard(t, srv, admin, "/rank/boards?days=7")
	if status != http.StatusOK {
		t.Fatalf("管理员读榜应 200，实为 %d", status)
	}
	if !adminNames["身份键乙源词"] {
		t.Fatalf("管理员那次是不限源的合并统计，应看到「身份键乙源词」，实得 %+v——这条用例的前提没了", adminNames)
	}

	// 普通用户同一路径、同一窗口：键里少了 allow/admin 任一段，这里就会命中管理员那一格
	status, userNames, _ := rankBoard(t, srv, user, "/rank/boards?days=7")
	if status != http.StatusOK {
		t.Fatalf("普通用户读榜应 200，实为 %d", status)
	}
	if userNames["身份键乙源词"] {
		t.Errorf("未开放的 fake_b 的词出现在普通用户的榜上——缓存键少了「这条响应属于谁」那一段（P84 那一族：少一段不是命中率低，是发错人）")
	}
	if !userNames["身份键甲词"] {
		t.Errorf("普通用户应看到已开放源 fake_a 的「身份键甲词」，实得 %+v", userNames)
	}
}

func TestRankBoardsCacheOffWhenTTLNonPositive(t *testing.T) {
	srv := newTestServer(t)
	base.ResetMetrics()
	admin := adminToken(t, srv)
	user := registerUser(t, srv, "p118c_u3", "p118c_u3@example.com", "pass1234")
	isolateRankCache(t, 0) // 0 = 每次真算（旧行为）
	openRankTo(t, srv, admin, "fake_a")

	insertSubjectCall(t, "fake_a", "search", "关缓存甲词", "", base.MediaNovel, 1, http.StatusOK)
	status, names, data := rankBoard(t, srv, user, "/rank/boards?days=7")
	if status != http.StatusOK {
		t.Fatalf("应 200，实为 %d", status)
	}
	if !names["关缓存甲词"] {
		t.Fatalf("第一次应看到「关缓存甲词」，实得 %+v", names)
	}
	if got := data["cache_ttl_sec"]; got != float64(0) {
		t.Errorf("TTL=0 时响应应说 cache_ttl_sec=0（不缓存就别声称有陈旧窗口），实得 %v", got)
	}

	// 关掉缓存之后，新写入必须下一次请求就看得见
	insertSubjectCall(t, "fake_a", "search", "关缓存乙词", "", base.MediaNovel, 1, http.StatusOK)
	_, names2, _ := rankBoard(t, srv, user, "/rank/boards?days=7")
	if !names2["关缓存乙词"] {
		t.Errorf("TTL=0 时第二次请求应立刻看到新明细「关缓存乙词」，实得 %+v（看不见 = 缓存其实还开着）", names2)
	}
}

func TestRankBoardsSourceListStaysLive(t *testing.T) {
	srv := newTestServer(t)
	base.ResetMetrics()
	admin := adminToken(t, srv)
	user := registerUser(t, srv, "p118c_u4", "p118c_u4@example.com", "pass1234")
	isolateRankCache(t, 60)
	openRankTo(t, srv, admin, "fake_a")

	insertSubjectCall(t, "fake_a", "search", "活列表甲词", "", base.MediaNovel, 1, http.StatusOK)
	if status, _, _ := rankBoard(t, srv, user, "/rank/boards?days=7"); status != http.StatusOK {
		t.Fatalf("第一次应 200，实为 %d", status)
	}

	// 榜的行进缓存，源的展示名不进。原名先读出来，还原时不靠手抄
	var before models.DataSource
	if err := db.DB.Where("name = ?", "fake_a").First(&before).Error; err != nil {
		t.Fatalf("data_sources 里没有 fake_a 这一行（%v）——这一格断言失去对象，要么改用例要么承认设计变了", err)
	}
	if err := db.DB.Model(&models.DataSource{}).Where("name = ?", "fake_a").
		Update("display_name", "改名后的甲源").Error; err != nil {
		t.Fatalf("改 fake_a 的展示名失败: %v", err)
	}
	t.Cleanup(func() {
		if err := db.DB.Model(&models.DataSource{}).Where("name = ?", "fake_a").
			Update("display_name", before.DisplayName).Error; err != nil {
			t.Errorf("还原 fake_a 的展示名失败: %v", err)
		}
	})
	// 同时补一条明细：第二次若连它也看见，那就是没命中，本用例「陈旧只发生在榜上」的前提就没了
	insertSubjectCall(t, "fake_a", "search", "活列表乙词", "", base.MediaNovel, 1, http.StatusOK)

	status, names, data := rankBoard(t, srv, user, "/rank/boards?days=7")
	if status != http.StatusOK {
		t.Fatalf("第二次应 200，实为 %d", status)
	}
	if names["活列表乙词"] {
		t.Fatalf("第二次本该命中那一格（新写的词在 TTL 内不可见），实得 %+v——不命中就测不到「陈旧只发生在榜上」这件事", names)
	}
	list, _ := data["sources"].([]interface{})
	var sawRenamed bool
	for _, raw := range list {
		o, _ := raw.(map[string]interface{})
		if o["name"] == "改名后的甲源" {
			sawRenamed = true
		}
	}
	if !sawRenamed {
		t.Errorf("sources 列表应每次现取（改名或下架一个源不该跟着榜陈旧一个 TTL），实得 %+v", data["sources"])
	}
}
