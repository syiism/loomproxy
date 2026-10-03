package test

// 聚合搜索（sources 参数）与搜索响应的 source/kind 打标用例。
//
// 钉住三条不写用例就没人拦得住的东西：
//   - source 由**平台**硬性填、覆盖源自报的值（下游拿它路由详情/正文，填错一个字整条链就断）；
//   - 扇出的每个源都要过与中间件同一套的访问与额度判定，并且**各扣一次**
//     （少一刀就是「一次请求换 N 个上游」的绕过通道；多一刀就是入口那个源被扣两次）；
//   - 单个目标源失败不拖垮整条聚合，也不把上游的错误文案透给下游。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"loomproxy/base/legado"
	"loomproxy/conf"
	"loomproxy/db"
	"loomproxy/gate"
	"loomproxy/models"
)

var aggSeq atomic.Int64

type aggItem struct {
	BookId string `json:"bookId"`
	Name   string `json:"name"`
	Kind   interface{}
	Source string `json:"source"`
}

type aggBody struct {
	BookList      []aggItem              `json:"bookList"`
	SourcesStatus map[string]string      `json:"sources_status"`
	Extra         map[string]interface{} `json:"-"`
}

// doGetAgg 走数据面原始响应（搜索体不是 {code,msg,data} 信封，不能用 doJSON）
func doGetAgg(t *testing.T, srv *httptest.Server, token, path string) (int, aggBody, string) {
	t.Helper()
	status, raw := doRaw(t, srv, http.MethodGet, path, nil, authHeader(token))
	var body aggBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("解析搜索响应失败: %v（body=%s）", err, truncate(string(raw), 300))
	}
	return status, body, string(raw)
}

// aggUser 注册一个独立用户（free 套餐），返回会话 token、用户名与用户 id
func aggUser(t *testing.T, srv *httptest.Server) (string, uint) {
	t.Helper()
	name := fmt.Sprintf("agg_%d", aggSeq.Add(1))
	token := registerUser(t, srv, name, name+"@example.com", "pass1234")
	return token, userIDByName(t, name)
}

// setAggCost 给某源的 search 配单价（默认播种是免费，不进扣减分支就等于没测）
func setAggCost(t *testing.T, source string, cost int) {
	t.Helper()
	q := map[string]interface{}{"group_code": source, "interface": "search"}
	var n int64
	db.DB.Model(&models.QuotaCost{}).Where(q).Count(&n)
	if n == 0 {
		if err := db.DB.Create(&models.QuotaCost{GroupCode: source, Interface: "search", Cost: int64(cost), Status: 1}).Error; err != nil {
			t.Fatalf("写入 %s/search 单价失败: %v", source, err)
		}
	} else if err := db.DB.Model(&models.QuotaCost{}).Where(q).
		// 速率字段清零：本文件一次请求要打多个源，别让限流先替我决定谁进得来
		Updates(map[string]interface{}{"cost": int64(cost), "status": 1, "interval": 0, "limit_count": 0, "window_sec": 0}).Error; err != nil {
		t.Fatalf("改 %s/search 单价失败: %v", source, err)
	}
	delCostCache(source, "search")
}

func aggDeductions(t *testing.T, uid uint, source string) int64 {
	t.Helper()
	var n int64
	if err := db.DB.Model(&models.QuotaUsageLog{}).
		Where("user_id = ? AND group_code = ? AND interface = ?", uid, source, "search").
		Count(&n).Error; err != nil {
		t.Fatalf("查 %s 的扣减流水失败: %v", source, err)
	}
	return n
}

// TestAggregateSearchStampsSourceAndKind 聚合两个源：条目按源顺序合并、source 硬性填、
// kind 第一项是源码且原有标签保留、源自报的 source 被覆盖
func TestAggregateSearchStampsSourceAndKind(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)

	// fake_a 的上游自带一个错的 source：平台必须以路由事实为准，不被上游牵着走
	setPlatformUpstream(t, "fake_a", staticUpstream(t, `{"bookList":[{"bookId":"a1","name":"甲书","kind":"都市,完结","source":"fake_zzz"}]}`).URL)
	setPlatformUpstream(t, "fake_b", staticUpstream(t, `{"bookList":[{"bookId":"b1","name":"乙书"}]}`).URL)

	status, body, raw := doGetAgg(t, srv, admin, "/fake_a/search?query=聚合&sources=fake_a,fake_b")
	if status != http.StatusOK {
		t.Fatalf("聚合搜索 status = %d, want 200（body=%s）", status, truncate(raw, 200))
	}
	if len(body.BookList) != 2 {
		t.Fatalf("合并后应有 2 条结果，实得 %d（body=%s）", len(body.BookList), truncate(raw, 300))
	}
	if body.BookList[0].Source != "fake_a" || body.BookList[1].Source != "fake_b" {
		t.Errorf("source 不是按真实来源打的: %q / %q", body.BookList[0].Source, body.BookList[1].Source)
	}
	if body.BookList[0].Kind != "fake_a,都市,完结" {
		t.Errorf("kind 首项不是源码或原标签被吃掉: %q（want fake_a,都市,完结）", body.BookList[0].Kind)
	}
	if body.BookList[1].Kind != "fake_b" {
		t.Errorf("没有 kind 的条目应补成只含源码: %q", body.BookList[1].Kind)
	}
	if body.BookList[0].BookId != "a1" || body.BookList[1].BookId != "b1" {
		t.Errorf("打标把原有字段改坏了: %+v", body.BookList)
	}
	for _, src := range []string{"fake_a", "fake_b"} {
		if body.SourcesStatus[src] != "ok" {
			t.Errorf("sources_status[%s] = %q, want ok", src, body.SourcesStatus[src])
		}
	}
}

