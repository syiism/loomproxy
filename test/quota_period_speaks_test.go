package test

// 限额行的 `period`：被接受、被存储、面板把它当单位显示（`100 / month`），而**没有任何判定读它**
// （窗口只有一个口径「当日」，`gate.UsageSince`）。本轮只做了两件事（待办清单 P70 的语义仍等人拍）：
//   ① 写入口在周期是个"装饰值"时出一条归因 ERROR；
//   ② 授权端点的响应不再回字面量 `limit:-1`，改成读回库里那行的生效值。
// 两条都是"让静默说话"，对外行为、判定口径、库里已有的值一律没动。

import (
	"bytes"
	"log"
	"net/http"
	"strings"
	"testing"

	"loomproxy/db"
	"loomproxy/gate"
	"loomproxy/models"
)

// captureLog 把标准日志接进缓冲区；返回的计数函数按子串数出声条数。
func captureLog(t *testing.T) func(sub string) int {
	t.Helper()
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })
	return func(sub string) int { return strings.Count(buf.String(), sub) }
}

func limitRow(t *testing.T, planID uint, scope, target string) models.QuotaLimit {
	t.Helper()
	var row models.QuotaLimit
	if err := db.DB.Where("plan_id = ? AND scope = ? AND target = ?", planID, scope, target).
		First(&row).Error; err != nil {
		t.Fatalf("查限额行失败（plan=%d scope=%s target=%s）: %v", planID, scope, target, err)
	}
	return row
}

// TestGrantResponseReportsStoredLimit 「授权」这条端点过去把 `limit:-1` 写死在响应里，
// 而 grant 真正落的是该套餐的默认档（free = 100）。这条用例钉的是**响应等于库里那行**。
func TestGrantResponseReportsStoredLimit(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)
	free := planIDByCode(t, "free")

	// 先回收，否则端点回 409 而这条用例什么都不就验不了
	if err := gate.UngrantPlanSource(free, "fake_b"); err != nil {
		t.Fatalf("回收 fake_b 失败: %v", err)
	}
	status, env := doJSON(t, srv, http.MethodPost, "/admin/quotas/plans/"+itoa(free)+"/data-sources",
		map[string]interface{}{"data_source_id": dataSourceIDByName(t, "fake_b")}, authHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("授权端点失败（status=%d msg=%s）", status, env.Msg)
	}
	out := env.dataMap(t)
	row := limitRow(t, free, "source", "fake_b")

	// 样本守卫：库里这行必须真的是默认档 100。若它哪天变成 -1，
	// 「响应 == 库」这个断言就会退化成"两边都是 -1"的空转（旧的那句字面量恰好也是 -1）。
	if row.Limit != 100 {
		t.Fatalf("库里 fake_b 的限额 = %d, want 100（free 的每源默认档）——本用例的对照值没了", row.Limit)
	}
	if got := int64(out["limit"].(float64)); got != row.Limit {
		t.Errorf("授权响应里的 limit = %d, 库里那行 = %d——响应必须报生效值，不是字面量", got, row.Limit)
	}
	if out["period"] != row.Period {
		t.Errorf("授权响应里的 period = %v, 库里那行 = %q——同样不许是字面量", out["period"], row.Period)
	}
}

