package test

// 隐私协议（阅读数据留存的同意位）端到端用例。
//
// 钉住四件事，每一件都是「不写用例就没人拦得住」的形状：
//   - 默认档是同意，且**存量用户（列为 NULL）也读成同意**——不是一次回填数据才能成立；
//   - 关闭后内容维度七个字段全空（含标识），请求本身照常成功、计数不受影响；
//   - 关闭的状态能扛过名称回填循环（回填只认标识，所以标识必须一起清）；
//   - 这条端点只认会话：长期密钥改不动本人对网关的授权。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"loomproxy/base"
	"loomproxy/db"
	"loomproxy/handlers/subjectrank"
	"loomproxy/models"
)

var privacySeq atomic.Int64

// privacyUser 注册一个带独立邮箱的用户，返回会话 token 与用户名
func privacyUser(t *testing.T, srv *httptest.Server) (string, string) {
	t.Helper()
	name := fmt.Sprintf("pv_%d", privacySeq.Add(1))
	token := registerUser(t, srv, name, name+"@example.com", "pass1234")
	return token, name
}

func setPrivacyConsent(t *testing.T, srv *httptest.Server, token string, consent bool) map[string]interface{} {
	t.Helper()
	status, env := doJSON(t, srv, http.MethodPost, "/auth/privacy",
		map[string]interface{}{"content_consent": consent}, authHeader(token))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("POST /auth/privacy(%v) 失败: status=%d code=%d msg=%s", consent, status, env.Code, env.Msg)
	}
	return env.dataMap(t)
}

// dimensionsOf 找出一条已记录明细里的内容维度（按 源+接口 匹配，取最新那条）
func dimensionsOf(rows []base.RecentCall, source, action string) (base.RecentCall, bool) {
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Source == source && rows[i].Action == action {
			return rows[i], true
		}
	}
	return base.RecentCall{}, false
}

func claimDims(t *testing.T, rc base.RecentCall, label string) {
	t.Helper()
	if rc.Keyword != "" || rc.BookName != "" || rc.ChapterTitle != "" ||
		rc.BookIdent != "" || rc.ChapterIdent != "" || rc.Media != "" || rc.ResultCount != 0 {
		t.Errorf("%s: 内容维度没被清空——keyword=%q book=%q chapter=%q book_ident=%q chapter_ident=%q media=%q result_count=%d",
			label, rc.Keyword, rc.BookName, rc.ChapterTitle, rc.BookIdent, rc.ChapterIdent, rc.Media, rc.ResultCount)
	}
}

// TestPrivacyConsentDefaultsToKeep 没表过态 = 同意（默认档），内容维度照旧捕获。
// 这条同时是反向守卫：万一默认档被写成「默认不同意」，采集会静默停摆，榜与覆盖率一起空掉。
func TestPrivacyConsentDefaultsToKeep(t *testing.T) {
	srv := newTestServer(t)
	base.ResetMetrics()
	base.ResetNameCaches()

	token, name := privacyUser(t, srv)
	setPlatformUpstream(t, "fake_a", staticUpstream(t, searchEnvelope).URL)
	mustOK(t, srv, token, "/fake_a/search?query=默认档搜的词")

	var u models.User
	if err := db.DB.Where(map[string]interface{}{"username": name}).First(&u).Error; err != nil {
		t.Fatalf("读回用户失败: %v", err)
	}
	if u.ContentConsent != nil {
		t.Errorf("未表态的用户同意位 = %v, want NULL（默认档不该靠一次数据回填写出来）", *u.ContentConsent)
	}
	if !u.KeepsContentData() {
		t.Error("NULL 读成了不同意：默认档是同意")
	}

	rc, ok := dimensionsOf(base.DrainRecentCalls(), "fake_a", "search")
	if !ok {
		t.Fatal("没找到 fake_a/search 的明细")
	}
	if rc.Keyword != "默认档搜的词" {
		t.Errorf("默认档没捕获搜索词: keyword=%q", rc.Keyword)
	}
	if rc.ResultCount == 0 || rc.ContentWithheld {
		t.Errorf("默认档不该标记为不捕获: result_count=%d withheld=%v", rc.ResultCount, rc.ContentWithheld)
	}
}

