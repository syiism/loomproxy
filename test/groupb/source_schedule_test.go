package groupb

// 数据源定时启用：窗口算法与「进窗口启用 / 出窗口还原」的语义。
//
// 判定那一轮是 admin.RunSourceScheduleTick(now)——时刻由调用方注入，所以这些用例
// 不用 sleep、不用真的等到那个钟点，也不起后台协程（testkit 只 CreateApp，循环根本没起）。

import (
	"net/http"
	"testing"
	"time"

	"loomproxy/db"
	"loomproxy/handlers/admin"
	"loomproxy/models"
	"loomproxy/utils"
)

func sourceStatusOf(t *testing.T, name string) int {
	t.Helper()
	var ds models.DataSource
	if err := db.DB.Where("name = ?", name).First(&ds).Error; err != nil {
		t.Fatalf("读数据源 %s 失败: %v", name, err)
	}
	return ds.Status
}

// setSourceStatus 改源状态并在用例结束时还原成启用——这些用例会把源关掉来观察定时任务，
// 不还原的话同包里后面那些「普通用户能访问 fake_a」的用例会集体失败。
func setSourceStatus(t *testing.T, name string, status int) {
	t.Helper()
	if err := db.DB.Model(&models.DataSource{}).Where("name = ?", name).Update("status", status).Error; err != nil {
		t.Fatalf("设置 %s 状态失败: %v", name, err)
	}
	t.Cleanup(func() {
		_ = db.DB.Model(&models.DataSource{}).Where("name = ?", name).Update("status", 1)
	})
}

func scheduleRow(t *testing.T, id uint) models.SourceSchedule {
	t.Helper()
	var r models.SourceSchedule
	if err := db.DB.Where("id = ?", id).First(&r).Error; err != nil {
		t.Fatalf("读规则 %d 失败: %v", id, err)
	}
	return r
}

// TestSourceScheduleWindowDaily 每天时段：窗口内/外、跨夜。
func TestSourceScheduleWindowDaily(t *testing.T) {
	loc := utils.PlatformZone()
	at := func(h, m int) time.Time { return time.Date(2026, 10, 11, h, m, 0, 0, loc) }

	cases := []struct {
		name     string
		startEnd [2]string
		now      time.Time
		wantIn   bool
	}{
		{"开始前", [2]string{"09:00", "18:00"}, at(8, 59), false},
		{"起始点", [2]string{"09:00", "18:00"}, at(9, 0), true},
		{"窗口中间", [2]string{"09:00", "18:00"}, at(13, 0), true},
		{"结束点（不含）", [2]string{"09:00", "18:00"}, at(18, 0), false},
		{"凌晨不在窗口", [2]string{"09:00", "18:00"}, at(3, 0), false},
		{"跨夜：起始点", [2]string{"22:00", "06:00"}, at(22, 0), true},
		{"跨夜：深夜", [2]string{"22:00", "06:00"}, at(23, 30), true},
		{"跨夜：次日凌晨仍在", [2]string{"22:00", "06:00"}, at(5, 59), true},
		{"跨夜：结束点", [2]string{"22:00", "06:00"}, at(6, 0), false},
		{"跨夜：白天不在", [2]string{"22:00", "06:00"}, at(12, 0), false},
	}
	for _, tc := range cases {
		r := models.SourceSchedule{
			Mode:       models.ScheduleModeDaily,
			StartClock: tc.startEnd[0],
			EndClock:   tc.startEnd[1],
		}
		in, expired := admin.SourceScheduleWindow(&r, tc.now)
		if in != tc.wantIn {
			t.Errorf("%s：in = %v, want %v", tc.name, in, tc.wantIn)
		}
		if expired {
			t.Errorf("%s：每天时段不该判为已过期", tc.name)
		}
	}
}

// TestSourceScheduleWindowOnce 一次性：未开始 / 生效中 / 已过期。
func TestSourceScheduleWindowOnce(t *testing.T) {
	loc := utils.PlatformZone()
	start := time.Date(2026, 10, 11, 20, 0, 0, 0, loc)
	r := models.SourceSchedule{Mode: models.ScheduleModeOnce, StartAt: &start, DurationMin: 60}

	if in, expired := admin.SourceScheduleWindow(&r, start.Add(-time.Minute)); in || expired {
		t.Errorf("开始前：in=%v expired=%v, want false/false", in, expired)
	}
	if in, expired := admin.SourceScheduleWindow(&r, start.Add(time.Minute)); !in || expired {
		t.Errorf("生效中：in=%v expired=%v, want true/false", in, expired)
	}
	if in, _ := admin.SourceScheduleWindow(&r, start.Add(59*time.Minute)); !in {
		t.Errorf("窗口最后一分钟应在窗口内")
	}
	if in, expired := admin.SourceScheduleWindow(&r, start.Add(61*time.Minute)); in || !expired {
		t.Errorf("已过期：in=%v expired=%v, want false/true", in, expired)
	}
}

