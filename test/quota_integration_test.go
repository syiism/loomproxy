package test

// quota 模块集成测试：数据源访问控制 / 计费 / 速率限制三个中间件的主路径。
// 数据源路由中间件链：monitor → baseURLCheck → DataSourceAccess → Billing → RateLimit → handler
// （app/app.go registerHandlers）。测试直接改库构造场景，注意清理 quota cost 缓存。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"loomproxy/db"
	"loomproxy/gate"
	"loomproxy/models"
	"loomproxy/utils"
)

// searchA 以指定 token 请求 假数据源A搜索接口
func searchA(t *testing.T, srv *httptest.Server, token string) (int, []byte) {
	t.Helper()
	return doRaw(t, srv, http.MethodGet, "/fake_a/search"+buildQuery(map[string]string{
		"query": "测试",
	}), nil, authHeader(token))
}

// delCostCache 直接改库后清理 QuotaCost 缓存（getCachedCost 走 utils.DefaultCache）
func delCostCache(sourceCode, action string) {
	utils.DefaultCache().Del(fmt.Sprintf("quota:cost:%s:%s", sourceCode, action))
}

// planIDByCode / dataSourceIDByName 查库辅助
func planIDByCode(t *testing.T, code string) uint {
	t.Helper()
	var id uint
	if err := db.DB.Table("quota_plans").Select("id").Where("code = ?", code).Scan(&id).Error; err != nil || id == 0 {
		t.Fatalf("查询套餐 %s 失败（id=%d err=%v）", code, id, err)
	}
	return id
}

func dataSourceIDByName(t *testing.T, name string) uint {
	t.Helper()
	var id uint
	if err := db.DB.Table("data_sources").Select("id").Where("name = ?", name).Scan(&id).Error; err != nil || id == 0 {
		t.Fatalf("查询数据源 %s 失败（id=%d err=%v）", name, id, err)
	}
	return id
}

// setAUpstream 把 fake_a 的平台默认 baseUrl 指向假上游。
// 平台默认配置属管理员可信来源，SSRF 校验豁免（允许 127.0.0.1）。
func setAUpstream(t *testing.T, upstreamURL string) {
	t.Helper()
	if err := db.DB.Create(&models.PlatformSourceConfig{
		SourceName: "fake_a",
		BaseURL:    upstreamURL,
	}).Error; err != nil {
		t.Fatalf("写入平台默认 baseUrl 失败: %v", err)
	}
}

// TestAccessDisabledSource 数据源被管理员禁用（status=0）后全员 403
func TestAccessDisabledSource(t *testing.T) {
	srv := newTestServer(t)
	token := registerUser(t, srv, "q_user1", "q_user1@example.com", "pass1234")

	if err := db.DB.Model(&models.DataSource{}).Where("name = ?", "fake_a").Update("status", 0).Error; err != nil {
		t.Fatalf("禁用数据源失败: %v", err)
	}

	status, raw := searchA(t, srv, token)
	if status != http.StatusForbidden {
		t.Fatalf("禁用数据源后请求 status = %d, want 403（body=%s）", status, truncate(string(raw), 200))
	}
	if !strings.Contains(string(raw), "禁用") {
		t.Fatalf("错误提示 = %s, want 含「禁用」", truncate(string(raw), 200))
	}
}

// TestAccessPlanNotInclude 套餐未授权数据源时普通用户 403，管理员不受影响
func TestAccessPlanNotInclude(t *testing.T) {
	srv := newTestServer(t)
	token := registerUser(t, srv, "q_user2", "q_user2@example.com", "pass1234")

	// 回收 free 套餐对 fake_a 的授权（授权=限额行，gate/grant.go）
	if err := gate.UngrantPlanSource(planIDByCode(t, "free"), "fake_a"); err != nil {
		t.Fatalf("回收套餐授权失败: %v", err)
	}

	status, raw := searchA(t, srv, token)
	if status != http.StatusForbidden {
		t.Fatalf("套餐不含数据源 status = %d, want 403（body=%s）", status, truncate(string(raw), 200))
	}
	if !strings.Contains(string(raw), "套餐不包含") {
		t.Fatalf("错误提示 = %s, want 含「套餐不包含」", truncate(string(raw), 200))
	}

	// 管理员跳过访问控制（无 baseUrl 会走到 handler 报参数错误，但绝不应 403）
	status, _ = searchA(t, srv, adminToken(t, srv))
	if status == http.StatusForbidden {
		t.Fatal("管理员请求被访问控制拦截，want 豁免")
	}
}