// TestPrivacyConsentOffWithholdsDimensions 关闭后：搜索、详情两类调用的内容维度全空，
// 但请求成功、调用计数不受影响——用户关掉的是「留不留我读什么」，不是「还能不能用」。
func TestPrivacyConsentOffWithholdsDimensions(t *testing.T) {
	srv := newTestServer(t)
	base.ResetMetrics()
	base.ResetNameCaches()

	token, _ := privacyUser(t, srv)
	setPlatformUpstream(t, "fake_a", staticUpstream(t, searchEnvelope).URL)
	setPlatformUpstream(t, "fake_b", staticUpstream(t, detailEnvelope).URL)

	data := setPrivacyConsent(t, srv, token, false)
	if data["content_consent"] != false {
		t.Fatalf("同意位回写读数 = %v, want false", data["content_consent"])
	}

	mustOK(t, srv, token, "/fake_a/search?query=关闭后的词")
	mustOK(t, srv, token, "/fake_b/detail?bookId=bk9")

	rows := base.DrainRecentCalls()
	search, ok := dimensionsOf(rows, "fake_a", "search")
	if !ok {
		t.Fatal("关闭后 fake_a/search 的明细整条没了——计数口径不能被一起抹掉")
	}
	claimDims(t, search, "关闭后的 search")
	if search.Status != http.StatusOK || search.LatencyMs < 0 {
		t.Errorf("请求读数受损: status=%d", search.Status)
	}
	if !search.ContentWithheld {
		t.Error("没打 content_withheld：退出用户的「全空」与「采集在漏」就分不开了")
	}

	detail, ok := dimensionsOf(rows, "fake_b", "detail")
	if !ok {
		t.Fatal("没找到 fake_b/detail 的明细")
	}
	claimDims(t, detail, "关闭后的 detail")
	if !detail.ContentWithheld {
		t.Error("detail 未标记不捕获")
	}

	// 榜必须不计入：判据是维度非空（OnlyPresent），不是再加一道显式过滤——
	// 「留空即不入榜」这条要是有天被改动，这里就会红
	kw := subjectItems(t, srv, adminToken(t, srv), map[string]string{"dim": "keyword"})
	if _, hit := kw["关闭后的词"]; hit {
		t.Errorf("退出用户的搜索词进了公开榜: %+v", kw)
	}
}

// TestPrivacyConsentOnAgainResumes 重新同意后新调用恢复捕获（开关不是单向棘轮）
func TestPrivacyConsentOnAgainResumes(t *testing.T) {
	srv := newTestServer(t)
	base.ResetMetrics()
	base.ResetNameCaches()

	token, _ := privacyUser(t, srv)
	setPlatformUpstream(t, "fake_a", staticUpstream(t, searchEnvelope).URL)

	setPrivacyConsent(t, srv, token, false)
	mustOK(t, srv, token, "/fake_a/search?query=关掉期间的词")
	if rc, ok := dimensionsOf(base.DrainRecentCalls(), "fake_a", "search"); ok {
		claimDims(t, rc, "关掉期间")
	} else {
		t.Fatal("没记到明细")
	}

	setPrivacyConsent(t, srv, token, true)
	mustOK(t, srv, token, "/fake_a/search?query=重新打开后的词")
	rc, ok := dimensionsOf(base.DrainRecentCalls(), "fake_a", "search")
	if !ok {
		t.Fatal("没找到重新打开后的明细")
	}
	if rc.Keyword != "重新打开后的词" || rc.ContentWithheld {
		t.Errorf("重新同意没恢复捕获: keyword=%q withheld=%v", rc.Keyword, rc.ContentWithheld)
	}
}