// TestSourceScheduleTickEnablesThenRestores 进窗口启用、出窗口还原成进窗口前的那个状态。
func TestSourceScheduleTickEnablesThenRestores(t *testing.T) {
	newTestServer(t)
	id := dataSourceIDByName(t, "fake_a")
	setSourceStatus(t, "fake_a", 0)

	T0 := time.Now().Truncate(time.Minute)
	rule := models.SourceSchedule{
		DataSourceID: id,
		Mode:         models.ScheduleModeOnce,
		StartAt:      &T0,
		DurationMin:  5,
		Status:       1,
	}
	if err := db.DB.Create(&rule).Error; err != nil {
		t.Fatalf("建规则失败: %v", err)
	}
	t.Cleanup(func() { _ = db.DB.Delete(&models.SourceSchedule{}, rule.ID) })

	// 窗口内：源被顶成启用，并记下原状态 0
	admin.RunSourceScheduleTick(T0.Add(time.Minute))
	if got := sourceStatusOf(t, "fake_a"); got != 1 {
		t.Fatalf("窗口内源状态 = %d, want 1", got)
	}
	if r := scheduleRow(t, rule.ID); r.Applied != 1 || r.RestoreStatus != 0 {
		t.Fatalf("窗口内规则状态 applied=%d restore=%d, want 1/0", r.Applied, r.RestoreStatus)
	}

	// 出窗口：还原成进窗口前的 0（不是"一律禁用"，是"还原"）
	admin.RunSourceScheduleTick(T0.Add(10 * time.Minute))
	if got := sourceStatusOf(t, "fake_a"); got != 0 {
		t.Fatalf("出窗口后源状态 = %d, want 0（还原进窗口前的状态）", got)
	}
	if r := scheduleRow(t, rule.ID); r.Applied != 0 {
		t.Fatalf("出窗口后 applied = %d, want 0", r.Applied)
	}
	if r := scheduleRow(t, rule.ID); r.Status != 0 {
		t.Fatalf("一次性规则过期后 status = %d, want 0（已结束）", r.Status)
	}
}

// TestSourceScheduleKeepsManuallyEnabled 本来就手动启用着的源，出窗口后仍是启用——
// 定时任务不该把一个没人让它关的源关掉（这是"还原"而非"禁用"这条语义的全部意义）。
func TestSourceScheduleKeepsManuallyEnabled(t *testing.T) {
	newTestServer(t)
	id := dataSourceIDByName(t, "fake_b")
	setSourceStatus(t, "fake_b", 1)

	T0 := time.Now().Truncate(time.Minute)
	rule := models.SourceSchedule{
		DataSourceID: id,
		Mode:         models.ScheduleModeOnce,
		StartAt:      &T0,
		DurationMin:  5,
		Status:       1,
	}
	if err := db.DB.Create(&rule).Error; err != nil {
		t.Fatalf("建规则失败: %v", err)
	}
	t.Cleanup(func() { _ = db.DB.Delete(&models.SourceSchedule{}, rule.ID) })

	admin.RunSourceScheduleTick(T0.Add(time.Minute))
	if r := scheduleRow(t, rule.ID); r.Applied != 1 || r.RestoreStatus != 1 {
		t.Fatalf("窗口内 applied=%d restore=%d, want 1/1", r.Applied, r.RestoreStatus)
	}
	admin.RunSourceScheduleTick(T0.Add(10 * time.Minute))
	if got := sourceStatusOf(t, "fake_b"); got != 1 {
		t.Fatalf("出窗口后手动启用的源状态 = %d, want 1（不该被定时任务关掉）", got)
	}
}

// TestSourceScheduleOverlappingRules 同一个源的两条规则窗口重叠：源的真实原状态只由最早进窗那条记，
// 先出窗的那条不许动源状态，最后一出窗的那条才还原——否则源会先被凭空关掉、再被留在永久启用。
func TestSourceScheduleOverlappingRules(t *testing.T) {
	newTestServer(t)
	id := dataSourceIDByName(t, "fake_a")
	setSourceStatus(t, "fake_a", 0)

	T0 := time.Now().Truncate(time.Minute)
	// A: [T0, T0+10min)，B: [T0+5, T0+25min) —— 前 5 分钟只有 A，中间 5 分钟两条重叠
	mk := func(start time.Time, dur int) models.SourceSchedule {
		r := models.SourceSchedule{DataSourceID: id, Mode: models.ScheduleModeOnce, StartAt: &start, DurationMin: dur, Status: 1}
		if err := db.DB.Create(&r).Error; err != nil {
			t.Fatalf("建规则失败: %v", err)
		}
		t.Cleanup(func() { _ = db.DB.Delete(&models.SourceSchedule{}, r.ID) })
		return r
	}
	a := mk(T0, 10)
	b := mk(T0.Add(5*time.Minute), 20)

	admin.RunSourceScheduleTick(T0.Add(time.Minute)) // 只有 A 在窗口里
	admin.RunSourceScheduleTick(T0.Add(6 * time.Minute))
	if r := scheduleRow(t, b.ID); r.Applied != 1 || r.RestoreStatus != 0 {
		t.Fatalf("重叠进窗后 B applied=%d restore=%d, want 1/0（源已被 A 顶成启用，原状态要从 A 继承，不能读那个 1）",
			r.Applied, r.RestoreStatus)
	}

	admin.RunSourceScheduleTick(T0.Add(11 * time.Minute)) // A 出窗、B 仍在窗口里
	if got := sourceStatusOf(t, "fake_a"); got != 1 {
		t.Fatalf("A 出窗后源状态 = %d, want 1（B 的窗口还没结束，不该被关掉）", got)
	}

	admin.RunSourceScheduleTick(T0.Add(26 * time.Minute)) // B 也出窗：最后一条负责还原
	if got := sourceStatusOf(t, "fake_a"); got != 0 {
		t.Fatalf("全部出窗后源状态 = %d, want 0（还原进窗前 A 记下的原状态）", got)
	}
	for _, rid := range []uint{a.ID, b.ID} {
		if r := scheduleRow(t, rid); r.Applied != 0 {
			t.Errorf("规则 %d 出窗后 applied = %d, want 0", rid, r.Applied)
		}
	}
}

