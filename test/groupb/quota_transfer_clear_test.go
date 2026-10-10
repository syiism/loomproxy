package groupb

// 待办清单 P119 ④：用户侧「额度转移记录」的清除——落地的是 C 档（**软删**）。
//
// 这条用例的重心不是"清掉了没有"，是三件错了不报错的事：
//   ① **审计面不能被本人关掉**：行留在库里、管理端仍可见，且管理端要说清"这条被本人清过"
//      （管理员看到一份被动过却看起来像原始记录，比过滤掉更糟）；
//   ② **清除不碰判定**：转移写的是 quota_limits / 覆盖增量，清除只动 cleared_at 那一格——
//      所以转过去的额度必须在清除之后仍然生效（这条是"软删没有顺带动到额度"的钉子）；
//   ③ **只认会话**：`/quota` 那组三形态统一可用（读得到），但销毁记录这一步不能交给长期密钥
//      （§7 那条边界：密钥不该能删审计痕迹）。
//
// 幂等（第二次清 0 笔）与越权（A 的清不动 B 的行）各钉一处：
// 前者是"0 是合法读数不是失败"，后者是那句老判据——归属条件只能有一处，写成报错反而暴露存在性。

import (
	"fmt"
	"net/http"
	"testing"

	"loomproxy/db"
	"loomproxy/gate"
	"loomproxy/models"
)

func TestQuotaTransferClearIsSoftDelete(t *testing.T) {
	srv := newTestServer(t)
	owner := registerUser(t, srv, "tc_v1", "tc_v1@example.com", "pass1234")
	admin := adminToken(t, srv)

	var u models.User
	if err := db.DB.Where("username = ?", "tc_v1").First(&u).Error; err != nil {
		t.Fatalf("读不回用户: %v", err)
	}
	plan := gate.ResolvePlanForUser(&u)
	ensureGrant(t, plan.ID, "fake_a", 100)
	ensureGrant(t, plan.ID, "fake_b", 40)

	status, env := doJSON(t, srv, http.MethodPost, "/quota/transfer",
		map[string]interface{}{"from": "fake_a", "to": "fake_b", "amount": 80}, authHeader(owner))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("前置转移失败 status=%d msg=%s", status, env.Msg)
	}
	// 样本成立性自己数一遍（判据页《断言空转》：没有样本的断言永远是绿的）
	if rows := auditRows(t, u.ID); len(rows) != 1 {
		t.Fatalf("审计应有 1 行，实得 %d——这条用例的前提没了", len(rows))
	}

	// 1) 清除：返回笔数、本人列表变空
	if status, env = doJSON(t, srv, http.MethodDelete, "/quota/transfers", nil, authHeader(owner)); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("清除应 200，实为 %d（msg=%s）", status, env.Msg)
	}
	if got := env.dataMap(t)["cleared"]; got != float64(1) {
		t.Errorf("清除应报 1 笔，实得 %v", got)
	}
	status, env = doJSON(t, srv, http.MethodGet, "/quota/transfers", nil, authHeader(owner))
	if status != http.StatusOK {
		t.Fatalf("清除后本人读列表应 200（空表），实为 %d", status)
	}
	if got := len(env.dataMap(t)["list"].([]interface{})); got != 0 {
		t.Errorf("清除后本人仍看到 %d 条——用户侧的过滤没生效（要么没写 cleared_at IS NULL，要么写错列）", got)
	}
	if got := env.dataMap(t)["total"]; got != float64(0) {
		t.Errorf("total 应跟着变 0（分页总数不能把已清除的算进去），实得 %v", got)
	}

	// 2) 幂等：再清一次报 0 笔而不是报错
	if status, env = doJSON(t, srv, http.MethodDelete, "/quota/transfers", nil, authHeader(owner)); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("第二次清除不该报错，实为 status=%d msg=%s", status, env.Msg)
	}
	if got := env.dataMap(t)["cleared"]; got != float64(0) {
		t.Errorf("第二次应报 0 笔（已经清过），实得 %v", got)
	}

	// 3) **行还在库里**：软删不是 DELETE
	var still int64
	if err := db.DB.Model(&models.QuotaTransferLog{}).Where("user_id = ?", u.ID).Count(&still).Error; err != nil {
		t.Fatalf("数审计行失败: %v", err)
	}
	if still != 1 {
		t.Errorf("库里应有 1 行（软删），实得 %d——清除做成了真删，审计面就归零了", still)
	}
	var clearedOne models.QuotaTransferLog
	if err := db.DB.Where("user_id = ?", u.ID).First(&clearedOne).Error; err != nil {
		t.Fatalf("读回那一行失败: %v", err)
	}
	if clearedOne.ClearedAt == nil {
		t.Error("那一行的 cleared_at 是空——本人列表靠它过滤，空就等于没清过")
	}

	// 4) 管理端仍可见，且**把"被本人清过"说出来**
	status, env = doJSON(t, srv, http.MethodGet, fmt.Sprintf("/admin/users/%d/quota-transfers", u.ID), nil, authHeader(admin))
	if status != http.StatusOK {
		t.Fatalf("管理端读转移史应 200，实为 %d", status)
	}
	arows, _ := env.dataMap(t)["list"].([]interface{})
	if len(arows) != 1 {
		t.Fatalf("管理端应仍看到 1 条（C 档：审计面不过滤），实得 %d", len(arows))
	}
	row, _ := arows[0].(map[string]interface{})
	if row["cleared"] != true {
		t.Errorf("管理端那一格 cleared 应为 true（%v）——看不出来就被当成原始记录了", row["cleared"])
	}

	// 5) 清除没有动到额度：转过去的 80 仍在（判定读的是覆盖行，与这一列无关）
	if lim, ok := transferOf(t, u.ID, "fake_b"); !ok || lim != 80 {
		t.Errorf("清除之后 fake_b 的覆盖增量应还是 80，实得 %v（存在=%v）——软删顺带动了判定输入，那就不是可见性问题了", lim, ok)
	}
}

