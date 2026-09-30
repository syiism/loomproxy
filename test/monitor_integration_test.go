package test

// 监控趋势 / 号池状态 / IP 自动拉黑 集成测试。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"loomproxy/base"
	"loomproxy/base/pool"
	"loomproxy/db"
	"loomproxy/middleware/ipblock"
	"loomproxy/models"
)

// insertCallLog 直插监控明细（created_at 可指定，用于跨天分桶）
func insertCallLog(t *testing.T, source, action string, status int, createdAt time.Time) {
	t.Helper()
	if err := db.DB.Create(&models.ApiCallLog{
		Username:  "tester",
		IP:        "1.2.3.4",
		Source:    source,
		Action:    action,
		Status:    status,
		LatencyMs: 10,
		CreatedAt: createdAt,
	}).Error; err != nil {
		t.Fatalf("插入调用明细失败: %v", err)
	}
}

// TestMonitorTrend 近 7 天按天×数据源聚合，保留期外明细不计入，无调用的天不占 rows
func TestMonitorTrend(t *testing.T) {
	srv := newTestServer(t)
	base.ResetMetrics() // 内存环形缓冲为全局态，隔离其他用例的残留明细

	now := time.Now()
	day := func(n int) time.Time { // n 天前中午（避免跨零点边界）
		return time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, time.Local).AddDate(0, 0, -n)
	}
	insertCallLog(t, "fake_c", "search", 200, day(0))
	insertCallLog(t, "fake_c", "search", 200, day(0))
	insertCallLog(t, "fake_c", "search", 500, day(0))
	insertCallLog(t, "fake_c", "search", 200, day(1))
	insertCallLog(t, "fake_a", "search", 403, day(1))
	insertCallLog(t, "fake_a", "search", 200, day(3))
	insertCallLog(t, "fake_c", "search", 200, day(10)) // 保留期外，不计入

	status, env := doJSON(t, srv, http.MethodGet, "/admin/monitor/trend", nil, authHeader(adminToken(t, srv)))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("GET /admin/monitor/trend 失败（status=%d code=%d msg=%s）", status, env.Code, env.Msg)
	}
	data := env.dataMap(t)

	daysRaw, _ := data["days"].([]interface{})
	if len(daysRaw) != 7 {
		t.Fatalf("days 长度 = %d, want 7", len(daysRaw))
	}

	sourcesRaw, _ := data["sources"].([]interface{})
	if len(sourcesRaw) != 2 {
		t.Fatalf("sources = %v, want 2 个（fake_c/fake_a）", sourcesRaw)
	}

	// 聚合成 "date|source" -> {total, success} 便于断言
	type bucket struct{ total, success int64 }
	got := make(map[string]bucket)
	for _, r := range data["rows"].([]interface{}) {
		rm := r.(map[string]interface{})
		key := rm["day"].(string) + "|" + rm["source"].(string)
		got[key] = bucket{int64(rm["total"].(float64)), int64(rm["success"].(float64))}
	}
	dayStr := func(n int) string { return day(n).Format("2006-01-02") }

	assertBucket := func(key string, total, success int64) {
		t.Helper()
		b, ok := got[key]
		if !ok {
			t.Fatalf("缺少分桶 %s（got=%v）", key, got)
		}
		if b.total != total || b.success != success {
			t.Fatalf("分桶 %s = %+v, want total=%d success=%d", key, b, total, success)
		}
	}
	assertBucket(dayStr(0)+"|fake_c", 3, 2)
	assertBucket(dayStr(1)+"|fake_c", 1, 1)
	assertBucket(dayStr(1)+"|fake_a", 1, 0)
	assertBucket(dayStr(3)+"|fake_a", 1, 1)
	if len(got) != 4 {
		t.Fatalf("分桶数 = %d, want 4（保留期外明细不应计入）: %v", len(got), got)
	}
}

// TestMonitorTrendMergesInMemory 趋势合并内存环形缓冲中未落库的明细
// （明细缓冲满 250 条才批量落库，低流量时调用只在内存——趋势不得空白）
func TestMonitorTrendMergesInMemory(t *testing.T) {
	srv := newTestServer(t)
	base.ResetMetrics()

	// 禁用 fake_c/search 接口，制造一次 403 调用（进入内存环形缓冲，不落库）
	if err := db.DB.Model(&models.QuotaCost{}).
		Where("group_code = ? AND interface = ?", "fake_c", "search").
		Update("status", 0).Error; err != nil {
		t.Fatalf("禁用接口失败: %v", err)
	}
	delCostCache("fake_c", "search")
	token := registerUser(t, srv, "trend_user", "trend_user@example.com", "pass1234")
	searchUXX := func() (int, []byte) {
		return doRaw(t, srv, http.MethodGet, "/fake_c/search"+buildQuery(map[string]string{
			"query": "测试",
		}), nil, authHeader(token))
	}
	status, _ := searchUXX()
	if status != http.StatusForbidden {
		t.Fatalf("制造 403 调用失败（status=%d）", status)
	}

	status, env := doJSON(t, srv, http.MethodGet, "/admin/monitor/trend", nil, authHeader(adminToken(t, srv)))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("GET /admin/monitor/trend 失败（status=%d msg=%s）", status, env.Msg)
	}
	data := env.dataMap(t)
	today := time.Now().Format("2006-01-02")
	found := false
	for _, r := range data["rows"].([]interface{}) {
		rm := r.(map[string]interface{})
		if rm["day"] == today && rm["source"] == "fake_c" {
			found = true
			if total := int64(rm["total"].(float64)); total != 1 {
				t.Fatalf("内存合并分桶 total = %d, want 1", total)
			}
			if success := int64(rm["success"].(float64)); success != 0 {
				t.Fatalf("内存合并分桶 success = %d, want 0（403 非 2xx）", success)
			}
		}
	}
	if !found {
		t.Fatalf("趋势缺少今日 fake_c 分桶（内存明细未合并）: %v", data["rows"])
	}
}