// TestPrivacyWithheldRowsSurviveBackfill 关闭状态要能扛过名称回填循环。
//
// 回填按 (source, 标识) 反查缓存补 book_name/chapter_title，判据是「名称为空」。
// 所以只清名称、留着标识 = 当场说不捕获、下一轮回填又给自己补上。
//
// 分两半，缺一不可：**先走真管道**证明标识在同意时确实落下、关闭时真的没落
// （上一版只手工插行，把「忘了清标识」这个变异测成了绿——插进去的行本来就没标识）；
// **再做库内对照**证明「没有标识」确实让回填填不上（正常行被补上，退出用户的行保持空）。
func TestPrivacyWithheldRowsSurviveBackfill(t *testing.T) {
	srv := newTestServer(t)
	base.ResetMetrics()
	base.ResetNameCaches()
	admin := adminToken(t, srv)

	setPlatformUpstream(t, "fake_a", staticUpstream(t, searchEnvelope).URL)
	setPlatformUpstream(t, "fake_b", staticUpstream(t, detailEnvelope).URL)

	// ① 真管道：同意时正文调用会落标识，关闭后七个维度（含两个标识）全空
	keepToken, _ := privacyUser(t, srv)
	mustOK(t, srv, keepToken, "/fake_a/content?bookId=bk9&itemId=c1")
	rcKeep, ok := dimensionsOf(base.DrainRecentCalls(), "fake_a", "content")
	if !ok {
		t.Fatal("没找到同意用户的正文调用明细")
	}
	if rcKeep.BookIdent == "" || rcKeep.ChapterIdent == "" {
		t.Fatalf("前置条件不成立：同意用户的正文调用没落标识（book=%q chapter=%q）——那下面那条断言就是空的",
			rcKeep.BookIdent, rcKeep.ChapterIdent)
	}

	offToken, _ := privacyUser(t, srv)
	setPrivacyConsent(t, srv, offToken, false)
	mustOK(t, srv, offToken, "/fake_a/content?bookId=bk9&itemId=c9")
	rcOff, ok := dimensionsOf(base.DrainRecentCalls(), "fake_a", "content")
	if !ok {
		t.Fatal("没找到退出用户的正文调用明细")
	}
	claimDims(t, rcOff, "退出用户的正文调用")
	if rcOff.BookIdent != "" || rcOff.ChapterIdent != "" {
		t.Fatalf("标识没被清掉（book=%q chapter=%q）：回填会在下一轮把书名与章节名补回来，开关形同虚设",
			rcOff.BookIdent, rcOff.ChapterIdent)
	}

	// ② 库内对照：回填只认标识，没有标识的行必然填不上
	mustOK(t, srv, admin, "/fake_b/detail?bookId=bk9") // 喂命名缓存：bk9 → 「某剧」
	if err := db.DB.Create(&[]models.ApiCallLog{
		{Username: "bf_normal", IP: "127.0.0.1", Source: "fake_b", Action: "content",
			Status: http.StatusOK, LatencyMs: 5, CreatedAt: time.Now(), BookIdent: "bk9"},
		{Username: "bf_withheld", IP: "127.0.0.1", Source: "fake_b", Action: "content",
			Status: http.StatusOK, LatencyMs: 5, CreatedAt: time.Now(), ContentWithheld: true},
	}).Error; err != nil {
		t.Fatalf("插入对照明细失败: %v", err)
	}

	_, env := doJSON(t, srv, http.MethodPost, "/admin/monitor/backfill-subjects?days=7", nil, authHeader(admin))
	if filled, _ := env.dataMap(t)["book_filled"].(float64); filled < 1 {
		t.Fatalf("对照组没被回填（book_filled=%v），这条用例就失去意义了", filled)
	}

	var normal []models.ApiCallLog
	db.DB.Where("username = ? AND source = ?", "bf_normal", "fake_b").Find(&normal)
	if len(normal) == 0 || normal[0].BookName == "" {
		t.Errorf("带标识的正常行没被回填: %+v", normal)
	}
	var withheld []models.ApiCallLog
	db.DB.Where("username = ? AND source = ?", "bf_withheld", "fake_b").Find(&withheld)
	for _, r := range withheld {
		if r.BookName != "" || r.BookIdent != "" {
			t.Errorf("退出用户的行被回填了（ident=%q name=%q）", r.BookIdent, r.BookName)
		}
	}
}