// TestBillingDisabledInterface QuotaCost.Status=0 的接口对全员 403（与计费开关无关）
func TestBillingDisabledInterface(t *testing.T) {
	srv := newTestServer(t)
	token := registerUser(t, srv, "q_user3", "q_user3@example.com", "pass1234")

	if err := db.DB.Model(&models.QuotaCost{}).
		Where("group_code = ? AND interface = ?", "fake_a", "search").
		Update("status", 0).Error; err != nil {
		t.Fatalf("禁用接口失败: %v", err)
	}
	delCostCache("fake_a", "search")

	status, raw := searchA(t, srv, token)
	if status != http.StatusForbidden {
		t.Fatalf("接口禁用后请求 status = %d, want 403（body=%s）", status, truncate(string(raw), 200))
	}
	if !strings.Contains(string(raw), "接口已被管理员禁用") {
		t.Fatalf("错误提示 = %s, want 含「接口已被管理员禁用」", truncate(string(raw), 200))
	}
}

// TestBillingDeductsOnSuccess 上游成功（HTTP 200）后扣减并写 quota_usage_logs。
// 假上游经平台默认 baseUrl 注入（SSRF 可信豁免），fake_a search 对 {} 响应归一化为空书单（data.books 缺失 → 空列表）。
func TestBillingDeductsOnSuccess(t *testing.T) {
	srv := newTestServer(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(upstream.Close)
	setAUpstream(t, upstream.URL)

	token := registerUser(t, srv, "q_user4", "q_user4@example.com", "pass1234")
	uid := userIDByName(t, "q_user4")

	status, raw := searchA(t, srv, token)
	if status != http.StatusOK {
		t.Fatalf("假上游搜索 status = %d, want 200（body=%s）", status, truncate(string(raw), 200))
	}

	var count int64
	if err := db.DB.Model(&models.QuotaUsageLog{}).
		Where("user_id = ? AND group_code = ? AND interface = ?", uid, "fake_a", "search").
		Count(&count).Error; err != nil {
		t.Fatalf("查询用量流水失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("用量流水条数 = %d, want 1", count)
	}
}

// TestBillingQuotaExhausted 当日额度用完后返回 429
func TestBillingQuotaExhausted(t *testing.T) {
	srv := newTestServer(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(upstream.Close)
	setAUpstream(t, upstream.URL)

	// free 套餐 fake_a 当日限额改为 1
	if err := db.DB.Model(&models.QuotaLimit{}).
		Where("plan_id = ? AND scope = ? AND target = ?", planIDByCode(t, "free"), "source", "fake_a").
		Update("limit", 1).Error; err != nil {
		t.Fatalf("修改限额失败: %v", err)
	}

	token := registerUser(t, srv, "q_user5", "q_user5@example.com", "pass1234")

	status, raw := searchA(t, srv, token)
	if status != http.StatusOK {
		t.Fatalf("第 1 次请求 status = %d, want 200（body=%s）", status, truncate(string(raw), 200))
	}

	status, raw = searchA(t, srv, token)
	if status != http.StatusTooManyRequests {
		t.Fatalf("额度用尽后请求 status = %d, want 429（body=%s）", status, truncate(string(raw), 200))
	}
	if !strings.Contains(string(raw), "额度已用完") {
		t.Fatalf("错误提示 = %s, want 含「额度已用完」", truncate(string(raw), 200))
	}
}

// contentA 以指定 token 请求假数据源A正文接口
func contentA(t *testing.T, srv *httptest.Server, token string) (int, []byte) {
	t.Helper()
	return doRaw(t, srv, http.MethodGet, "/fake_a/content"+buildQuery(map[string]string{
		"bookId": "1",
		"itemId": "2",
	}), nil, authHeader(token))
}

// TestRateLimitPlanIntervalFreeContent 套餐级速率限制（QuotaCostPlan）：
// 假数据源A免费套餐 content 配置 10s 一次，绑定 free 套餐的普通用户一分钟内连续请求，
// 第 1 次放行、后续请求 429；管理员豁免
func TestRateLimitPlanIntervalFreeContent(t *testing.T) {
	srv := newTestServer(t)

	freePlanID := planIDByCode(t, "free")

	// 免费套餐 fake_a/content 接口：每 10 秒允许 1 次（直查库无缓存，插入即生效）
	if err := db.DB.Create(&models.QuotaCostPlan{
		PlanID:    freePlanID,
		GroupCode: "fake_a",
		Interface: "content",
		Interval:  10,
	}).Error; err != nil {
		t.Fatalf("写入套餐级限流配置失败: %v", err)
	}

	// 注册普通用户并显式绑定 free 套餐
	token := registerUser(t, srv, "q_user7", "q_user7@example.com", "pass1234")
	if err := db.DB.Model(&models.User{}).
		Where("username = ?", "q_user7").
		Update("plan_id", freePlanID).Error; err != nil {
		t.Fatalf("绑定 free 套餐失败: %v", err)
	}

	// 第 1 次：放行（无 baseUrl 走到 handler 报参数错误 400，但不应被限流）
	status, _ := contentA(t, srv, token)
	if status == http.StatusTooManyRequests {
		t.Fatal("第 1 次请求即被限流，want 放行")
	}

	// 一分钟内连续请求（间隔 10s 内）：第 2、3 次均应 429
	for i := 2; i <= 3; i++ {
		status, raw := contentA(t, srv, token)
		if status != http.StatusTooManyRequests {
			t.Fatalf("间隔内第 %d 次请求 status = %d, want 429（body=%s）", i, status, truncate(string(raw), 200))
		}
		if !strings.Contains(string(raw), "过于频繁") {
			t.Fatalf("第 %d 次错误提示 = %s, want 含「过于频繁」", i, truncate(string(raw), 200))
		}
	}

	// 管理员豁免一切速率限制
	status, _ = contentA(t, srv, adminToken(t, srv))
	if status == http.StatusTooManyRequests {
		t.Fatal("管理员请求被限流，want 豁免")
	}
}

// TestRateLimitPlanIntervalSourceCode 套餐级限流按管理面板写入口径（group_code=数据源码）
// 写入时必须生效：回归 2026-08-09 线上 Bug——Interfaces.vue upsert 用 source_code
// （如 fake_a）落库，而 rateLimitMiddleware 按组码（组码）查询导致限流形同虚设
func TestRateLimitPlanIntervalSourceCode(t *testing.T) {
	srv := newTestServer(t)

	freePlanID := planIDByCode(t, "free")

	// 模拟管理面板写入：group_code 为数据源码 fake_a
	//（用 chapter 接口避免与其他用例共享同 key 的内存限流器状态）
	if err := db.DB.Create(&models.QuotaCostPlan{
		PlanID:    freePlanID,
		GroupCode: "fake_a",
		Interface: "chapter",
		Interval:  10,
	}).Error; err != nil {
		t.Fatalf("写入套餐级限流配置失败: %v", err)
	}

	token := registerUser(t, srv, "q_user8", "q_user8@example.com", "pass1234")
	if err := db.DB.Model(&models.User{}).
		Where("username = ?", "q_user8").
		Update("plan_id", freePlanID).Error; err != nil {
		t.Fatalf("绑定 free 套餐失败: %v", err)
	}

	chapter := func() (int, []byte) {
		return doRaw(t, srv, http.MethodGet, "/fake_a/chapter"+buildQuery(map[string]string{
			"bookId": "1",
		}), nil, authHeader(token))
	}

	// 第 1 次放行，间隔内第 2 次必须 429（修复前此处放行，限流不生效）
	if status, _ := chapter(); status == http.StatusTooManyRequests {
		t.Fatal("第 1 次请求即被限流，want 放行")
	}
	status, raw := chapter()
	if status != http.StatusTooManyRequests {
		t.Fatalf("按数据源码配置的套餐级限流未生效：status = %d, want 429（body=%s）", status, truncate(string(raw), 200))
	}
}

// TestRateLimitInterval QuotaCost.Interval 生效：同用户第二次请求 429；管理员豁免
func TestRateLimitInterval(t *testing.T) {
	srv := newTestServer(t)

	if err := db.DB.Model(&models.QuotaCost{}).
		Where("group_code = ? AND interface = ?", "fake_a", "search").
		Update("interval", 60).Error; err != nil {
		t.Fatalf("设置限流间隔失败: %v", err)
	}
	delCostCache("fake_a", "search")

	token := registerUser(t, srv, "q_user6", "q_user6@example.com", "pass1234")

	// 第 1 次：放行（无 baseUrl 走到 handler 报参数错误 400，但不应被限流）
	status, _ := searchA(t, srv, token)
	if status == http.StatusTooManyRequests {
		t.Fatal("第 1 次请求即被限流，want 放行")
	}

	// 第 2 次：同用户同 IP，间隔内 429
	status, raw := searchA(t, srv, token)
	if status != http.StatusTooManyRequests {
		t.Fatalf("间隔内第 2 次请求 status = %d, want 429（body=%s）", status, truncate(string(raw), 200))
	}
	if !strings.Contains(string(raw), "过于频繁") {
		t.Fatalf("错误提示 = %s, want 含「过于频繁」", truncate(string(raw), 200))
	}

	// 管理员豁免一切速率限制
	status, _ = searchA(t, srv, adminToken(t, srv))
	if status == http.StatusTooManyRequests {
		t.Fatal("管理员请求被限流，want 豁免")
	}
}

// 限流用例统一使用未被其他用例占用的 数据源+接口 组合，避免共享进程级内存限流器状态
//（IP/用户维度的 key 含数据源+接口；已占用：fake_a 的 search/content/chapter/detail 与 fake_b 的 search/detail/chapter）

// TestRateLimitWindowCount 窗口计数限流（QuotaCost.limit_count / window_sec）：
// 窗口内允许突发——前 N 次连续请求全部放行（间隔模型下第 2 次就会 429），
// 用满后剩余窗口内一律 429；管理员豁免
func TestRateLimitWindowCount(t *testing.T) {
	srv := newTestServer(t)

	// 全局 fake_a/detail：60 秒内最多 3 次
	if err := db.DB.Model(&models.QuotaCost{}).
		Where("group_code = ? AND interface = ?", "fake_a", "detail").
		Updates(map[string]interface{}{"limit_count": 3, "window_sec": 60, "interval": 0}).Error; err != nil {
		t.Fatalf("设置窗口限流失败: %v", err)
	}
	delCostCache("fake_a", "detail")

	token := registerUser(t, srv, "q_user9", "q_user9@example.com", "pass1234")
	detail := func(tk string) (int, []byte) {
		return doRaw(t, srv, http.MethodGet, "/fake_a/detail"+buildQuery(map[string]string{
			"bookId": "1",
		}), nil, authHeader(tk))
	}

	// 窗口内可突发：连续 3 次全部放行（无 baseUrl 会走到 handler 报 400，但不应被限流）
	for i := 1; i <= 3; i++ {
		status, raw := detail(token)
		if status == http.StatusTooManyRequests {
			t.Fatalf("窗口内第 %d 次请求被限流，want 放行（body=%s）", i, truncate(string(raw), 200))
		}
	}

	// 第 4 次：60 秒窗口内已累计 3 次，429
	status, raw := detail(token)
	if status != http.StatusTooManyRequests {
		t.Fatalf("窗口用满后第 4 次请求 status = %d, want 429（body=%s）", status, truncate(string(raw), 200))
	}
	if !strings.Contains(string(raw), "过于频繁") {
		t.Fatalf("错误提示 = %s, want 含「过于频繁」", truncate(string(raw), 200))
	}

	// 管理员豁免一切速率限制
	status, _ = detail(adminToken(t, srv))
	if status == http.StatusTooManyRequests {
		t.Fatal("管理员请求被限流，want 豁免")
	}
}

// TestRateLimitWindowPlanOverride 套餐级窗口计数优先于全局固定间隔：
// 全局配 interval=60（第 2 次必 429），套餐配 limit_count=5/window_sec=60，
// 生效的是套餐窗口（前 3 次连续放行）——同时覆盖「套餐级 > 全局」与「窗口 > 间隔」两条优先级
func TestRateLimitWindowPlanOverride(t *testing.T) {
	srv := newTestServer(t)

	freePlanID := planIDByCode(t, "free")

	if err := db.DB.Model(&models.QuotaCost{}).
		Where("group_code = ? AND interface = ?", "fake_b", "search").
		Updates(map[string]interface{}{"interval": 60, "limit_count": 0}).Error; err != nil {
		t.Fatalf("设置全局间隔限流失败: %v", err)
	}
	delCostCache("fake_b", "search")

	if err := db.DB.Create(&models.QuotaCostPlan{
		PlanID:     freePlanID,
		GroupCode:  "fake_b",
		Interface:  "search",
		Interval:   60,
		LimitCount: 5,
		WindowSec:  60,
	}).Error; err != nil {
		t.Fatalf("写入套餐级窗口限流失败: %v", err)
	}

	token := registerUser(t, srv, "q_user10", "q_user10@example.com", "pass1234")
	if err := db.DB.Model(&models.User{}).
		Where("username = ?", "q_user10").
		Update("plan_id", freePlanID).Error; err != nil {
		t.Fatalf("绑定 free 套餐失败: %v", err)
	}

	rank := func() (int, []byte) {
		return doRaw(t, srv, http.MethodGet, "/fake_b/search"+buildQuery(map[string]string{
			"query": "测试",
		}), nil, authHeader(token))
	}

	// 套餐窗口 5 次未用满：连续 3 次都应放行（若误用全局 interval=60，第 2 次即 429）
	for i := 1; i <= 3; i++ {
		status, raw := rank()
		if status == http.StatusTooManyRequests {
			t.Fatalf("套餐级窗口限流未生效：第 %d 次请求被限流（body=%s）", i, truncate(string(raw), 200))
		}
	}
}

// TestRateLimitWindowExpiry 滑动窗口过期恢复：1 秒窗口内只允许 1 次，
// 第 2 次 429；待最早一次滑出窗口后（>1s）恢复放行
func TestRateLimitWindowExpiry(t *testing.T) {
	srv := newTestServer(t)

	if err := db.DB.Model(&models.QuotaCost{}).
		Where("group_code = ? AND interface = ?", "fake_b", "detail").
		Updates(map[string]interface{}{"limit_count": 1, "window_sec": 1, "interval": 0}).Error; err != nil {
		t.Fatalf("设置窗口限流失败: %v", err)
	}
	delCostCache("fake_b", "detail")

	token := registerUser(t, srv, "q_user11", "q_user11@example.com", "pass1234")
	related := func() (int, []byte) {
		return doRaw(t, srv, http.MethodGet, "/fake_b/detail"+buildQuery(map[string]string{
			"bookId": "1",
		}), nil, authHeader(token))
	}

	if status, raw := related(); status == http.StatusTooManyRequests {
		t.Fatalf("第 1 次请求即被限流，want 放行（body=%s）", truncate(string(raw), 200))
	}

	status, raw := related()
	if status != http.StatusTooManyRequests {
		t.Fatalf("1 秒窗口内第 2 次请求 status = %d, want 429（body=%s）", status, truncate(string(raw), 200))
	}

	time.Sleep(1200 * time.Millisecond)

	// 最早一次已滑出 1 秒窗口，恢复放行
	status, raw = related()
	if status == http.StatusTooManyRequests {
		t.Fatalf("窗口过期后仍被限流，want 放行（body=%s）", truncate(string(raw), 200))
	}
}

// TestRateLimitWindowAdminConfig 管理接口写入窗口限流（PUT /admin/quota-costs/:id）：
// 配置落库并立即对普通用户生效；非法值（window_sec=0、负数）返回 400
func TestRateLimitWindowAdminConfig(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)

	var row models.QuotaCost
	if err := db.DB.Where("group_code = ? AND interface = ?", "fake_b", "chapter").First(&row).Error; err != nil {
		t.Fatalf("查询 QuotaCost 行失败: %v", err)
	}
	path := fmt.Sprintf("/admin/quota-costs/%d", row.ID)

	// 非法值：window_sec 必须 >0、limit_count 不能为负
	status, env := doJSON(t, srv, http.MethodPut, path,
		map[string]interface{}{"limit_count": 2, "window_sec": 0}, authHeader(admin))
	if status != http.StatusBadRequest {
		t.Fatalf("window_sec=0 status = %d, want 400（msg=%s）", status, env.Msg)
	}
	status, env = doJSON(t, srv, http.MethodPut, path,
		map[string]interface{}{"limit_count": -1}, authHeader(admin))
	if status != http.StatusBadRequest {
		t.Fatalf("limit_count=-1 status = %d, want 400（msg=%s）", status, env.Msg)
	}

	// 正常写入：60 秒内最多 2 次
	status, env = doJSON(t, srv, http.MethodPut, path,
		map[string]interface{}{"limit_count": 2, "window_sec": 60, "interval": 0}, authHeader(admin))
	if status != http.StatusOK {
		t.Fatalf("写入窗口限流 status = %d, want 200（msg=%s）", status, env.Msg)
	}
	var saved models.QuotaCost
	if err := db.DB.First(&saved, row.ID).Error; err != nil {
		t.Fatalf("重新查询 QuotaCost 失败: %v", err)
	}
	if saved.LimitCount != 2 || saved.WindowSec != 60 {
		t.Fatalf("落库值 limit_count=%d window_sec=%d, want 2/60", saved.LimitCount, saved.WindowSec)
	}
	delCostCache("fake_b", "chapter")

	// 立即生效：2 次放行，第 3 次 429
	token := registerUser(t, srv, "q_user12", "q_user12@example.com", "pass1234")
	author := func() (int, []byte) {
		return doRaw(t, srv, http.MethodGet, "/fake_b/chapter"+buildQuery(map[string]string{
			"bookId": "1",
		}), nil, authHeader(token))
	}
	for i := 1; i <= 2; i++ {
		if status, raw := author(); status == http.StatusTooManyRequests {
			t.Fatalf("窗口内第 %d 次请求被限流，want 放行（body=%s）", i, truncate(string(raw), 200))
		}
	}
	status, raw := author()
	if status != http.StatusTooManyRequests {
		t.Fatalf("管理接口配置的窗口限流未生效：第 3 次请求 status = %d, want 429（body=%s）", status, truncate(string(raw), 200))
	}
}

// TestRateLimitWindowPlanUpsert 套餐级限流 upsert 的冲突更新覆盖新字段：
// 同一 (plan_id, group_code, interface) 二次 upsert 只更新一行，limit_count/window_sec 随之更新
func TestRateLimitWindowPlanUpsert(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)
	freePlanID := planIDByCode(t, "free")

	// 直接用套餐级行验证 upsert 语义（首次插入 + 同键二次覆盖）
	payload := map[string]interface{}{
		"plan_id":     freePlanID,
		"group_code":  "fake_a",
		"interface":   "explore",
		"interval":    0,
		"limit_count": 10,
		"window_sec":  60,
	}
	if status, env := doJSON(t, srv, http.MethodPost, "/admin/quota-costs/plans/upsert", payload, authHeader(admin)); status != http.StatusOK {
		t.Fatalf("首次 upsert status = %d, want 200（msg=%s）", status, env.Msg)
	}

	// 二次 upsert（仅改次数）走 OnConflict 更新分支
	payload["limit_count"] = 20
	if status, env := doJSON(t, srv, http.MethodPost, "/admin/quota-costs/plans/upsert", payload, authHeader(admin)); status != http.StatusOK {
		t.Fatalf("二次 upsert status = %d, want 200（msg=%s）", status, env.Msg)
	}

	var rows []models.QuotaCostPlan
	if err := db.DB.Where("plan_id = ? AND group_code = ? AND interface = ?", freePlanID, "fake_a", "explore").
		Find(&rows).Error; err != nil {
		t.Fatalf("查询套餐限流行失败: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("套餐限流行数 = %d, want 1（upsert 应为覆盖更新）", len(rows))
	}
	if rows[0].LimitCount != 20 || rows[0].WindowSec != 60 {
		t.Fatalf("二次 upsert 后 limit_count=%d window_sec=%d, want 20/60", rows[0].LimitCount, rows[0].WindowSec)
	}
}

// TestLimiterIdleReclaimPreservesLowRate 回归（窗口限流改造引入）：闲置回收不得削弱低速率限流。
// 回收判定必须按各限流器自己的配置周期计算——若统一用 10 分钟阈值，interval=3600（每 1 小时
// 1 次）的限流器会在闲置 10 分钟后被清掉，下一次请求因限流器重建而被提前放行。
func TestLimiterIdleReclaimPreservesLowRate(t *testing.T) {
	// 普通速率（interval=2 / window=60）：闲置 11 分钟应判定可回收（避免 map 无限增长）
	if expired, winExpired := gate.LimiterIdleExpiredForTest(2, 60, 11*time.Minute); !expired || !winExpired {
		t.Fatalf("普通速率配置闲置 11 分钟应可回收：interval=%v window=%v", expired, winExpired)
	}

	// 低速率（3600 秒周期）：闲置 11 分钟绝不可回收
	if expired, winExpired := gate.LimiterIdleExpiredForTest(3600, 3600, 11*time.Minute); expired || winExpired {
		t.Fatalf("低速率配置（3600s）闲置 11 分钟不应被回收：interval=%v window=%v", expired, winExpired)
	}

	// 超过 2×周期（安全余量）后仍应回收，否则低速率配置的 key 永不释放
	if expired, _ := gate.LimiterIdleExpiredForTest(3600, 0, 2*time.Hour+time.Minute); !expired {
		t.Fatal("低速率配置闲置超过 2×interval 后应可回收")
	}
}