// TestDecorativePeriodSpeaksAtLimitWrites 非 day 的周期在创建与更新两个写口各出声一次，
// 而 day（唯一与判定吻合的值）不再喊。
func TestDecorativePeriodSpeaksAtLimitWrites(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)
	free := planIDByCode(t, "free")
	spoken := captureLog(t)

	// 播种已把 fake_c 授权给 free；先回收才能走"新建"这一支
	if err := gate.UngrantPlanSource(free, "fake_c"); err != nil {
		t.Fatalf("回收 fake_c 失败: %v", err)
	}
	status, env := doJSON(t, srv, http.MethodPost, "/admin/quotas/limits", map[string]interface{}{
		"plan_id": free, "scope": "source", "target": "fake_c", "limit": 5,
	}, authHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("新增限额行失败（status=%d msg=%s）", status, env.Msg)
	}
	row := limitRow(t, free, "source", "fake_c")
	if row.Limit != 5 {
		t.Fatalf("通路没打通：limit = %d, want 5", row.Limit)
	}
	// 不带 period 的调用落的就是 month——**默认值恰好是那个不参与判定的值**，这条断言是本案的要点之一
	if row.Period != "month" {
		t.Fatalf("缺省 period = %q, want month（quotas.go 的默认值）——前提变了，后面的出声断言就得重看", row.Period)
	}
	if n := spoken("没有任何判定读这一列"); n != 1 {
		t.Errorf("创建写口出声 %d 条, want 1（且必须带「没有任何判定读这一列」那句归因）", n)
	}
	if n := spoken("额度窗口只有一个口径「当日」"); n != 1 {
		t.Errorf("那条 ERROR 没说明生效窗口是哪一个（%d 条）——只喊「不对」不给口径，读的人还得自己查", n)
	}

	id := itoa(row.ID)
	if status, env := doJSON(t, srv, http.MethodPut, "/admin/quotas/limits/"+id,
		map[string]interface{}{"limit": 6}, authHeader(admin)); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("更新限额行失败（status=%d msg=%s）", status, env.Msg)
	}
	if n := spoken("没有任何判定读这一列"); n != 2 {
		t.Errorf("更新写口没出声（累计 %d 条, want 2）——行还是 month，改限额也该说一次", n)
	}

	// 改成 day：与判定吻合的那个值，不该再新增一条
	if status, env := doJSON(t, srv, http.MethodPut, "/admin/quotas/limits/"+id,
		map[string]interface{}{"period": "day"}, authHeader(admin)); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("把 period 改回 day 失败（status=%d msg=%s）", status, env.Msg)
	}
	if row = limitRow(t, free, "source", fakeC); row.Period != "day" {
		t.Fatalf("period 没改成 day（得到 %q）——这一步没生效，下一条断言是空转", row.Period)
	}
	if n := spoken("没有任何判定读这一列"); n != 2 {
		t.Errorf("day 也被喊了（累计 %d 条, want 2）——day 是唯一与判定吻合的值，喊它等于把这条 ERROR 变噪音", n)
	}
}

// TestApplyGroupLimitsDropsPeriodButSpeaks 「按分组套用限额」这个接口收 period 参数却只写 limit：
// 面板上那个周期下拉今天改不动任何东西。本轮不悄悄补写（那是行为变更），只让它出声。
func TestApplyGroupLimitsDropsPeriodButSpeaks(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)

	group := createGroup(t, srv, admin, "周期下拉组", 41)
	if status, env := putGroupMembers(t, srv, admin, group, []string{fakeA, fakeB}); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("设置成员失败（status=%d msg=%s）", status, env.Msg)
	}
	beforeA := limitRow(t, planIDByCode(t, "free"), "source", fakeA)
	beforeB := limitRow(t, planIDByCode(t, "free"), "source", fakeB)
	// 样本守卫：两行都得是 day，否则"改不动"可能只是本来就等于请求值
	if beforeA.Period != "day" || beforeB.Period != "day" {
		t.Fatalf("播种的 period = %q / %q, want 两个都是 day——对照值没了，本用例验不出「参数被丢」",
			beforeA.Period, beforeB.Period)
	}
	spoken := captureLog(t)

	status, env := doJSON(t, srv, http.MethodPost, "/admin/source-groups/"+itoa(group)+"/apply-limits",
		map[string]interface{}{"plan_code": "free", "limit": 6, "period": "month"}, authHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("套用限额失败（status=%d msg=%s）", status, env.Msg)
	}
	if out := env.dataMap(t); out["applied"] != float64(2) {
		t.Fatalf("applied = %v, want 2（两行都没写就等于下面两条断言空转）", out["applied"])
	}
	free := planIDByCode(t, "free")
	for _, name := range []string{fakeA, fakeB} {
		row := limitRow(t, free, "source", name)
		if row.Limit != 6 {
			t.Errorf("%s 的 limit = %d, want 6——写路径没生效", name, row.Limit)
		}
		if row.Period != "day" {
			t.Errorf("%s 的 period 被写成了 %q, want 保持 day（这个接口只写 limit，本轮刻意不补）", name, row.Period)
		}
	}
	if n := spoken("这个接口只写 limit"); n != 1 {
		t.Errorf("丢弃 period 这件事出声 %d 条, want 1——面板那个下拉改不动任何东西，得由服务端说出来", n)
	}

	// 带 day 的请求不必喊（day 是它本来的值，也不与判定冲突）
	if status, env := doJSON(t, srv, http.MethodPost, "/admin/source-groups/"+itoa(group)+"/apply-limits",
		map[string]interface{}{"plan_code": "free", "limit": 7, "period": "day"}, authHeader(admin)); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("第二次套用限额失败（status=%d msg=%s）", status, env.Msg)
	}
	if n := spoken("这个接口只写 limit"); n != 1 {
		t.Errorf("period=day 也被喊了（累计 %d 条, want 1）", n)
	}
}
