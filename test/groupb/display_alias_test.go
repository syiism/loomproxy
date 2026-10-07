package groupb

// 套餐名/角色名的用户维度显示别名（待办清单 P43）。
//
// 四条要守住的性质，每条各一个用例：
//  1. 默认名那张全局表不动，别名只影响「显示」；
//  2. 本人界面（/auth/me、/quota/dashboard）看别名，管理员界面**同时**拿到默认名与别名；
//  3. 清除（alias 传空）退回默认名，且「超长」不能走这条清除路径（手滑删掉覆盖是坏行为）；
//  4. 别名按 target 存，所以换套餐不会把上一个套餐的称呼串过来。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"loomproxy/db"
	"loomproxy/models"
)

var aliasSeq atomic.Int64

// aliasUser 建一个用户并绑上指定套餐，返回 (会话 token, 用户名, 套餐 ID)
func aliasUser(t *testing.T, srv *httptest.Server, admin, planCode string) (string, string, uint) {
	t.Helper()
	name := fmt.Sprintf("al_%d", aliasSeq.Add(1))
	token := registerUser(t, srv, name, name+"@example.com", "pass1234")
	pid := planIDByCode(t, planCode)
	status, env := doJSON(t, srv, http.MethodPut, "/admin/users/"+uidOf(t, srv, admin, name)+"/plan",
		map[string]interface{}{"plan_id": pid}, authHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("绑定 %s 套餐失败: status=%d code=%d msg=%s", planCode, status, env.Code, env.Msg)
	}
	return token, name, pid
}

// uidOf 用管理员列表按用户名找一个用户的 id（不靠内存顺序猜）
func uidOf(t *testing.T, srv *httptest.Server, admin, username string) string {
	t.Helper()
	status, env := doJSON(t, srv, http.MethodGet, "/admin/users"+buildQuery(map[string]string{"keyword": username}), nil, authHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("查用户列表失败: status=%d msg=%s", status, env.Msg)
	}
	rows, _ := env.dataMap(t)["list"].([]interface{})
	for _, r := range rows {
		m, _ := r.(map[string]interface{})
		if m["username"] == username {
			id, _ := m["id"].(float64)
			return fmt.Sprintf("%.0f", id)
		}
	}
	t.Fatalf("用户列表里找不到 %s（%d 行）", username, len(rows))
	return ""
}

func putAlias(t *testing.T, srv *httptest.Server, token string, kind string, targetID uint, alias string) (int, apiEnvelope) {
	t.Helper()
	return doJSON(t, srv, http.MethodPut, "/auth/display-alias",
		map[string]interface{}{"kind": kind, "target_id": targetID, "alias": alias}, authHeader(token))
}

// planOfMe 取 /auth/me 里当前套餐的三个字段
func planOfMe(t *testing.T, srv *httptest.Server, token string) map[string]interface{} {
	t.Helper()
	status, env := doJSON(t, srv, http.MethodGet, "/auth/me", nil, authHeader(token))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("/auth/me 失败: status=%d msg=%s", status, env.Msg)
	}
	plan, _ := env.dataMap(t)["plan"].(map[string]interface{})
	if plan == nil {
		t.Fatalf("/auth/me 没有 plan 字段: %+v", env.dataMap(t))
	}
	return plan
}

