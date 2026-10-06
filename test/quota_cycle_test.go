package test

// 单日额度的清零钟点可由本人切换（待办清单 P106）。
//
// 这个功能唯一的危险不在算法，在**它能给自己发额度**：切换只移动起算点、不冲正任何流水，
// 而起算点一旦后移，之前那段用量就不在窗口里了（UsedToday 正是按起算点求和）。
// 所以用例的重心是三条：① 默认方向必须还是自然日（升级前后口径一字不变）；
// ② 窗口长度仍然是「一日」——只挪钟点，不改成月；③ 30 天限频且"重复提交同一个值"不占名额。
//
// 判法沿用 platform_timezone_test.go 那条：**测试自己独立算一遍期望值**，
// 而不是拿被测函数验证被测函数（那样改错两边一起绿）。

import (
	"net/http"
	"testing"
	"time"

	"loomproxy/conf"
	"loomproxy/db"
	"loomproxy/gate"
	"loomproxy/models"
	"loomproxy/utils"
)

// probeAnchorStart 独立算一遍「最近一次到达 anchor 钟点」的时刻：只借 conf 里那个偏移数值，
// 不调用 utils.DailyAnchorStart / PlatformZone。
func probeAnchorStart(t *testing.T, anchor time.Time) time.Time {
	t.Helper()
	loc := time.FixedZone("probe", conf.Config.TZOffsetHours*3600)
	n := time.Now().In(loc)
	a := anchor.In(loc)
	want := time.Date(n.Year(), n.Month(), n.Day(), a.Hour(), a.Minute(), a.Second(), 0, loc)
	if n.Before(want) {
		want = want.AddDate(0, 0, -1)
	}
	return want
}