// TestSourceScheduleDeleteRestores 删掉一条正在生效的规则必须先还原源状态——
// 规则行没了，循环再也看不见它，没有任何一步会把它收回来。
func TestSourceScheduleDeleteRestores(t *testing.T) {
	srv := newTestServer(t)
	adminTok := adminToken(t, srv)
	id := dataSourceIDByName(t, "fake_c")
	setSourceStatus(t, "fake_c", 0)

	T0 := time.Now().Truncate(time.Minute)
	startISO := T0.UTC().Format(time.RFC3339)
	status, env := doJSON(t, srv, http.MethodPost, "/admin/source-schedules", map[string]interface{}{
		"source_ids": []uint{id}, "mode": "once", "start_at": startISO, "duration_min": 5,
	}, authHeader(adminTok))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("创建规则失败（status=%d msg=%s）", status, env.Msg)
	}

	// 创建接口会立刻判定一轮，此刻落在窗口里 → 源已被启用
	if got := sourceStatusOf(t, "fake_c"); got != 1 {
		t.Fatalf("创建后源状态 = %d, want 1（落在窗口内应立刻生效）", got)
	}

	var rule models.SourceSchedule
	if err := db.DB.Where("data_source_id = ?", id).Order("id desc").First(&rule).Error; err != nil {
		t.Fatalf("读规则失败: %v", err)
	}
	status, env = doJSON(t, srv, http.MethodDelete, "/admin/source-schedules/"+itoa(rule.ID), nil, authHeader(adminTok))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("删除规则失败（status=%d msg=%s）", status, env.Msg)
	}
	if got := sourceStatusOf(t, "fake_c"); got != 0 {
		t.Fatalf("删除生效中的规则后源状态 = %d, want 0（应先还原再退场）", got)
	}
}

// TestSourceScheduleCreateBulkAndValidate 一次为多个源各建一行；非法形态一律 400。
func TestSourceScheduleCreateBulkAndValidate(t *testing.T) {
	srv := newTestServer(t)
	adminTok := adminToken(t, srv)
	ids := []uint{
		dataSourceIDByName(t, "fake_a"),
		dataSourceIDByName(t, "fake_b"),
		dataSourceIDByName(t, "fake_c"),
	}
	t.Cleanup(func() {
		_ = db.DB.Where("data_source_id IN ?", ids).Delete(&models.SourceSchedule{}).Error
	})

	status, env := doJSON(t, srv, http.MethodPost, "/admin/source-schedules", map[string]interface{}{
		"source_ids": ids, "mode": "daily", "start_clock": "09:00", "end_clock": "18:00",
	}, authHeader(adminTok))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("批量创建失败（status=%d msg=%s）", status, env.Msg)
	}
	if env.DataMap(t)["created"].(float64) != 3 {
		t.Fatalf("创建条数 = %v, want 3", env.Data)
	}

	bad := []struct {
		name string
		body map[string]interface{}
	}{
		{"非法模式", map[string]interface{}{"source_ids": ids, "mode": "weekly", "start_clock": "09:00", "end_clock": "18:00"}},
		{"钟点越界", map[string]interface{}{"source_ids": ids, "mode": "daily", "start_clock": "25:00", "end_clock": "18:00"}},
		{"钟点非数字", map[string]interface{}{"source_ids": ids, "mode": "daily", "start_clock": "abc", "end_clock": "18:00"}},
		{"一次性缺起始时刻", map[string]interface{}{"source_ids": ids, "mode": "once", "duration_min": 60}},
		{"一次性时长为 0", map[string]interface{}{"source_ids": ids, "mode": "once", "start_at": time.Now().UTC().Format(time.RFC3339), "duration_min": 0}},
		{"数据源不存在", map[string]interface{}{"source_ids": []uint{999999}, "mode": "daily", "start_clock": "09:00", "end_clock": "18:00"}},
	}
	for _, c := range bad {
		status, _ := doJSON(t, srv, http.MethodPost, "/admin/source-schedules", c.body, authHeader(adminTok))
		if status != http.StatusBadRequest {
			t.Errorf("%s 应 400，status=%d", c.name, status)
		}
	}
}