func TestDisplayAliasAppliesToSelfButNotToAdminView(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)
	token, name, pid := aliasUser(t, srv, admin, "vip")

	var plan models.QuotaPlan
	if err := db.DB.First(&plan, pid).Error; err != nil {
		t.Fatalf("读套餐失败: %v", err)
	}
	const alias = "至尊年卡"

	status, env := putAlias(t, srv, token, models.DisplayKindPlan, pid, alias)
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("设别名失败: status=%d code=%d msg=%s", status, env.Code, env.Msg)
	}
	if got := env.dataMap(t)["alias"]; got != alias {
		t.Errorf("写端点回读的 alias = %v, want %q（应当以库里的值返回，而不是把入参回显）", got, alias)
	}

	// 本人界面：display_name 是别名，name 仍是默认名（默认那份事实没被改）
	me := planOfMe(t, srv, token)
	if me["display_name"] != alias {
		t.Errorf("plan.display_name = %v, want %q", me["display_name"], alias)
	}
	if me["name"] != plan.Name {
		t.Errorf("plan.name = %v, want 默认名 %q——别名不许覆盖全局默认名", me["name"], plan.Name)
	}
	if me["alias"] != alias {
		t.Errorf("plan.alias = %v, want %q", me["alias"], alias)
	}

	// 首页 dashboard 也必须走同一处规则，否则「个人中心改了、首页没改」就是这么长出来的
	status, env = doJSON(t, srv, http.MethodGet, "/quota/dashboard", nil, authHeader(token))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("dashboard 失败: status=%d msg=%s", status, env.Msg)
	}
	if got := env.dataMap(t)["plan_name"]; got != alias {
		t.Errorf("dashboard plan_name = %v, want %q（两处显示名规则必须同源）", got, alias)
	}

	// 管理员列表：两个值都要在，管理员的读数不被被观察者修饰
	rows, _ := mustAdminUsers(t, srv, admin, name)
	if rows[0]["plan"] == nil {
		t.Fatalf("管理员列表没有 plan 字段")
	}
	p, _ := rows[0]["plan"].(map[string]interface{})
	if p["name"] != plan.Name || p["alias"] != alias {
		t.Errorf("管理员视图 name=%v alias=%v, want name=%q alias=%q（管理员应看到两栏而不是被别名替换）",
			p["name"], p["alias"], plan.Name, alias)
	}

	// 额度弹窗同样两栏：plan_name 是默认名、plan_alias 是别名（弹窗是管理员改额度的地方，
	// 那里把默认名换成别名就会让人误判「这人买的是什么档位」）
	status, env = doJSON(t, srv, http.MethodGet, "/admin/users/"+uidOf(t, srv, admin, name)+"/quota", nil, authHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("额度详情失败: status=%d msg=%s", status, env.Msg)
	}
	qv := env.dataMap(t)
	if qv["plan_name"] != plan.Name || qv["plan_alias"] != alias {
		t.Errorf("额度详情 plan_name=%v plan_alias=%v, want %q / %q", qv["plan_name"], qv["plan_alias"], plan.Name, alias)
	}

	// 库里默认名一个字没动
	var after models.QuotaPlan
	if err := db.DB.First(&after, pid).Error; err != nil {
		t.Fatalf("复读套餐失败: %v", err)
	}
	if after.Name != plan.Name {
		t.Errorf("quota_plans.name 被改成 %q，默认名这张全局表不许动", after.Name)
	}
}

func mustAdminUsers(t *testing.T, srv *httptest.Server, admin, keyword string) ([]map[string]interface{}, int64) {
	t.Helper()
	status, env := doJSON(t, srv, http.MethodGet, "/admin/users"+buildQuery(map[string]string{"keyword": keyword}), nil, authHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("管理员用户列表失败: status=%d msg=%s", status, env.Msg)
	}
	raw, _ := env.dataMap(t)["list"].([]interface{})
	out := make([]map[string]interface{}, 0, len(raw))
	total, _ := env.dataMap(t)["total"].(float64)
	for _, r := range raw {
		m, _ := r.(map[string]interface{})
		out = append(out, m)
	}
	return out, int64(total)
}

func TestDisplayAliasClearAndOverlong(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)
	token, _, pid := aliasUser(t, srv, admin, "vip")

	if status, env := putAlias(t, srv, token, models.DisplayKindPlan, pid, "  黑卡  "); status != http.StatusOK {
		t.Fatalf("设别名失败: %d %s", status, env.Msg)
	} else if got := env.dataMap(t)["alias"]; got != "黑卡" {
		t.Errorf("别名应去掉首尾空白，实得 %q", got)
	}

	// 超长必须是 400，不能静默清成空串——空串在这条端点上的语义是「清除覆盖」
	long := strings.Repeat("长", models.AliasMaxRunes+1)
	status, env := putAlias(t, srv, token, models.DisplayKindPlan, pid, long)
	if status != http.StatusBadRequest {
		t.Fatalf("超长别名 status = %d, want 400（msg=%s）；静默清除会让一次手滑变成删掉覆盖", status, env.Msg)
	}
	if me := planOfMe(t, srv, token); me["display_name"] != "黑卡" {
		t.Errorf("被超长请求误清了：display_name = %v, want 仍是「黑卡」", me["display_name"])
	}

	// 控制字符被剥掉（列宽与显示都不该被 \r\n 一类东西咬到）
	if status, env = putAlias(t, srv, token, models.DisplayKindPlan, pid, "a\tb\r\nc"); status != http.StatusOK {
		t.Fatalf("含控制字符的别名写入失败: %d %s", status, env.Msg)
	} else if got := env.dataMap(t)["alias"]; got != "abc" {
		t.Errorf("控制字符未清理，实得 %q", got)
	}

	// 清除：空串 = 回到默认名
	if status, env = putAlias(t, srv, token, models.DisplayKindPlan, pid, ""); status != http.StatusOK {
		t.Fatalf("清除别名失败: %d %s", status, env.Msg)
	} else if cleared, _ := env.dataMap(t)["cleared"].(bool); !cleared {
		t.Errorf("清除响应 cleared = %v", env.dataMap(t)["cleared"])
	}
	var plan models.QuotaPlan
	if err := db.DB.First(&plan, pid).Error; err != nil {
		t.Fatalf("读套餐失败: %v", err)
	}
	if me := planOfMe(t, srv, token); me["display_name"] != plan.Name || me["alias"] != "" {
		t.Errorf("清除后应回默认名，实得 display_name=%v alias=%v", me["display_name"], me["alias"])
	}
	// 库里那一行真的没了（不是留着一条空串假装清过）
	if got := aliasRowCount(t, pid); got != 0 {
		t.Errorf("清除后仍剩 %d 行别名记录，应该删干净", got)
	}
}

