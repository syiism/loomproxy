package groupb

// 额度转移（待办清单 P97 落地）的黑盒用例。
//
// 重心不在"能不能转"，在四件**错了不报错**的事：
//   ① 写回的是**覆盖增量**而不是有效额度（差一个套餐限额，接口一路 200、额度却凭空变多——
//      第一版实现就是这么错的，靠这条钉住）；
//   ② 增量落到 0 时必须**删行**，不是留一行 0（0 的语义是"没有覆盖"，留着它 P46 的
//      「被手工刷过额度」筛选会把没刷过的人数进去）；
//   ③ 两端之和守恒（转出去再转回来不产生新额度）；
//   ④ 被拒的转移**一行都不许落库**（额度没动却留下审计，比没审计更坏）。

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"loomproxy/db"
	"loomproxy/gate"
	"loomproxy/models"
)

// ensureGrant 把某个套餐对某个源的限额摆成用例需要的形状（测试自己造前置，不靠播种的巧合）。
func ensureGrant(t *testing.T, planID uint, code string, limit int64) {
	t.Helper()
	var row models.QuotaLimit
	err := db.DB.Where(map[string]interface{}{"plan_id": planID, "scope": "source", "target": code}).
		First(&row).Error
	if err == nil {
		if err := db.DB.Model(&row).Updates(map[string]interface{}{"limit": limit}).Error; err != nil {
			t.Fatalf("改限额失败 %s=%d: %v", code, limit, err)
		}
		return
	}
	if err != nil {
		// 没有这一行 → 建一行（scope=source 的行同时承担"授权"，P34）
		if err := db.DB.Create(&models.QuotaLimit{PlanID: planID, Scope: "source", Target: code, Limit: limit}).Error; err != nil {
			t.Fatalf("建限额行失败 %s=%d: %v", code, limit, err)
		}
	}
}

func dropGrant(t *testing.T, planID uint, code string) {
	t.Helper()
	if err := db.DB.Where("plan_id = ? AND scope = ? AND target = ?", planID, "source", code).
		Delete(&models.QuotaLimit{}).Error; err != nil {
		t.Fatalf("删授权行失败 %s: %v", code, err)
	}
}

func transferOf(t *testing.T, userID uint, code string) (int64, bool) {
	t.Helper()
	var row models.UserQuotaOverride
	err := db.DB.Where(map[string]interface{}{"user_id": userID, "group_code": code}).First(&row).Error
	if err != nil {
		return 0, false
	}
	return row.Limit, true
}

func auditRows(t *testing.T, userID uint) []models.QuotaTransferLog {
	t.Helper()
	var rows []models.QuotaTransferLog
	if err := db.DB.Where("user_id = ?", userID).Order("id asc").Find(&rows).Error; err != nil {
		t.Fatalf("读审计行失败: %v", err)
	}
	return rows
}

// 本人端一次成功转移：写回的是增量、两端守恒、审计留下一行。
func TestQuotaTransferSelf(t *testing.T) {
	srv := newTestServer(t)
	token := registerUser(t, srv, "tr_u1", "tr_u1@example.com", "pass1234")

	var u models.User
	if err := db.DB.Where("username = ?", "tr_u1").First(&u).Error; err != nil {
		t.Fatalf("读不回用户: %v", err)
	}
	plan := gate.ResolvePlanForUser(&u)
	ensureGrant(t, plan.ID, "fake_a", 100)
	ensureGrant(t, plan.ID, "fake_b", 40)
	limits := gate.PlanSourceLimits(plan.ID)
	beforeA, beforeB := limits["fake_a"], limits["fake_b"]
	if beforeA != 100 || beforeB != 40 {
		t.Fatalf("前置限额没摆好：fake_a=%v fake_b=%v", beforeA, beforeB)
	}

	status, env := doJSON(t, srv, http.MethodPost, "/quota/transfer",
		map[string]interface{}{"from": "fake_a", "to": "fake_b", "amount": 20}, authHeader(token))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("转移应 200，实得 status=%d msg=%s", status, env.Msg)
	}
	d := env.dataMap(t)
	from, _ := d["from"].(map[string]interface{})
	to, _ := d["to"].(map[string]interface{})
	if from["after"] != float64(80) || to["after"] != float64(60) {
		t.Errorf("响应里的前后额度不对：from=%v to=%v", from, to)
	}
	// **核心那一格**：写回的是增量（-20 / +20），不是有效额度（80 / 60）——第一版错在这里
	if ov, ok := transferOf(t, u.ID, "fake_a"); !ok || ov != -20 {
		t.Errorf("fake_a 的覆盖增量 = %d（存在=%v），want -20（有效额度是 80，把 80 写进去就是凭空造额度）", ov, ok)
	}
	if ov, ok := transferOf(t, u.ID, "fake_b"); !ok || ov != 20 {
		t.Errorf("fake_b 的覆盖增量 = %d（存在=%v），want 20", ov, ok)
	}
	// 守恒：两端有效额度之和不变
	if sum := gate.EffectiveSourceLimit(&u, "fake_a", limits) + gate.EffectiveSourceLimit(&u, "fake_b", limits); sum != 140 {
		t.Errorf("转移后两端之和 = %d, want 140（守恒破了就是凭空造/消灭额度）", sum)
	}
	rows := auditRows(t, u.ID)
	if len(rows) != 1 {
		t.Fatalf("审计行应有 1 条，实得 %d", len(rows))
	}
	if rows[0].Via != "self" || rows[0].OperatorID != u.ID || rows[0].Amount != 20 {
		t.Errorf("审计行形状不对：via=%q operator=%d amount=%d", rows[0].Via, rows[0].OperatorID, rows[0].Amount)
	}
	// （这里原本还断言过一次"响应要带 user_id"——那是我的断言过度：本人端调用者知道自己是谁，
	// 为一个断言去加响应字段是反过来的。管理端那一条才有意义，见 TestQuotaTransferAdminEntry。）
}

