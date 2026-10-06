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
// TestWhitelistedDatasourcesStillParsesIdentity P100 修完之后的形状：**白名单免的是强制，不是解析**。
// 两支各钉一件事，少一支就留着一个失效方向：
//
//	① 带合法 VIP 凭证 → 按套餐裁剪（`fake_b` 在）。过去这一支拿到免费版视图，
//	   于是 `/datasources` 里"按套餐裁剪"那段代码是**死代码**——文档还明确建议运维把这条加进白名单。
//	② 不带凭证 → 仍然 200，拿到匿名那一份。白名单"不拦匿名"的语义不许被这次修复改成"要鉴权"，
//	   只钉 ① 会让"顺手把白名单收掉"这种过度修复照样绿。
func TestWhitelistedDatasourcesStillParsesIdentity(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)
	freeID, vipID := planIDByCode(t, "free"), planIDByCode(t, "vip")
	// 不动白名单：脚手架默认就带着 /datasources——这条用例测的正是"在白名单上"那一支

	row := grantRow(t, freeID)
	if _, env := doJSON(t, srv, http.MethodDelete, "/admin/quotas/limits/"+itoa(row.ID), nil, authHeader(admin)); env.Code != 0 {
		t.Fatalf("回收 free 对 fake_b 的授权失败: %s", env.Msg)
	}
	vipToken := registerUser(t, srv, "p100w_vip", "p100w_vip@example.com", "pass1234")
	setUserPlanForTest(t, "p100w_vip", vipID)

	// ② 匿名那一支，同时是**防断言空转的前提检查**：夹具没做出两种视图的话，下面的断言都是空转
	catalog.InvalidateDatasourcesCache()
	if names := datasourceNamesAs(t, srv, ""); names["fake_b"] {
		t.Fatal("匿名视图含 fake_b——回收没生效，这两条断言会同时空转")
	}
	if status, env := doJSON(t, srv, http.MethodGet, "/datasources", nil, nil); status != http.StatusOK || env.Code != 0 {
		t.Errorf("白名单被这次修复变成了要鉴权：匿名请求回 %d（%v）——白名单的语义是不拦，这条不许变", status, env.Msg)
	}

	// ① 带合法凭证那一支
	catalog.InvalidateDatasourcesCache()
	if names := datasourceNamesAs(t, srv, vipToken); !names["fake_b"] {
		t.Fatal("白名单上的 /datasources 仍把身份整体丢掉：VIP 拿到的是免费版视图（P100 没修好，或声明位没接上）")
	}
}