func aliasRowCount(t *testing.T, planID uint) int64 {
	t.Helper()
	var n int64
	if err := db.DB.Model(&models.UserDisplayAlias{}).
		Where("kind = ? AND target_id = ?", models.DisplayKindPlan, planID).Count(&n).Error; err != nil {
		t.Fatalf("数别名行失败: %v", err)
	}
	return n
}

// TestDisplayAliasNotCarriedAcrossPlanChange 别名按 (kind, target_id) 存，
// 换套餐后上一个套餐的称呼不会串到新套餐上——这条是「为什么不建 users.plan_alias 一列」的答案。
func TestDisplayAliasNotCarriedAcrossPlanChange(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)
	token, name, vipID := aliasUser(t, srv, admin, "vip")

	if status, env := putAlias(t, srv, token, models.DisplayKindPlan, vipID, "至尊年卡"); status != http.StatusOK {
		t.Fatalf("设别名失败: %d %s", status, env.Msg)
	}

	// 换到免费版：显示名回到「免费版」，而不是那条还留在库里的 vip 别名
	freeID := planIDByCode(t, "free")
	status, env := doJSON(t, srv, http.MethodPut, "/admin/users/"+uidOf(t, srv, admin, name)+"/plan",
		map[string]interface{}{"plan_id": freeID}, authHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("改套餐失败: %d %s", status, env.Msg)
	}
	var free models.QuotaPlan
	if err := db.DB.First(&free, freeID).Error; err != nil {
		t.Fatalf("读 free 套餐失败: %v", err)
	}
	if me := planOfMe(t, srv, token); me["display_name"] != free.Name {
		t.Errorf("换套餐后 display_name = %v, want %q（别名串套餐就是这形状）", me["display_name"], free.Name)
	}

	// 而且免费版没资格再设别名
	if status, _ := putAlias(t, srv, token, models.DisplayKindPlan, freeID, "随便叫"); status != http.StatusForbidden {
		t.Errorf("免费套餐设别名 status = %d, want 403", status)
	}
}

func TestDisplayAliasGuardsTargetAndCredential(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)
	token, _, pid := aliasUser(t, srv, admin, "vip")

	// 不是自己的套餐：拒（不给「随便挑一个 plan_id 起名」留口子）
	other := planIDByCode(t, "free")
	if other == pid {
		t.Skip("测试数据里 free 与 vip 同 id")
	}
	if status, _ := putAlias(t, srv, token, models.DisplayKindPlan, other, "蹭个名"); status != http.StatusBadRequest {
		t.Errorf("给别人的套餐设别名 status = %d, want 400", status)
	}

	// kind 只认 plan/role
	if status, _ := putAlias(t, srv, token, "datasource", pid, "x"); status != http.StatusBadRequest {
		t.Errorf("非法 kind status = %d, want 400", status)
	}

	// 别名是自我表达：长期密钥改不动（与 /auth/privacy 同一条不变量）
	_, key := threeFormUser(t, srv)
	for _, f := range []struct {
		headers map[string]string
		query   string
	}{
		{map[string]string{"X-API-Key": key}, ""},
		{nil, "?api_key=" + key},
	} {
		status, _ := doJSON(t, srv, http.MethodPut, "/auth/display-alias"+f.query,
			map[string]interface{}{"kind": models.DisplayKindPlan, "target_id": pid, "alias": "替他人改名"}, f.headers)
		if status == http.StatusOK {
			t.Errorf("用 %v 竟能改显示别名", f.headers)
		}
		if status != http.StatusUnauthorized && status != http.StatusForbidden {
			t.Errorf("密钥形态期望 401/403，实得 %d", status)
		}
	}
}
