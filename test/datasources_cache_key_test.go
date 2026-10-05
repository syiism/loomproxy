package test

// 待办清单 P100（第三十六遍巡检）：`/datasources` 的「按套餐裁剪」在 HTTP 上到底有没有生效，
// 取决于**身份有没有被解析**——而这两件事在过去是绑在一起的、绑错了地方。
//
// 现场形状（`utils/auth.go` 的 `VerifyAuth`）：
//
//	if IsWhitelisted(path) { return nil }     // 凭证一个字都不解析
//
// 对照同一函数里 `AuthEnabled=false` 那一支（P64）：那是「先 best-effort 解析、失败按匿名放行」。
// 于是白名单免的不只是**强制**，把**解析**也一起免了。可达路径不是理论：文档里写着
// 「要匿名分发清单就把它加进 `AUTH_WHITELIST`」（AGENTS §11 / 鉴权页），而运维真这么做了的那一天，
// 所有登录用户的 `/datasources` 会一起退化成免费版视图——VIP 看不见自己有权用的源，且没有任何提示。
//
// 本文件的三条用例各自钉一件事：
//   ① 身份被解析时（不在白名单），视图真的按套餐裁剪，且缓存键带得下"这条响应属于谁"（P84 那一族）；
//   ② 缓存的键必须含 uid：删掉 `datasource.go` 里 `cleanParams["uid"]` 那行，① 当场红；
//   ③ **tripwire**：钉住「白名单路径今天不带身份」这个现状——P100 修好后这条会红，
//      红了就该把它改成"白名单也解析"，而不是删掉。它不是给缺陷背书，是防止修的时候没人注意到行为变了。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"loomproxy/conf"
	"loomproxy/db"
	"loomproxy/handlers/catalog"
	"loomproxy/models"
)

func datasourceNamesAs(t *testing.T, srv *httptest.Server, token string) map[string]bool {
	t.Helper()
	status, env := doJSON(t, srv, http.MethodGet, "/datasources", nil, authHeader(token))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("带凭证 GET /datasources 失败（status=%d code=%d msg=%s）", status, env.Code, env.Msg)
	}
	var items []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(env.Data, &items); err != nil {
		t.Fatalf("解析 /datasources 响应失败: %v", err)
	}
	names := make(map[string]bool, len(items))
	for _, it := range items {
		names[it.ID] = true
	}
	return names
}

// takeDatasourcesOffWhitelist 把 /datasources 从测试脚手架的白名单里摘掉，让 apiauth 真的去解析凭证。
// 改的是全局 conf（本仓用例的既有做法），用 t.Cleanup 还原。
func takeDatasourcesOffWhitelist(t *testing.T) {
	t.Helper()
	prev := conf.Config.AuthWhitelist
	out := make([]string, 0, len(prev))
	for _, p := range prev {
		if p == "/datasources" {
			continue
		}
		out = append(out, p)
	}
	conf.Config.AuthWhitelist = out
	t.Cleanup(func() { conf.Config.AuthWhitelist = prev })
	catalog.InvalidateDatasourcesCache()
}

func setUserPlanForTest(t *testing.T, username string, planID uint) {
	t.Helper()
	if err := db.DB.Model(&models.User{}).Where("username = ?", username).
		Update("plan_id", planID).Error; err != nil {
		t.Fatalf("改 %s 的套餐失败: %v", username, err)
	}
	// 失效一律走产品自己的口，用例不复制键的拼法（P39：键拼法长出第二份，失效就打空而构建照样绿）
	catalog.InvalidateDatasourcesCache()
}

func TestDatasourcesViewFollowsPlanWhenIdentityParsed(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)
	freeID, vipID := planIDByCode(t, "free"), planIDByCode(t, "vip")
	takeDatasourcesOffWhitelist(t)

	// 让两个套餐在 fake_b 上确实不同：回收免费套餐那一行（P34：删行=回收）
	row := grantRow(t, freeID)
	if _, env := doJSON(t, srv, http.MethodDelete, "/admin/quotas/limits/"+itoa(row.ID), nil, authHeader(admin)); env.Code != 0 {
		t.Fatalf("回收 free 对 fake_b 的授权失败: %s", env.Msg)
	}

	freeToken := registerUser(t, srv, "p100_free", "p100_free@example.com", "pass1234")
	vipToken := registerUser(t, srv, "p100_vip", "p100_vip@example.com", "pass1234")
	setUserPlanForTest(t, "p100_vip", vipID)

	// 先做基线：夹具没做出差别的话，下面的断言全是空转
	if names := datasourceNamesAs(t, srv, freeToken); names["fake_b"] {
		t.Fatal("免费视图含 fake_b——回收没生效，这条用例的前提不成立")
	}
	if names := datasourceNamesAs(t, srv, vipToken); !names["fake_b"] {
		t.Fatal("VIP 视图不含 fake_b：要么身份没被解析（见 P100），要么缓存键没带够维度（P84 那一族）")
	}

	// 反向顺序也要隔离：先 VIP 填缓存，再看免费用户会不会拿到 VIP 的列表
	catalog.InvalidateDatasourcesCache()
	if names := datasourceNamesAs(t, srv, vipToken); !names["fake_b"] {
		t.Fatal("清缓存后 VIP 视图仍不含 fake_b")
	}
	if names := datasourceNamesAs(t, srv, freeToken); names["fake_b"] {
		t.Fatal("免费用户拿到了 VIP 的列表：缓存键少了「这条响应属于谁」那一段")
	}

	// 同套餐的第三个人也得各自一份（这一条把"键里带 uid"钉住，而不是只钉"键里带套餐"）
	other := registerUser(t, srv, "p100_other", "p100_other@example.com", "pass1234")
	if names := datasourceNamesAs(t, srv, other); names["fake_b"] {
		t.Error("第三个免费套餐用户看到了 fake_b（应是回收后的列表）")
	}
}

// TestWhitelistedDatasourcesDropsIdentity P100 的 tripwire：现状是「路由在白名单上 → 凭证不解析」。
// 修掉 P100 之后这条会红——那是提醒你把断言改成「白名单也解析、但解析失败仍匿名」，不是让你删掉它。
func TestWhitelistedDatasourcesDropsIdentity(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)
	freeID, vipID := planIDByCode(t, "free"), planIDByCode(t, "vip")
	// 不动白名单：脚手架默认就带着 /datasources

	row := grantRow(t, freeID)
	if _, env := doJSON(t, srv, http.MethodDelete, "/admin/quotas/limits/"+itoa(row.ID), nil, authHeader(admin)); env.Code != 0 {
		t.Fatalf("回收 free 对 fake_b 的授权失败: %s", env.Msg)
	}
	vipToken := registerUser(t, srv, "p100w_vip", "p100w_vip@example.com", "pass1234")
	setUserPlanForTest(t, "p100w_vip", vipID)

	names := datasourceNamesAs(t, srv, vipToken)
	if names["fake_b"] {
		t.Fatalf("P100 已修：白名单路径现在会解析身份了——把这条用例改成「解析成功按套餐裁剪 / 解析失败仍匿名」，" +
			"并回到待办清单把 P100 的状态行填上落点（不要只是删掉这条）")
	}
	t.Log("现状确认：带合法 VIP 凭证请求白名单上的 /datasources，拿到的是免费版视图（身份被整体丢弃）——这就是 P100")
}