// TestPoolStatusEndpoint 号池状态接口：按 status 统计 pool_devices，标识与凭证脱敏。
// 池未启动（无数据源登记或开关关闭）时走库计数兜底，Running=false。
func TestPoolStatusEndpoint(t *testing.T) {
	srv := newTestServer(t)

	p := pool.Register(pool.New(newFakeProvider(), fakePoolConfig()))
	t.Cleanup(func() { pool.Unregister(p.Name()) })

	devices := []models.PoolDevice{
		{Pool: fakePoolName, Ident: "dev-hot-1234567890ab", Status: pool.StatusHot, TotalQuota: 60, UsedQuota: 10, Attrs: `{"sn":"sn-hot-1234567890ab"}`},
		{Pool: fakePoolName, Ident: "dev-cold-1234567890ab", Status: pool.StatusCold, Attrs: `{"sn":"sn-cold-1234567890ab"}`},
		{Pool: fakePoolName, Ident: "dev-spent-1234567890a", Status: pool.StatusSpent, Attrs: `{"sn":"sn-spent-1234567890a"}`},
		{Pool: fakePoolName, Ident: "dev-spent2-1234567890", Status: pool.StatusSpent, Attrs: `{"sn":"sn-spent2-1234567890"}`},
		{Pool: fakePoolName, Ident: "dev-dead-1234567890ab", Status: pool.StatusDead, Attrs: `{"sn":"sn-dead-1234567890ab"}`},
	}
	for i := range devices {
		if err := db.DB.Create(&devices[i]).Error; err != nil {
			t.Fatalf("预置号失败: %v", err)
		}
	}

	status, env := doJSON(t, srv, http.MethodGet, "/admin/pools", nil, authHeader(adminToken(t, srv)))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("GET /admin/pools 失败（status=%d code=%d msg=%s）", status, env.Code, env.Msg)
	}
	var pools []map[string]interface{}
	if err := json.Unmarshal(env.Data, &pools); err != nil {
		t.Fatalf("解析号池列表失败: %v", err)
	}
	if len(pools) != 1 {
		t.Fatalf("号池数 = %d, want 1（%v）", len(pools), pools)
	}

	counts, ok := pools[0]["counts"].(map[string]interface{})
	if !ok {
		t.Fatalf("响应缺少 counts: %v", pools[0])
	}
	want := map[string]int64{"hot": 1, "cold": 1, "spent": 2, "dead": 1}
	for k, v := range want {
		if got := int64(counts[k].(float64)); got != v {
			t.Fatalf("counts[%s] = %d, want %d（counts=%v）", k, got, v, counts)
		}
	}

	// 脱敏校验：完整标识/凭证值不得出现在响应中
	raw, _ := json.Marshal(pools)
	for _, secret := range []string{"dev-hot-1234567890ab", "sn-hot-1234567890ab"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("号池状态未脱敏，响应含 %s", secret)
		}
	}
}