// 增量正好落到 0 时必须删行；而"转空"是写 -限额，不是删行。
func TestQuotaTransferZeroSentinel(t *testing.T) {
	srv := newTestServer(t)
	token := registerUser(t, srv, "tr_u2", "tr_u2@example.com", "pass1234")
	var u models.User
	if err := db.DB.Where("username = ?", "tr_u2").First(&u).Error; err != nil {
		t.Fatalf("读不回用户: %v", err)
	}
	plan := gate.ResolvePlanForUser(&u)
	ensureGrant(t, plan.ID, "fake_a", 100)
	ensureGrant(t, plan.ID, "fake_b", 40)

	// 转出 20、再转回 20：覆盖行必须**消失**（增量为 0 的语义是"没有覆盖"）
	for _, step := range [][2]string{{"fake_a", "fake_b"}, {"fake_b", "fake_a"}} {
		if status, env := doJSON(t, srv, http.MethodPost, "/quota/transfer",
			map[string]interface{}{"from": step[0], "to": step[1], "amount": 20}, authHeader(token)); status != http.StatusOK {
			t.Fatalf("%s→%s 转移失败 status=%d msg=%s", step[0], step[1], status, env.Msg)
		}
	}
	if ov, ok := transferOf(t, u.ID, "fake_a"); ok {
		t.Errorf("转回原状后 fake_a 仍留着一行覆盖（limit=%d）——**0 该用删行表达**，留着它会进「被手工刷过额度」的筛选", ov)
	}
	if ov, ok := transferOf(t, u.ID, "fake_b"); ok {
		t.Errorf("转回原状后 fake_b 仍留着一行覆盖（limit=%d）", ov)
	}

	// 把 fake_a 整个转空：有效额度 0 = 覆盖 -100（**不是删行**，删行等于回套餐全额）
	if status, env := doJSON(t, srv, http.MethodPost, "/quota/transfer",
		map[string]interface{}{"from": "fake_a", "to": "fake_b", "amount": 100}, authHeader(token)); status != http.StatusOK {
		t.Fatalf("转空失败 status=%d msg=%s", status, env.Msg)
	}
	limits := gate.PlanSourceLimits(plan.ID)
	if got := gate.EffectiveSourceLimit(&u, "fake_a", limits); got != 0 {
		t.Errorf("转空后 fake_a 的有效额度 = %d, want 0", got)
	}
	if ov, ok := transferOf(t, u.ID, "fake_a"); !ok || ov != -100 {
		t.Errorf("转空要写 -限额（-100）而不是删行，实得 ov=%d 存在=%v", ov, ok)
	}
}