// TestSingleSearchAlsoStamped 不带 sources 的普通搜索同样打标：
// 下游的路由规则只有一套，不能「聚合时才有 source、直连时没有」
func TestSingleSearchAlsoStamped(t *testing.T) {
	srv := newTestServer(t)
	setPlatformUpstream(t, "fake_a", staticUpstream(t, searchEnvelope).URL)

	status, body, raw := doGetAgg(t, srv, adminToken(t, srv), "/fake_a/search?query=单源")
	if status != http.StatusOK {
		t.Fatalf("status = %d（body=%s）", status, truncate(raw, 200))
	}
	if len(body.BookList) == 0 {
		t.Fatal("没有结果，这条用例就是空的")
	}
	for _, it := range body.BookList {
		if it.Source != "fake_a" {
			t.Errorf("单源搜索没打 source: %+v", it)
		}
		if !strings.HasPrefix(fmt.Sprint(it.Kind), "fake_a") {
			t.Errorf("kind 首项不是源码: %v", it.Kind)
		}
	}
	if _, has := body.SourcesStatus["fake_a"]; has {
		t.Error("单源搜索不该带 sources_status（那是聚合的读数）")
	}
}

// TestAggregateSearchSkipsUngrantedSource 套餐不含的源不能借聚合进来：
// 扇出绕过中间件链，不在这里判就等于把 P34 的授权模型废掉一半
func TestAggregateSearchSkipsUngrantedSource(t *testing.T) {
	srv := newTestServer(t)
	token, uid := aggUser(t, srv)

	setPlatformUpstream(t, "fake_a", staticUpstream(t, `{"bookList":[{"bookId":"a1","name":"甲书"}]}`).URL)
	setPlatformUpstream(t, "fake_c", staticUpstream(t, `{"bookList":[{"bookId":"c1","name":"不该出现的书"}]}`).URL)

	if err := gate.UngrantPlanSource(planIDByCode(t, "free"), "fake_c"); err != nil {
		t.Fatalf("回收 fake_c 授权失败: %v", err)
	}

	status, body, raw := doGetAgg(t, srv, token, "/fake_a/search?query=x&sources=fake_a,fake_c")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200（body=%s）", status, truncate(raw, 200))
	}
	if body.SourcesStatus["fake_c"] != "ungranted" {
		t.Errorf("未授权的源没被判成 ungranted: %v", body.SourcesStatus)
	}
	for _, it := range body.BookList {
		if it.Source == "fake_c" || it.Name == "不该出现的书" {
			t.Fatalf("未授权源的结果漏出来了: %+v", body.BookList)
		}
	}
	if len(body.BookList) != 1 {
		t.Errorf("应只留下 fake_a 的 1 条，实得 %d", len(body.BookList))
	}
	// 被跳过的源不该留下扣减
	if n := aggDeductions(t, uid, "fake_c"); n != 0 {
		t.Errorf("未授权的 fake_c 被扣了 %d 次额度", n)
	}
}

