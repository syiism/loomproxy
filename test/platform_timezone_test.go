package test

// 平台时区与日界只有一处定义（待办清单 P71）。
//
// 第十四遍巡检数出来的是七种写法：四处写死 FixedZone("CST", 8*3600)（额度日界、验证码日限、
// 距下次重置、统计面板今日新增）、三处跟着进程时区 time.Local（榜单窗口、趋势图日标、名称回填窗口），
// 而 TZ_OFFSET_HOURS 这个配置只有格式化那一处读。现网系统时区是 Asia/Shanghai、配置也是 8，
// 三种口径恰好重合，所以这条**不会自己报错**——换到 UTC 机器上，「今日额度」与「今日趋势」就不是同一天。
//
// 用例的判法（两条都不是"改全局"，原因写在下面）：
//   ① **返回值的 Location 身份**必须就是 `utils.PlatformZone()` 那一个对象——写死一个新 `FixedZone`
//      或改回 `time.Local` 都会拿到另一个指针，当场红；
//   ② 那一刻必须是平台时区的零点，且与端点读到的日界（趋势图日标、面板「距下次重置」）一致。
//
// 为什么不用"改 conf 的偏移"或"改 time.Local"这两种更直接的做法：
// 二者都是跨用例共享的全局，而 httptest 服务器里每来一个请求就有一次 `time.Now()`——
// **`time.Now()` 自己就读 `time.Local`**，第一版这么写立刻被 `-race` 拦下（P57 同一族的第三种形态）。
// 所以"跟着进程时区算"这一支在用例里靠不住，只能由 `make vet` 的 `tz-check` 静态兜住；
// 端点那两条断言能抓住的是"写成了另一个偏移"，抓不住"改回 time.Local"（宿主机恰好 +8 时两者同值）——
// 这个分工是刻意的，写在这里免得下一轮以为端点断言已经覆盖了全部七处。

import (
	"net/http"
	"testing"
	"time"

	"loomproxy/conf"
	"loomproxy/gate"
	"loomproxy/handlers/subjectrank"
	"loomproxy/utils"
)

// platformDay 独立算一遍平台时区的「今天零点 / 明天零点」：
// 只借用 conf 里那个**数值**，不复用被测的 DayStart——否则就是拿被测函数验证被测函数。
func platformDay() (time.Time, time.Time) {
	loc := time.FixedZone("probe", conf.Config.TZOffsetHours*3600)
	n := time.Now().In(loc)
	today := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc)
	return today, today.Add(24 * time.Hour)
}

// assertPlatformMidnight 一处日界既要等于平台时区零点，也要**带着平台时区那个对象**回来。
func assertPlatformMidnight(t *testing.T, name string, got, want time.Time) {
	t.Helper()
	if !got.Equal(want) {
		t.Errorf("%s = %s, want 平台时区今天零点 %s（差 %v）",
			name, got.Format(time.RFC3339), want.Format(time.RFC3339), got.Sub(want))
	}
	if got.Location() != utils.PlatformZone() {
		t.Errorf("%s 带着的时区不是 utils.PlatformZone() 那一个对象（得到 %v）——"+
			"说明这一处自己又造了一个时区（写死偏移或跟进程时区），日界迟早会漂", name, got.Location())
	}
}

func TestDayBoundariesFollowPlatformZone(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)

	today, tomorrow := platformDay()

	// ① 额度那一侧的门面（UsageSince 用它）——原本每次新建一个写死 +8 的 Location
	assertPlatformMidnight(t, "gate.StartOfDay()", gate.StartOfDay(), today)
	// ② 榜单窗口：当日与近 7 天——原本用 time.Local
	assertPlatformMidnight(t, "subjectrank.WindowStart(1)", subjectrank.WindowStart(1), today)
	if got, want := subjectrank.WindowStart(7), today.AddDate(0, 0, -6); !got.Equal(want) {
		t.Errorf("subjectrank.WindowStart(7) = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
	if got := utils.DayStart(1); !got.Equal(today) {
		t.Errorf("utils.DayStart(1) = %s, want %s", got.Format(time.RFC3339), today.Format(time.RFC3339))
	}

	// ③ 面板每个源卡上那句「距下次重置」（原本写死 +8；它是 per-source 字段，不是顶层）
	status, env := doJSON(t, srv, http.MethodGet, "/quota/dashboard", nil, authHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("读 dashboard 失败（status=%d msg=%s）——通路没打通", status, env.Msg)
	}
	list, ok := env.dataMap(t)["sources"].([]interface{})
	if !ok || len(list) == 0 {
		t.Fatalf("dashboard 的 sources 是空的（得到 %#v）——next_reset 挂在源卡上，取不到就等于断言空转",
			env.dataMap(t)["sources"])
	}
	item, _ := list[0].(map[string]interface{})
	want := tomorrow.Format("2006/1/2 15:04:05")
	if got := item["next_reset"]; got != want {
		t.Errorf("dashboard 的 next_reset = %v, want %s", got, want)
	}

	// ④ 趋势图的日标序列：末位必须落在平台时区的今天
	status, env = doJSON(t, srv, http.MethodGet, "/admin/monitor/trend", nil, authHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("读 trend 失败（status=%d msg=%s）", status, env.Msg)
	}
	days, ok := env.dataMap(t)["days"].([]interface{})
	if !ok || len(days) == 0 {
		t.Fatalf("trend 响应里没有 days 数组——后面的断言会空转")
	}
	if last, _ := days[len(days)-1].(string); last != today.Format("2006-01-02") {
		t.Errorf("趋势图最后一个日标 = %q, want %q", last, today.Format("2006-01-02"))
	}
	if len(days) != 7 {
		t.Errorf("日标数 = %d, want 7（展示窗口是 trendDays=7）", len(days))
	}
}

// TestDayStartWindowArithmetic 「近 N 天」这一族过去在三处各写一遍 `.AddDate(0,0,-(days-1))`。
// 这条钉的是收口后的算术本身（含 days<1 的兜底），免得下一轮有人把 DayStart 改成"往前 days 天"。
func TestDayStartWindowArithmetic(t *testing.T) {
	newTestServer(t) // 装配：conf 里的 TZOffsetHours 要先把配置读出来才引用
	today, _ := platformDay()

	if got := utils.DayStart(1); !got.Equal(today) {
		t.Errorf("DayStart(1) = %s, want %s", got.Format(time.RFC3339), today.Format(time.RFC3339))
	}
	for _, d := range []int{2, 7, 30} {
		want := today.AddDate(0, 0, -(d - 1))
		if got := utils.DayStart(d); !got.Equal(want) {
			t.Errorf("DayStart(%d) = %s, want %s（近 N 天含今天，所以往前是 N-1 天）",
				d, got.Format(time.RFC3339), want.Format(time.RFC3339))
		}
	}
	if got := utils.DayStart(0); !got.Equal(today) {
		t.Errorf("DayStart(0) = %s, want 退化成今天零点 %s（days<1 的兜底）",
			got.Format(time.RFC3339), today.Format(time.RFC3339))
	}
}