// 四条拒绝：句子固定、**一行都不许落库**。
func TestQuotaTransferRejections(t *testing.T) {
	srv := newTestServer(t)
	token := registerUser(t, srv, "tr_u3", "tr_u3@example.com", "pass1234")
	var u models.User
	if err := db.DB.Where("username = ?", "tr_u3").First(&u).Error; err != nil {
		t.Fatalf("读不回用户: %v", err)
	}
	plan := gate.ResolvePlanForUser(&u)
	ensureGrant(t, plan.ID, "fake_a", 100)
	ensureGrant(t, plan.ID, "fake_b", 40)

	cases := []struct {
		name        string
		body        map[string]interface{}
		wantMsgPart string
		prep        func()
		restore     func()
	}{
		{"零数量", map[string]interface{}{"from": "fake_a", "to": "fake_b", "amount": 0}, "正整数", nil, nil},
		{"负数量", map[string]interface{}{"from": "fake_a", "to": "fake_b", "amount": -5}, "正整数", nil, nil},
		{"同源自转", map[string]interface{}{"from": "fake_a", "to": "fake_a", "amount": 5}, "同一个数据源", nil, nil},
		{"不存在的源", map[string]interface{}{"from": "fake_a", "to": "no_such_source", "amount": 5}, "数据源不存在", nil, nil},
		{"超额转出", map[string]interface{}{"from": "fake_a", "to": "fake_b", "amount": 101}, "限额不足", nil, nil},
		{"不限额的源", map[string]interface{}{"from": "fake_a", "to": "fake_b", "amount": 5}, "不限额",
			func() { ensureGrant(t, plan.ID, "fake_a", -1) }, func() { ensureGrant(t, plan.ID, "fake_a", 100) }},
		{"未授权的目标", map[string]interface{}{"from": "fake_a", "to": "fake_b", "amount": 5}, "未授权",
			func() { dropGrant(t, plan.ID, "fake_b") }, func() { ensureGrant(t, plan.ID, "fake_b", 40) }},
	}
	for _, c := range cases {
		if c.prep != nil {
			c.prep()
		}
		status, env := doJSON(t, srv, http.MethodPost, "/quota/transfer", c.body, authHeader(token))
		if status != http.StatusBadRequest {
			t.Errorf("%s：应 400，实得 %d（msg=%s）", c.name, status, env.Msg)
		}
		if !strings.Contains(env.Msg, c.wantMsgPart) {
			t.Errorf("%s：句子要含「%s」，实得 %q", c.name, c.wantMsgPart, env.Msg)
		}
		// 对外不许带内部形状（P90：表名/列名/驱动原文都不进响应）
		for _, leak := range []string{"quota_limits", "user_quota_overrides", "Error 1", "near(\""} {
			if strings.Contains(env.Msg, leak) {
				t.Errorf("%s：错误句里漏出了内部形状 %q", c.name, leak)
			}
		}
		if c.restore != nil {
			c.restore()
		}
	}
	if rows := auditRows(t, u.ID); len(rows) != 0 {
		t.Errorf("被拒的转移留下了 %d 条审计行——额度没动却留下记录，比没审计更坏", len(rows))
	}
	if _, ok := transferOf(t, u.ID, "fake_a"); ok {
		t.Error("被拒的转移改了 fake_a 的覆盖行")
	}
}

// 这条端点**只认会话**：apiKey 与匿名都要被拒（§7 那一组的边界，与 P106 同一条理由）。
func TestQuotaTransferNeedsSession(t *testing.T) {
	srv := newTestServer(t)
	token := registerUser(t, srv, "tr_u4", "tr_u4@example.com", "pass1234")
	var u models.User
	if err := db.DB.Where("username = ?", "tr_u4").First(&u).Error; err != nil {
		t.Fatalf("读不回用户: %v", err)
	}
	plan := gate.ResolvePlanForUser(&u)
	ensureGrant(t, plan.ID, "fake_a", 100)
	ensureGrant(t, plan.ID, "fake_b", 40)

	status, env := doJSON(t, srv, http.MethodPost, "/apikey", map[string]string{"name": "tr"}, authHeader(token))
	if status != http.StatusOK {
		t.Fatalf("创建密钥失败 status=%d", status)
	}
	key, _ := env.dataMap(t)["key"].(string)
	if !strings.HasPrefix(key, "lp_") {
		t.Fatalf("密钥格式异常: %q", key)
	}
	// 先证明这把密钥**本身是有效的**：同一把 key 打同组的只读端点必须 200。
	// 少了这一步，下面的 401 就只可能因为"密钥根本没用对"而通过——
	// （第一版正是这样：我写成了 `Authorization: Bearer lp_…`，那个传输方式根本不被认，
	// 于是断言为一个错误的理由长期通过，直到变异验证才暴露。）
	if status, _ = doJSON(t, srv, http.MethodGet, "/quota/dashboard", nil, map[string]string{"X-API-Key": key}); status != http.StatusOK {
		t.Fatalf("这把密钥连 /quota/dashboard 都过不去（status=%d）——后面的 401 断言没有意义", status)
	}
	body := map[string]interface{}{"from": "fake_a", "to": "fake_b", "amount": 10}
	if status, _ = doJSON(t, srv, http.MethodPost, "/quota/transfer", body,
		map[string]string{"X-API-Key": key}); status != http.StatusUnauthorized {
		t.Errorf("apiKey 走本人端点应 401，实得 %d——长期密钥不该能改额度分布", status)
	}
	if status, _ = doJSON(t, srv, http.MethodPost, "/quota/transfer", body, nil); status != http.StatusUnauthorized {
		t.Errorf("匿名应 401，实得 %d", status)
	}
	if rows := auditRows(t, u.ID); len(rows) != 0 {
		t.Errorf("被凭证拦下的请求留下了审计行（%d 条）", len(rows))
	}
}