// TestAutoBlockWindowAndStatus 自动拉黑的计数口径：
// 仅 403/429 计入（2xx/4xx/5xx 不计）、403 与 429 混合累计、
// 滑动窗口外的旧记录被剪枝不计入。经导出函数直接驱动非回环地址场景。
func TestAutoBlockWindowAndStatus(t *testing.T) {
	srv := newTestServer(t)

	// 开启自动拉黑：阈值 3 次 / 窗口 2s（短窗口便于验证剪枝）
	admin := authHeader(adminToken(t, srv))
	for k, v := range map[string]string{
		"auto_block_enabled":    "true",
		"auto_block_threshold":  "3",
		"auto_block_window_sec": "2",
	} {
		status, env := doJSON(t, srv, http.MethodPut, "/admin/settings/"+k,
			map[string]interface{}{"value": v}, admin)
		if status != http.StatusOK || env.Code != 0 {
			t.Fatalf("设置 %s 失败（status=%d msg=%s）", k, status, env.Msg)
		}
	}

	assertBlocked := func(ip string, want bool) {
		t.Helper()
		var count int64
		db.DB.Model(&models.BlockedIP{}).Where("ip = ?", ip).Count(&count)
		if (count > 0) != want {
			t.Fatalf("IP %s 拉黑状态 = %v, want %v", ip, count > 0, want)
		}
	}

	// 1) 非 403/429 状态码不计数：远超阈值的 200/400/500 不应触发拉黑
	ip1 := "203.0.113.10"
	for i := 0; i < 10; i++ {
		ipblock.RecordFailure(ip1, http.StatusOK)
		ipblock.RecordFailure(ip1, http.StatusBadRequest)
		ipblock.RecordFailure(ip1, http.StatusInternalServerError)
	}
	assertBlocked(ip1, false)

	// 2) 403 与 429 混合累计达阈值 → 拉黑
	ipblock.RecordFailure(ip1, http.StatusForbidden)
	ipblock.RecordFailure(ip1, http.StatusTooManyRequests)
	assertBlocked(ip1, false) // 2 < 3
	ipblock.RecordFailure(ip1, http.StatusForbidden)
	assertBlocked(ip1, true)

	// 3) 滑动窗口剪枝：窗口外的旧记录不计入
	ip2 := "203.0.113.11"
	ipblock.RecordFailure(ip2, http.StatusForbidden)
	ipblock.RecordFailure(ip2, http.StatusForbidden)
	time.Sleep(2100 * time.Millisecond) // 前 2 次滑出窗口
	ipblock.RecordFailure(ip2, http.StatusForbidden)
	ipblock.RecordFailure(ip2, http.StatusForbidden)
	assertBlocked(ip2, false) // 窗口内仅 2 次 < 3
	ipblock.RecordFailure(ip2, http.StatusForbidden)
	assertBlocked(ip2, true) // 窗口内第 3 次达标
}

// TestAutoBlockIP 自动拉黑：阈值内 403/429 累计达标后写入 blocked_ips（source=auto），
// 后续请求被全局中间件 403；本机回环地址永不自动拉黑
func TestAutoBlockIP(t *testing.T) {
	srv := newTestServer(t)

	// 开启自动拉黑：阈值 3 次 / 窗口 60s
	admin := authHeader(adminToken(t, srv))
	for k, v := range map[string]string{
		"auto_block_enabled":    "true",
		"auto_block_threshold":  "3",
		"auto_block_window_sec": "60",
	} {
		status, env := doJSON(t, srv, http.MethodPut, "/admin/settings/"+k,
			map[string]interface{}{"value": v}, admin)
		if status != http.StatusOK || env.Code != 0 {
			t.Fatalf("设置 %s 失败（status=%d msg=%s）", k, status, env.Msg)
		}
	}

	// 制造 403：禁用 fake_c/search 接口后，普通用户请求触发计费层 403
	if err := db.DB.Model(&models.QuotaCost{}).
		Where("group_code = ? AND interface = ?", "fake_c", "search").
		Update("status", 0).Error; err != nil {
		t.Fatalf("禁用接口失败: %v", err)
	}
	delCostCache("fake_c", "search")
	token := registerUser(t, srv, "ab_user", "ab_user@example.com", "pass1234")

	searchUXX := func() (int, []byte) {
		return doRaw(t, srv, http.MethodGet, "/fake_c/search"+buildQuery(map[string]string{
			"query": "测试",
		}), nil, authHeader(token))
	}
	// 前 2 次 403 不应拉黑
	for i := 0; i < 2; i++ {
		status, _ := searchUXX()
		if status != http.StatusForbidden {
			t.Fatalf("第 %d 次请求 status = %d, want 403", i+1, status)
		}
	}
	var count int64
	db.DB.Model(&models.BlockedIP{}).Where("ip = ?", "127.0.0.1").Count(&count)
	if count != 0 {
		t.Fatal("未达阈值即拉黑")
	}

	// 回环地址安全阀：即使达到阈值也不拉黑本机地址（防管理端自锁）
	for i := 0; i < 3; i++ {
		searchUXX()
	}
	db.DB.Model(&models.BlockedIP{}).Where("ip = ?", "127.0.0.1").Count(&count)
	if count != 0 {
		t.Fatal("回环地址被自动拉黑，安全阀失效")
	}

	// 非回环地址：直接经内部函数累计达标，应写入 blocked_ips（source=auto）
	for i := 0; i < 3; i++ {
		ipblock.RecordFailure("203.0.113.9", http.StatusForbidden)
	}
	var rec models.BlockedIP
	if err := db.DB.Where("ip = ?", "203.0.113.9").First(&rec).Error; err != nil {
		t.Fatalf("非回环地址达阈值未拉黑: %v", err)
	}
	if rec.Source != "auto" {
		t.Fatalf("拉黑来源 = %q, want auto", rec.Source)
	}
}