// TestAggregateSearchPaysQuotaPerTarget 每个真正打出去的上游各扣一次，
// 而入口那个源**只扣一次**（链上已扣，扇出侧不能重复）
func TestAggregateSearchPaysQuotaPerTarget(t *testing.T) {
	srv := newTestServer(t)
	token, uid := aggUser(t, srv)

	setPlatformUpstream(t, "fake_a", staticUpstream(t, `{"bookList":[{"bookId":"a1","name":"甲书"}]}`).URL)
	setPlatformUpstream(t, "fake_b", staticUpstream(t, `{"bookList":[{"bookId":"b1","name":"乙书"}]}`).URL)
	setAggCost(t, "fake_a", 1)
	setAggCost(t, "fake_b", 1)

	prev := conf.Config.BillingDedupeSec
	conf.Config.BillingDedupeSec = 0 // 关掉 P25 冷却：否则双扣会被去重挡住，这条变异就测不出来
	defer func() { conf.Config.BillingDedupeSec = prev }()
	status, body, raw := doGetAgg(t, srv, token, "/fake_a/search?query=x&sources=fake_a,fake_b")
	if status != http.StatusOK {
		t.Fatalf("status = %d（body=%s）", status, truncate(raw, 200))
	}
	if body.SourcesStatus["fake_a"] != "ok" || body.SourcesStatus["fake_b"] != "ok" {
		t.Fatalf("两个源都该成功: %v", body.SourcesStatus)
	}
	if n := aggDeductions(t, uid, "fake_a"); n != 1 {
		t.Errorf("入口源 fake_a 被扣 %d 次, want 1（链上已扣一次，扇出侧重复扣就是双计）", n)
	}
	if n := aggDeductions(t, uid, "fake_b"); n != 1 {
		t.Errorf("扇出源 fake_b 被扣 %d 次, want 1（聚合不得比直连便宜）", n)
	}
}

// TestAggregateSearchSkipsExhaustedTarget 某个扇出目标当日额度已用完时跳过它，
// 而不是「链上没管到它，于是继续白打上游」
func TestAggregateSearchSkipsExhaustedTarget(t *testing.T) {
	srv := newTestServer(t)
	token, uid := aggUser(t, srv)

	setPlatformUpstream(t, "fake_a", staticUpstream(t, `{"bookList":[{"bookId":"a1","name":"甲书"}]}`).URL)
	setPlatformUpstream(t, "fake_b", staticUpstream(t, `{"bookList":[{"bookId":"b1","name":"乙书"}]}`).URL)
	setAggCost(t, "fake_b", 1)

	// 把 free 套餐对 fake_b 的日限额压到 1，并预置一笔已用完的流水
	var row models.QuotaLimit
	if err := db.DB.Where("plan_id = ? AND scope = ? AND target = ?", planIDByCode(t, "free"), "source", "fake_b").
		First(&row).Error; err != nil {
		t.Fatalf("找不到 free 对 fake_b 的限额行: %v", err)
	}
	if err := db.DB.Model(&row).Update("limit", 1).Error; err != nil {
		t.Fatalf("压限额失败: %v", err)
	}
	if err := db.DB.Create(&models.QuotaUsageLog{UserID: uid, GroupCode: "fake_b", Interface: "search", Cost: 1, CreatedAt: time.Now()}).Error; err != nil {
		t.Fatalf("预置流水失败: %v", err)
	}

	status, body, raw := doGetAgg(t, srv, token, "/fake_a/search?query=x&sources=fake_a,fake_b")
	if status != http.StatusOK {
		t.Fatalf("一个源用完额度不该让整条请求失败: status=%d body=%s", status, truncate(raw, 200))
	}
	if body.SourcesStatus["fake_b"] != "limit_exceeded" {
		t.Errorf("用完额度的源没被判成 limit_exceeded: %v", body.SourcesStatus)
	}
	for _, it := range body.BookList {
		if it.Source == "fake_b" {
			t.Errorf("额度用完的源仍然出了结果: %+v", body.BookList)
		}
	}
	if n := aggDeductions(t, uid, "fake_b"); n != 1 {
		t.Errorf("被判住的 fake_b 又多了扣减: %d, want 仍是那 1 笔预置流水", n)
	}
}