// 管理端入口：同一套判定，区别只在审计行的 via 与 operator_id，以及响应带的是**目标用户**。
func TestQuotaTransferAdminEntry(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)
	userToken := registerUser(t, srv, "tr_u5", "tr_u5@example.com", "pass1234")
	var u models.User
	if err := db.DB.Where("username = ?", "tr_u5").First(&u).Error; err != nil {
		t.Fatalf("读不回用户: %v", err)
	}
	plan := gate.ResolvePlanForUser(&u)
	ensureGrant(t, plan.ID, "fake_a", 100)
	ensureGrant(t, plan.ID, "fake_b", 40)
	var op models.User
	if err := db.DB.Where("username = ?", "admin").First(&op).Error; err != nil {
		t.Fatalf("读不回管理员: %v", err)
	}

	status, env := doJSON(t, srv, http.MethodPost, "/admin/users/"+fmt.Sprintf("%d", u.ID)+"/quota-transfer",
		map[string]interface{}{"from": "fake_a", "to": "fake_b", "amount": 30}, authHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("管理端转移应 200，实得 status=%d msg=%s", status, env.Msg)
	}
	d := env.dataMap(t)
	if d["user_id"] != float64(u.ID) {
		t.Errorf("响应里的 user_id 要是被操作的那个人，实得 %v（want %d）", d["user_id"], u.ID)
	}
	rows := auditRows(t, u.ID)
	if len(rows) != 1 {
		t.Fatalf("审计行应有 1 条，实得 %d", len(rows))
	}
	if rows[0].Via != "admin" || rows[0].OperatorID != op.ID || rows[0].UserID != u.ID {
		t.Errorf("审计行没记下是谁动的：via=%q operator=%d user=%d", rows[0].Via, rows[0].OperatorID, rows[0].UserID)
	}
	// 管理员也只能挪这个人的两个源：判定共用同一条，不因为来自 /admin/* 就放宽
	if status, _ = doJSON(t, srv, http.MethodPost, "/admin/users/"+fmt.Sprintf("%d", u.ID)+"/quota-transfer",
		map[string]interface{}{"from": "fake_a", "to": "fake_b", "amount": 1000}, authHeader(admin)); status != http.StatusBadRequest {
		t.Errorf("管理端超额应 400，实得 %d", status)
	}
	_ = userToken
}