// TestPrivacyEndpointIsSessionOnly 长期密钥改不动本人的同意位。
// 没加进 three_forms_auth_test.go 的那张表：那里每条用例都要用会话再走一遍 200，
// 而本端点必须带 body，表里的会话分支是无体的，形状对不上会假红。
func TestPrivacyEndpointIsSessionOnly(t *testing.T) {
	srv := newTestServer(t)
	token, key := privacyUser(t, srv)

	for _, f := range []struct {
		name    string
		headers map[string]string
		query   string
	}{
		{"X-API-Key", map[string]string{"X-API-Key": key}, ""},
		{"?api_key=", nil, "?api_key=" + key},
	} {
		status, raw := doRaw(t, srv, http.MethodPost, "/auth/privacy"+f.query,
			map[string]interface{}{"content_consent": false}, f.headers)
		if status == http.StatusOK {
			t.Errorf("用 %s 竟然改成了同意位——密钥泄露时攻击者能让受害者的阅读记录重新开始被采集，而会话列表看不见（body=%s）",
				f.name, truncate(string(raw), 120))
		}
		if status != http.StatusUnauthorized && status != http.StatusForbidden {
			t.Errorf("用 %s status = %d, want 401/403", f.name, status)
		}
	}

	// 同一端点用会话必须可用（否则上面的「拒绝」可能是守卫整个坏了）
	setPrivacyConsent(t, srv, token, false)

	// 缺字段不算「不改」：必须报 400，不能让一次畸形请求静默当成同意
	status, env := doJSON(t, srv, http.MethodPost, "/auth/privacy", map[string]interface{}{}, authHeader(token))
	if status == http.StatusOK || env.Code == 0 {
		t.Errorf("缺 content_consent 竟然通过：status=%d code=%d msg=%s", status, env.Code, env.Msg)
	}
}