func TestDailyAnchorStartKeepsPlatformZone(t *testing.T) {
	newTestServer(t) // 装配 conf（TZ_OFFSET_HOURS 到位），否则 PlatformZone 读的是零值配置
	loc := time.FixedZone("probe", conf.Config.TZOffsetHours*3600)
	// 锚点取「现在这个钟点往前 3 小时」——必然已经过了，结果应落在今天
	anchor := time.Now().In(loc).Add(-3 * time.Hour)
	got := utils.DailyAnchorStart(anchor)
	want := probeAnchorStart(t, anchor)
	if !got.Equal(want) {
		t.Errorf("DailyAnchorStart = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
	if got.Location() != utils.PlatformZone() {
		t.Errorf("结果带的时区不是 utils.PlatformZone() 那一个对象（得到 %v）——日界算法又散落了一处", got.Location())
	}
	// 锚点还没到的那一头：取「现在往后 3 小时」→ 本轮应从**昨天**那一刻算
	future := time.Now().In(loc).Add(3 * time.Hour)
	now := time.Now()
	got2 := utils.DailyAnchorStart(future)
	if !got2.Before(now) {
		t.Errorf("锚点钟点还没过时，起算点应回推到昨天那一刻，实得 %s（在未来）", got2.Format(time.RFC3339))
	}
	// 正确的性质是「now 落在 [起算点, 起算点+24h)」——**不是** "回推 24 小时"：
	// 锚点离现在越近，回推出来的那一刻就越短（现在 03:33、锚点 06:33 → 回推的是昨天 06:33，只差 21h）。
	// 上一版本用例把这条写成"应接近 24h"，红的是用例自己，不是被测函数。
	if d := now.Sub(got2); d >= 24*time.Hour {
		t.Errorf("起算点距今 %v，已超过一轮——now 不在 [起算点, +24h) 里", d)
	}
	if a, g := future.In(loc).Format("15:04:05"), got2.In(loc).Format("15:04:05"); a != g {
		t.Errorf("起算点的钟点不是锚点钟点：anchor=%s got=%s", a, g)
	}
	// 退化情形：锚点 = 0 点 → 必须等于 utils.DayStart(1)（默认模式与新算法同值）
	midnight := time.Date(2020, 1, 1, 0, 0, 0, 0, loc)
	if got3 := utils.DailyAnchorStart(midnight); !got3.Equal(utils.DayStart(1)) {
		t.Errorf("锚点为 0 点时 DailyAnchorStart 应等于 DayStart(1)：%s vs %s", got3, utils.DayStart(1))
	}
}

// 起算点的三条规则：day（含空值）= 自然日；subscription = 注册钟点；管理员刷新仍压过两者。
func TestUsageSinceHonoursCycleMode(t *testing.T) {
	newTestServer(t)
	loc := utils.PlatformZone()
	// 注册时刻：故意取一个"今天的这个钟点还没到/已过"都可能的值，断言只看规则不看运气
	created := time.Now().In(loc).Add(-40 * 24 * time.Hour)

	dayUser := &models.User{CreatedAt: created, QuotaCycleMode: models.QuotaCycleDay}
	if got := gate.UsageSince(dayUser); !got.Equal(gate.StartOfDay()) {
		t.Errorf("day 模式的起算点应等于自然日零点：%s vs %s", got, gate.StartOfDay())
	}
	// **默认方向**：列值为空串（AutoMigrate 之前那批行的形状）必须仍算 day——
	// 默认值若是新行为，升级本身就成了给所有人改口径
	blank := &models.User{CreatedAt: created, QuotaCycleMode: ""}
	if got := gate.UsageSince(blank); !got.Equal(gate.StartOfDay()) {
		t.Errorf("空模式被当成非 day 处理（起算点 %s）——默认方向漂了", got)
	}

	sub := &models.User{CreatedAt: created, QuotaCycleMode: models.QuotaCycleSubscription}
	want := probeAnchorStart(t, created)
	if got := gate.UsageSince(sub); !got.Equal(want) {
		t.Errorf("subscription 模式起算点 = %s, want %s（注册钟点）", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
	// 管理员刷新（P41）压过模式：两者都比锚点晚时，起算点是刷新时刻
	reset := time.Now().Add(1 * time.Hour)
	sub.QuotaResetAt = &reset
	if got := gate.UsageSince(sub); !got.Equal(reset) {
		t.Errorf("管理员刷新时刻没压过锚点：得到 %s, want %s", got.Format(time.RFC3339), reset.Format(time.RFC3339))
	}
}

// 窗口长度不许变：切到 subscription 之后，「下一次清零」与「本轮起算点」之间仍是 24 小时。
// 这一条防的是最容易被顺手改错的方向——把它做成"从订阅日起算一个月"（P70 那条决定就不是随手能改的）。
func TestQuotaCycleSwitchKeeps24hWindow(t *testing.T) {
	srv := newTestServer(t)
	token := registerUser(t, srv, "cycle_u1", "cycle_u1@example.com", "pass1234")

	status, env := doJSON(t, srv, http.MethodPost, "/auth/quota-cycle", map[string]interface{}{"mode": "subscription"}, authHeader(token))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("切到 subscription 失败（status=%d msg=%s）", status, env.Msg)
	}
	data := env.dataMap(t)
	if data["mode"] != "subscription" || data["changed"] != true {
		t.Errorf("响应没报到切换结果：%v", data)
	}
	if anchor, _ := data["anchor"].(string); len(anchor) != 5 || anchor[2] != ':' {
		t.Errorf("anchor 应是平台时区的钟点（HH:MM），实得 %v", data["anchor"])
	}

	var u models.User
	if err := db.DB.Where("username = ?", "cycle_u1").First(&u).Error; err != nil {
		t.Fatalf("读不回用户: %v", err)
	}
	if u.EffectiveQuotaCycleMode() != models.QuotaCycleSubscription {
		t.Errorf("库里没落上：mode=%q", u.QuotaCycleMode)
	}
	if u.QuotaCycleChangedAt == nil {
		t.Error("模式真变时没盖 quota_cycle_changed_at（限频就失去依据）")
	}
	since := gate.UsageSince(&u)
	if d := time.Since(since); d > 24*time.Hour+time.Minute {
		t.Errorf("窗口长度不再是 24h：本轮起算点距现在 %v", d)
	}
	// /auth/me 下发有效值与锚点，面板不自己算
	status, env = doJSON(t, srv, http.MethodGet, "/auth/me", nil, authHeader(token))
	if status != http.StatusOK {
		t.Fatalf("读 /auth/me 失败（status=%d）", status)
	}
	me := env.dataMap(t)
	if me["quota_cycle_mode"] != "subscription" {
		t.Errorf("/auth/me 没下发 quota_cycle_mode：%v", me["quota_cycle_mode"])
	}
	if a, _ := me["quota_cycle_anchor"].(string); a == "" {
		t.Error("/auth/me 的 quota_cycle_anchor 是空的（面板就没法显示实际钟点）")
	}
	// dashboard 的 next_reset 必须跟着新钟点，且仍等于「起算点 + 24h」
	status, env = doJSON(t, srv, http.MethodGet, "/quota/dashboard", nil, authHeader(token))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("读 dashboard 失败（status=%d msg=%s）", status, env.Msg)
	}
	list, ok := env.dataMap(t)["sources"].([]interface{})
	if !ok || len(list) == 0 {
		t.Fatalf("dashboard 的 sources 是空的——next_reset 挂在源卡上，取不到就等于断言空转")
	}
	item, _ := list[0].(map[string]interface{})
	want := since.Add(24 * time.Hour).Format("2006/1/2 15:04:05")
	if item["next_reset"] != want {
		t.Errorf("subscription 模式下的 next_reset = %v, want %s（起算点 + 24h）", item["next_reset"], want)
	}
}

// 限频与幂等：重复提交同一个值**不算切换**（不盖时刻、不占名额）；真切换后立刻再切回要被拒。
func TestQuotaCycleSwitchIsRateLimited(t *testing.T) {
	srv := newTestServer(t)
	token := registerUser(t, srv, "cycle_u2", "cycle_u2@example.com", "pass1234")

	if status, env := doJSON(t, srv, http.MethodPost, "/auth/quota-cycle", map[string]interface{}{"mode": "monthly"}, authHeader(token)); status != http.StatusBadRequest {
		t.Errorf("非法 mode 应 400，实得 status=%d msg=%s", status, env.Msg)
	}
	// 首次切换
	if status, env := doJSON(t, srv, http.MethodPost, "/auth/quota-cycle", map[string]interface{}{"mode": "subscription"}, authHeader(token)); status != http.StatusOK {
		t.Fatalf("首次切换应 200，实得 status=%d msg=%s", status, env.Msg)
	}
	var u models.User
	if err := db.DB.Where("username = ?", "cycle_u2").First(&u).Error; err != nil {
		t.Fatalf("读不回用户: %v", err)
	}
	stamped := *u.QuotaCycleChangedAt

	// 重复提交同一个值：200、changed=false，且**时刻不许被刷新**（否则每次重复都续上 30 天，
	// 而 P52 那条更根本：给没变的状态盖时刻等于让那列说假话）
	status, env := doJSON(t, srv, http.MethodPost, "/auth/quota-cycle", map[string]interface{}{"mode": "subscription"}, authHeader(token))
	if status != http.StatusOK {
		t.Fatalf("重复提交同值应 200，实得 status=%d msg=%s", status, env.Msg)
	}
	if env.dataMap(t)["changed"] != false {
		t.Errorf("重复提交被当成变更：%v", env.dataMap(t))
	}
	if err := db.DB.Where("username = ?", "cycle_u2").First(&u).Error; err != nil {
		t.Fatalf("再读用户失败: %v", err)
	}
	if !u.QuotaCycleChangedAt.Equal(stamped) {
		t.Errorf("重复提交把限频时刻刷到了 %v（原 %v）——重复请求正在吃掉用户的切换名额", u.QuotaCycleChangedAt, stamped)
	}

	// 真切换后立刻切回：429，并且**不许落库**
	if status, env = doJSON(t, srv, http.MethodPost, "/auth/quota-cycle", map[string]interface{}{"mode": "day"}, authHeader(token)); status != http.StatusTooManyRequests {
		t.Fatalf("30 天内第二次切换应 429，实得 status=%d msg=%s", status, env.Msg)
	}
	if err := db.DB.Where("username = ?", "cycle_u2").First(&u).Error; err != nil || u.QuotaCycleMode != models.QuotaCycleSubscription {
		t.Errorf("被拒的切换仍然改了库：mode=%q err=%v", u.QuotaCycleMode, err)
	}

	// 冷却期一过就放行：把时刻改到 31 天前（测试自己造的样本，不动别人的行）
	back := stamped.Add(-31 * 24 * time.Hour)
	if err := db.DB.Model(&models.User{}).Where(map[string]interface{}{"id": u.ID}).
		Update("quota_cycle_changed_at", back).Error; err != nil {
		t.Fatalf("造冷却样本失败: %v", err)
	}
	if status, env = doJSON(t, srv, http.MethodPost, "/auth/quota-cycle", map[string]interface{}{"mode": "day"}, authHeader(token)); status != http.StatusOK {
		t.Errorf("冷却期满应放行，实得 status=%d msg=%s", status, env.Msg)
	}
}