// 「转移是永久的」这条不能靠我读一遍代码下结论：
// 它要断的是**跨过一次日界之后仍然生效**，以及**管理员刷新额度也不把它抹掉**。
// 挪的是流水的时间戳，不改系统时钟（改时钟会污染同一进程里的其它用例，P57 那一族）。
func TestQuotaTransferPersistsAcrossDays(t *testing.T) {
	srv := newTestServer(t)
	token := registerUser(t, srv, "tr_p1", "tr_p1@example.com", "pass1234")
	admin := adminToken(t, srv)
	var u models.User
	if err := db.DB.Where("username = ?", "tr_p1").First(&u).Error; err != nil {
		t.Fatalf("读不回用户: %v", err)
	}
	plan := gate.ResolvePlanForUser(&u)
	ensureGrant(t, plan.ID, "fake_a", 100)
	ensureGrant(t, plan.ID, "fake_b", 40)

	if status, env := doJSON(t, srv, http.MethodPost, "/quota/transfer",
		map[string]interface{}{"from": "fake_a", "to": "fake_b", "amount": 20}, authHeader(token)); status != http.StatusOK {
		t.Fatalf("转移失败 status=%d msg=%s", status, env.Msg)
	}
	limits := gate.PlanSourceLimits(plan.ID)

	log := models.QuotaUsageLog{UserID: u.ID, GroupCode: "fake_a", Cost: 5}
	if err := db.DB.Create(&log).Error; err != nil {
		t.Fatalf("写用量流水失败: %v", err)
	}
	var now models.User
	if err := db.DB.First(&now, u.ID).Error; err != nil {
		t.Fatalf("重读用户失败: %v", err)
	}
	if got := gate.UsedToday(&now, "fake_a"); got != 5 {
		t.Fatalf("前置不成立：当日已用 = %d, want 5（后面那条「跨日」断言会空转）", got)
	}
	if got := gate.EffectiveSourceLimit(&now, "fake_a", limits); got != 80 {
		t.Fatalf("转移后 fake_a 的限额 = %d, want 80", got)
	}

	// 把这条用量挪到 3 天前 = 模拟"过了日界、第二天刷新生效"
	if err := db.DB.Model(&models.QuotaUsageLog{}).Where(map[string]interface{}{"id": log.ID}).
		Update("created_at", time.Now().AddDate(0, 0, -3)).Error; err != nil {
		t.Fatalf("挪流水时间失败: %v", err)
	}
	if got := gate.UsedToday(&now, "fake_a"); got != 0 {
		t.Errorf("过日后当日已用应归 0，实得 %d（这条不成立就说明日界判定没读流水时间）", got)
	}
	// **核心那条**：用量归零了，限额却没回到 100——转移是永久的
	if got := gate.EffectiveSourceLimit(&now, "fake_a", limits); got != 80 {
		t.Errorf("跨日之后 fake_a 的限额 = %d, want 80（回到 100 就说明转移跟着日界失效了）", got)
	}
	if got := gate.EffectiveSourceLimit(&now, "fake_b", limits); got != 60 {
		t.Errorf("跨日之后 fake_b 的限额 = %d, want 60（转入量没留住）", got)
	}

	// 管理员那次「刷新额度」只该动起算点，不许顺手把覆盖清掉
	if status, env := doJSON(t, srv, http.MethodPost, "/admin/users/"+fmt.Sprintf("%d", u.ID)+"/refresh-quota", nil, authHeader(admin)); status != http.StatusOK {
		t.Fatalf("刷新额度失败 status=%d msg=%s", status, env.Msg)
	}
	if ov, ok := transferOf(t, u.ID, "fake_a"); !ok || ov != -20 {
		t.Errorf("刷新额度之后 fake_a 的覆盖被改动：ov=%d 存在=%v, want -20", ov, ok)
	}
	if ov, ok := transferOf(t, u.ID, "fake_b"); !ok || ov != 20 {
		t.Errorf("刷新额度之后 fake_b 的覆盖被改动：ov=%d 存在=%v, want 20", ov, ok)
	}
}