func TestQuotaTransferClearRespectsOwnership(t *testing.T) {
	srv := newTestServer(t)
	alice := registerUser(t, srv, "tc_a", "tc_a@example.com", "pass1234")
	bob := registerUser(t, srv, "tc_b", "tc_b@example.com", "pass1234")
	uidA := userIDByName(t, "tc_a")
	uidB := userIDByName(t, "tc_b")

	// 两个人的套餐可能相同，逐个把授权摆好再各转一笔
	for _, who := range []struct {
		token string
		uid   uint
	}{{alice, uidA}, {bob, uidB}} {
		var u models.User
		if err := db.DB.First(&u, who.uid).Error; err != nil {
			t.Fatalf("读不回用户 %d: %v", who.uid, err)
		}
		pl := gate.ResolvePlanForUser(&u)
		ensureGrant(t, pl.ID, "fake_a", 100)
		ensureGrant(t, pl.ID, "fake_b", 40)
		if status, env := doJSON(t, srv, http.MethodPost, "/quota/transfer",
			map[string]interface{}{"from": "fake_a", "to": "fake_b", "amount": 10}, authHeader(who.token)); status != http.StatusOK {
			t.Fatalf("%s 的前置转移失败 status=%d msg=%s", u.Username, status, env.Msg)
		}
	}
	if rows := auditRows(t, uidA); len(rows) != 1 {
		t.Fatalf("甲应有 1 行审计，实得 %d", len(rows))
	}

	// 甲清除**只影响自己**：乙那一行必须还在、乙仍看得见
	if status, env := doJSON(t, srv, http.MethodDelete, "/quota/transfers", nil, authHeader(alice)); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("甲清除失败 status=%d msg=%s", status, env.Msg)
	}
	if rowsB := auditRows(t, uidB); len(rowsB) != 1 || rowsB[0].ClearedAt != nil {
		t.Errorf("乙的审计行被甲的清除动了（%d 行、cleared_at=%v）——归属条件少了一处", len(rowsB), rowsB[0].ClearedAt)
	}
	if n := auditRows(t, uidA); len(n) != 1 || n[0].ClearedAt == nil {
		t.Errorf("甲那一行应存在且已标记清除，实得 %d 行", len(n))
	}
	_, env := doJSON(t, srv, http.MethodGet, "/quota/transfers", nil, authHeader(bob))
	if got := len(env.dataMap(t)["list"].([]interface{})); got != 1 {
		t.Errorf("乙应仍看到自己的 1 条，实得 %d", got)
	}
}

func TestQuotaTransferClearNeedsSession(t *testing.T) {
	srv := newTestServer(t)
	user := registerUser(t, srv, "tc_k1", "tc_k1@example.com", "pass1234")
	uid := userIDByName(t, "tc_k1")

	var u models.User
	if err := db.DB.First(&u, uid).Error; err != nil {
		t.Fatalf("读不回用户: %v", err)
	}
	pl := gate.ResolvePlanForUser(&u)
	ensureGrant(t, pl.ID, "fake_a", 100)
	ensureGrant(t, pl.ID, "fake_b", 40)
	if status, env := doJSON(t, srv, http.MethodPost, "/quota/transfer",
		map[string]interface{}{"from": "fake_a", "to": "fake_b", "amount": 10}, authHeader(user)); status != http.StatusOK {
		t.Fatalf("前置转移失败 status=%d msg=%s", status, env.Msg)
	}

	status, env := doJSON(t, srv, http.MethodPost, "/apikey", map[string]string{"name": "tc"}, authHeader(user))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("建密钥失败 status=%d msg=%s", status, env.Msg)
	}
	plain, _ := env.dataMap(t)["key"].(string)
	if plain == "" {
		t.Fatalf("创建响应没有 key")
	}
	keyHdr := map[string]string{"X-API-Key": plain}

	// 读：三形态统一可用（P97 定的口径，这里当对照用——它必须仍然 200，
	// 否则下面那条"删除被拒"就只是"整条端点都挂了"的另一种写法）
	if st, _ := doRaw(t, srv, http.MethodGet, "/quota/transfers", nil, keyHdr); st != http.StatusOK {
		t.Fatalf("apiKey 读自己的转移史应 200（对照），实为 %d——这条对照不成立，后面的断言全都不算数", st)
	}
	// 写（销毁记录）：只认会话
	st, raw := doRaw(t, srv, http.MethodDelete, "/quota/transfers", nil, keyHdr)
	if st != http.StatusUnauthorized && st != http.StatusForbidden {
		t.Errorf("apiKey 调清除应被拒（只认会话），实为 %d（body=%s）", st, string(raw))
	}
	// 而且**没有真的清掉**：本人列表还是 1 条
	if _, env = doJSON(t, srv, http.MethodGet, "/quota/transfers", nil, authHeader(user)); len(env.dataMap(t)["list"].([]interface{})) != 1 {
		t.Error("apiKey 那一次调用虽然回了非 2xx，却把记录清掉了——守卫与写库的先后顺序错了")
	}
}