// TestAggregateSearchOneFailingTargetKeepsOthers 单个目标失败既不该把整条聚合带走，
// 也不该把上游的错误文案透给下游
func TestAggregateSearchOneFailingTargetKeepsOthers(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)

	setPlatformUpstream(t, "fake_a", staticUpstream(t, `{"bookList":[{"bookId":"a1","name":"甲书"}]}`).URL)
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"detail":"upstream internal secret /api?app_key=TOPSECRET"}`, http.StatusInternalServerError)
	}))
	t.Cleanup(broken.Close)
	setPlatformUpstream(t, "fake_b", broken.URL)

	status, body, raw := doGetAgg(t, srv, admin, "/fake_a/search?query=x&sources=fake_a,fake_b")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200（一个源坏了不能让整条聚合变失败）", status)
	}
	if body.SourcesStatus["fake_b"] != "upstream_failed" {
		t.Errorf("失败的源没被判成 upstream_failed: %v", body.SourcesStatus)
	}
	if len(body.BookList) != 1 || body.BookList[0].Source != "fake_a" {
		t.Errorf("好源的结果被坏源带走了: %+v", body.BookList)
	}
	// 上游的密钥与内部文案不能出现在下发体里（出口脱敏的口径同样适用于这里）
	if strings.Contains(raw, "TOPSECRET") || strings.Contains(raw, "secret") {
		t.Errorf("响应透出了上游的错误正文: %s", truncate(raw, 300))
	}
}

// TestAggregateSearchFailsLikeDirectWhenNothingReturns 一条结果都没有时必须照实失败。
//
// 「200 + 空列表」在下游与监控里的读数都是「这次没搜到」，而真相是上游都坏了：
// 成功率、覆盖率、榜三处一起说谎，而且单源聚合（本该等价于直连）与直连的状态码还不一致。
func TestAggregateSearchFailsLikeDirectWhenNothingReturns(t *testing.T) {
	srv := newTestServer(t)
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "{}", http.StatusBadGateway)
	}))
	t.Cleanup(broken.Close)
	setPlatformUpstream(t, "fake_a", broken.URL)
	setPlatformUpstream(t, "fake_b", broken.URL)

	// 单源聚合：状态码要与直连一致（入口源的错误原样抛）
	_, directRaw := doRaw(t, srv, http.MethodGet, "/fake_a/search?query=x", nil, authHeader(adminToken(t, srv)))
	stAgg, _, aggRaw := doGetAgg(t, srv, adminToken(t, srv), "/fake_a/search?query=x&sources=fake_a")
	if stAgg == http.StatusOK {
		t.Fatalf("全失败却返回 200: %s", truncate(aggRaw, 200))
	}
	if !strings.Contains(aggRaw, "502") || !strings.Contains(string(directRaw), "502") {
		t.Errorf("聚合与直连的失败读数不一致: agg=%s direct=%s", truncate(aggRaw, 160), truncate(string(directRaw), 160))
	}

	// 多源全失败：仍然不是 200
	st2, _, raw2 := doGetAgg(t, srv, adminToken(t, srv), "/fake_a/search?query=x&sources=fake_a,fake_b")
	if st2 == http.StatusOK {
		t.Errorf("所有目标都坏了却返回 200: %s", truncate(raw2, 200))
	}
}

// TestAggregateTargetsParsing sources 参数的形状纪律：入口源永远排第一、去重、上限截断
func TestAggregateTargetsParsing(t *testing.T) {
	got := legado.AggregateTargets("fake_a", " fake_b ,fake_b,, fake_c ")
	want := []string{"fake_a", "fake_b", "fake_c"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("解析结果 = %v, want %v", got, want)
	}

	var many []string
	for i := 0; i < 30; i++ {
		many = append(many, fmt.Sprintf("src_%02d", i))
	}
	if got := legado.AggregateTargets("route", strings.Join(many, ",")); len(got) != legado.AggregateMaxSources {
		t.Errorf("扇出数量没受上限约束: %d, want %d", len(got), legado.AggregateMaxSources)
	} else if got[0] != "route" {
		t.Errorf("入口源不在第一位: %v", got)
	}

	// 空参数只剩入口源（普通搜索的语义不变）
	if got := legado.AggregateTargets("route", ""); len(got) != 1 || got[0] != "route" {
		t.Errorf("sources 为空时 = %v, want [route]", got)
	}
}

// TestStampSearchSourceSkipsInBandError 带内错误正文没有 bookList，打标必须原样放过：
// 给失败响应塞一个空列表，下游会把「失败了」读成「没这本书」
func TestStampSearchSourceSkipsInBandError(t *testing.T) {
	errBody := map[string]interface{}{"contentType": "error", "message": "上游超时"}
	out := legado.StampSearchSource(errBody, "fake_a")
	m, ok := out.(map[string]interface{})
	if !ok {
		t.Fatalf("返回形状被改了: %T", out)
	}
	if _, has := m["bookList"]; has {
		t.Errorf("给错误正文塞了空列表: %+v", m)
	}
	if m["message"] != "上游超时" {
		t.Errorf("错误正文被改写: %+v", m)
	}
}

// TestPrependSourceToKindKeepsShape kind 两种形状（字符串/数组）都保持原形状，
// 平台打标不该顺手改了字段的类型
func TestPrependSourceToKindKeepsShape(t *testing.T) {
	if got := legado.PrependSourceToKind("都市,完结", "fake_a"); got != "fake_a,都市,完结" {
		t.Errorf("字符串形状被改坏: %#v", got)
	}
	if got := legado.PrependSourceToKind("fake_a,都市", "fake_a"); got != "fake_a,都市" {
		t.Errorf("已有源码项应挪到最前而不是重复: %#v", got)
	}
	arr := []interface{}{"都市", "完结"}
	got, ok := legado.PrependSourceToKind(arr, "fake_a").([]interface{})
	if !ok || len(got) != 3 || got[0] != "fake_a" {
		t.Errorf("数组形状没保持: %#v", got)
	}
	if got := legado.PrependSourceToKind(nil, "fake_a"); got != "fake_a" {
		t.Errorf("空 kind 应补成只含源码: %#v", got)
	}
}