// 只读查看口：本人看自己的（不带操作者身份），管理端看别人的（带），且**互相看不到别人的记录**。
func TestQuotaTransferReadViews(t *testing.T) {
	srv := newTestServer(t)
	owner := registerUser(t, srv, "tr_v1", "tr_v1@example.com", "pass1234")
	other := registerUser(t, srv, "tr_v2", "tr_v2@example.com", "pass1234")
	admin := adminToken(t, srv)

	var u models.User
	if err := db.DB.Where("username = ?", "tr_v1").First(&u).Error; err != nil {
		t.Fatalf("读不回用户: %v", err)
	}
	plan := gate.ResolvePlanForUser(&u)
	ensureGrant(t, plan.ID, "fake_a", 100)
	ensureGrant(t, plan.ID, "fake_b", 40)
	if status, env := doJSON(t, srv, http.MethodPost, "/quota/transfer",
		map[string]interface{}{"from": "fake_a", "to": "fake_b", "amount": 80}, authHeader(owner)); status != http.StatusOK {
		t.Fatalf("转移失败 status=%d msg=%s", status, env.Msg)
	}

	status, env := doJSON(t, srv, http.MethodGet, "/quota/transfers", nil, authHeader(owner))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("本人读转移史失败 status=%d msg=%s", status, env.Msg)
	}
	d := env.dataMap(t)
	list, _ := d["list"].([]interface{})
	if len(list) != 1 {
		t.Fatalf("本人应看到 1 条，实得 %d", len(list))
	}
	row, _ := list[0].(map[string]interface{})
	if row["from"] != "fake_a" || row["to"] != "fake_b" || row["amount"] != float64(80) {
		t.Errorf("记录内容不对：%v", row)
	}
	if row["via"] != "self" {
		t.Errorf("via = %v, want self", row["via"])
	}
	// 本人面**不给操作者身份**：管理员账号名不是这个人需要知道的信息
	if _, leaked := row["operator_id"]; leaked {
		t.Error("本人面把 operator_id 发出去了——只给 via 这一格就够")
	}

	// 越权那条最贵：另一个用户读同一条端点，必须一条都看不到
	status, env = doJSON(t, srv, http.MethodGet, "/quota/transfers", nil, authHeader(other))
	if status != http.StatusOK {
		t.Fatalf("别人读自己的转移史应 200（空表），实得 %d", status)
	}
	if got := len(env.dataMap(t)["list"].([]interface{})); got != 0 {
		t.Errorf("别人看到了这个用户的转移记录（%d 条）——查询没按 user_id 夹住", got)
	}

	// 管理员再挪一笔（fake_b→fake_a 20）：管理端的操作者名字两条路都要走到——
	// 自助那一行的操作者是本人自己（tr_v1），管理端那一行才是 admin。原先这里只发了一笔
	// 自助转移却硬性期望 operator=="admin"，是「为错误的理由写绿」的断言（P97 落地教训②同族）。
	if status, env := doJSON(t, srv, http.MethodPost, "/admin/users/"+fmt.Sprintf("%d", u.ID)+"/quota-transfer",
		map[string]interface{}{"from": "fake_b", "to": "fake_a", "amount": 20}, authHeader(admin)); status != http.StatusOK {
		t.Fatalf("管理端转移失败 status=%d msg=%s", status, env.Msg)
	}
	// 管理端：带操作者账号名，并且审计里的写回增量说的是真话
	status, env = doJSON(t, srv, http.MethodGet, "/admin/users/"+fmt.Sprintf("%d", u.ID)+"/quota-transfers", nil, authHeader(admin))
	if status != http.StatusOK {
		t.Fatalf("管理端读转移史失败 status=%d", status)
	}
	arows, _ := env.dataMap(t)["list"].([]interface{})
	if len(arows) != 2 {
		t.Fatalf("管理端应看到 2 条，实得 %d", len(arows))
	}
	operatorOf := func(amount float64) interface{} {
		for _, r := range arows {
			m, _ := r.(map[string]interface{})
			if m["amount"] == amount {
				return m["operator"]
			}
		}
		return nil
	}
	if got := operatorOf(80); got != "tr_v1" {
		t.Errorf("自助转移的操作者应是本人自己（tr_v1），实得 %v", got)
	}
	if got := operatorOf(20); got != "admin" {
		t.Errorf("管理端转移的操作者应是 admin，实得 %v", got)
	}
	var arow map[string]interface{}
	for _, r := range arows {
		m, _ := r.(map[string]interface{})
		if m["amount"] == float64(80) {
			arow = m
		}
	}
	if arow["from_override"] != float64(-80) {
		t.Errorf("审计里的写回增量 = %v, want -80", arow["from_override"])
	}
	status, env = doJSON(t, srv, http.MethodGet, "/quota/dashboard", nil, authHeader(owner))
	if status != http.StatusOK {
		t.Fatalf("读 dashboard 失败 status=%d", status)
	}
	sources, _ := env.dataMap(t)["sources"].([]interface{})
	if len(sources) == 0 {
		t.Fatal("dashboard 的 sources 是空的——下面那条 override 断言会空转")
	}
	var label string
	for _, s := range sources {
		m, _ := s.(map[string]interface{})
		if m["source_code"] == "fake_a" {
			label, _ = m["override"].(string)
		}
	}
	if label != "-60" {
		t.Errorf("源卡上的永久调整 = %q, want \"-60\"——两笔转移后 fake_a 净减 60（转走 80、转回 20），"+
			"被转走额度的源必须看得见这件事，否则半年后回来的人只会怀疑源坏了", label)
	}
}