// TestPrivacyPromisePublicRankCarriesNoIdentity 承诺的后半句要有用例守着：
// 「只用于排行榜展示、只展示书名和搜索词、不泄露其他用户信息」。
// 白名单在 handlers/rank/rank.go 里是写死的两维，但**写死不等于守得住**——
// 以后有人往 boards 里加一列（哪怕"只是多个字段"），这条会立刻红。
func TestPrivacyPromisePublicRankCarriesNoIdentity(t *testing.T) {
	srv := newTestServer(t)
	base.ResetMetrics()
	base.ResetNameCaches()
	admin := adminToken(t, srv)

	if st, env := doJSON(t, srv, http.MethodPut, "/admin/settings/rank_public_sources",
		map[string]string{"value": "fake_a"}, authHeader(admin)); st != http.StatusOK || env.Code != 0 {
		t.Fatalf("放行 fake_a 进公开榜失败: status=%d msg=%s", st, env.Msg)
	}
	db.InvalidateSettingCache("rank_public_sources")

	token, name := privacyUser(t, srv)
	setPlatformUpstream(t, "fake_a", staticUpstream(t, searchEnvelope).URL)
	mustOK(t, srv, token, "/fake_a/search?query=排行榜该出现的词")

	status, raw := doRaw(t, srv, http.MethodGet, "/rank/boards?days=7", nil, authHeader(token))
	if status != http.StatusOK {
		t.Fatalf("GET /rank/boards status = %d, want 200（body=%s）", status, truncate(string(raw), 200))
	}
	body := string(raw)
	if !strings.Contains(body, "排行榜该出现的词") {
		t.Errorf("公开榜里没有这次搜索词，后面的断言就成了空的：%s", truncate(body, 200))
	}
	for _, leak := range []string{name, "@example.com", "username", `"ip"`, "last_called_at", "latency"} {
		if strings.Contains(body, leak) {
			t.Errorf("公开榜响应里出现了 %q——承诺是「不泄露其他用户信息」，身份与时间戳都不能出这个端点", leak)
		}
	}

	var payload struct {
		Data struct {
			Boards []struct {
				Dim  string                   `json:"dim"`
				Rows []map[string]interface{} `json:"rows"`
			} `json:"boards"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("解析公开榜失败: %v（body=%s）", err, truncate(string(raw), 200))
	}
	boards := payload.Data.Boards
	if len(boards) == 0 {
		t.Fatal("公开榜一张都没有，这条用例就是空的")
	}
	allowedDim := map[string]bool{"keyword": true, "book": true}
	allowedKey := map[string]bool{"name": true, "total": true, "book_id": true, "unit": true, "metric": true}
	for _, b := range boards {
		if !allowedDim[b.Dim] {
			t.Errorf("公开榜多出维度 %q（白名单只有 keyword/book）", b.Dim)
		}
		for _, r := range b.Rows {
			for k := range r {
				if !allowedKey[k] {
					t.Errorf("榜条目 %s 带了未登记字段 %q", b.Dim, k)
				}
			}
		}
	}
}

// TestPrivacyCoverageIgnoresWithheldRows 关闭留存不该把覆盖率拖下去：
// 那些行是用户选择不留，不是采集在漏。两者混在一个分母里，下一个读数字的人会去修一个不存在的问题
// （P19 那条判据的反方向：读数要自己分得开成因）。
//
// 走 subjectrank.Coverage 直接断言结构体（本仓覆盖率用例的既有写法），
// 行数各 5 条是必要的：后端对少于 5 行的组合不出数。
func TestPrivacyCoverageIgnoresWithheldRows(t *testing.T) {
	newTestServer(t) // 只为建库与播种：这条用例走 Coverage 直查，不需要 HTTP 服务
	var logs []models.ApiCallLog
	for i := 0; i < 5; i++ {
		logs = append(logs, models.ApiCallLog{
			Username: "cov_keep", IP: "127.0.0.1", Source: "fake_a", Action: "search",
			Status: http.StatusOK, LatencyMs: 5, CreatedAt: time.Now(),
			Keyword: fmt.Sprintf("正常样本%d", i), ResultCount: 2,
		})
	}
	for i := 0; i < 5; i++ {
		logs = append(logs, models.ApiCallLog{
			Username: "cov_off", IP: "127.0.0.1", Source: "fake_a", Action: "search",
			Status: http.StatusOK, LatencyMs: 5, CreatedAt: time.Now(),
			ContentWithheld: true,
		})
	}
	if err := db.DB.Create(&logs).Error; err != nil {
		t.Fatalf("插入对照明细失败: %v", err)
	}

	rows, err := subjectrank.Coverage(1, "fake_a", nil)
	if err != nil {
		t.Fatalf("Coverage 查询失败: %v", err)
	}
	got := coverageRow(t, rows, "fake_a", "search")
	if got.Rows != 5 {
		t.Errorf("rows = %d, want 5（退出用户的 5 行不该进分母）", got.Rows)
	}
	if got.Withheld != 5 {
		t.Errorf("withheld = %d, want 5（它们要单独报得出来，不能悄悄消失）", got.Withheld)
	}
	if got.HasKeyword != 5 {
		t.Errorf("has_keyword = %d, want 5——覆盖率被用户的合规选择拉低就是误报", got.HasKeyword)
	}
	if got.Failed != 0 {
		t.Errorf("failed = %d, want 0（退出行的 status 也是 200，但同样不该进这一组读数）", got.Failed)
	}
}
