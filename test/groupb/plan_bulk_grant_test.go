package groupb

// 自定义套餐的源授权现在支持批量：一次请求建多行 scope=source 的限额（授权与限额同一行，P34 方案 A）。
// 这组用例守的是「批量 = 多次单条授权在同一事务里、幂等、限额由调用方显式给」这条不变式。

import (
	"loomproxy/db"
	"loomproxy/gate"
	"loomproxy/models"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// createCustomPlan 建一个自定义套餐（非内置，不吃 attachSourceToBuiltinPlans 的自动挂载），返回 id。
func createCustomPlan(t *testing.T, srv *httptest.Server, code string) uint {
	t.Helper()
	admin := adminToken(t, srv)
	status, env := doJSON(t, srv, http.MethodPost, "/admin/quotas/plans", map[string]interface{}{
		"code": code, "name": code, "level": 5,
	}, authHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("创建自定义套餐失败（status=%d msg=%s）", status, env.Msg)
	}
	m := env.DataMap(t)
	id, ok := m["id"].(float64)
	if !ok {
		t.Fatalf("创建套餐响应缺少 id：%v", env.Data)
	}
	return uint(id)
}

// planSourceLimit 取「套餐 × 源」那行限额的 limit 值。
func planSourceLimit(t *testing.T, planID uint, name string) int64 {
	t.Helper()
	var row models.QuotaLimit
	if err := db.DB.Where("plan_id = ? AND scope = ? AND target = ?", planID, "source", name).First(&row).Error; err != nil {
		t.Fatalf("查限额行失败 %s: %v", name, err)
	}
	return row.Limit
}

// grantOne 走单条端点授权一个源（用于造出不同限额，检验克隆「保留各自限额」）。
func grantOne(t *testing.T, srv *httptest.Server, admin string, planID uint, name string, limit int64) {
	t.Helper()
	status, env := doJSON(t, srv, http.MethodPost, "/admin/quotas/limits", map[string]interface{}{
		"plan_id": planID, "scope": "source", "target": name, "limit": limit,
	}, authHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("授权 %s 失败（status=%d msg=%s）", name, status, env.Msg)
	}
}

// TestBulkGrantPlanSources 一次请求授权多个源、幂等跳过、非法名拒绝、缺 limit 拒绝。
func TestBulkGrantPlanSources(t *testing.T) {
	srv := newTestServer(t)
	planID := createCustomPlan(t, srv, "bulk_p1")
	admin := adminToken(t, srv)

	// 一次授权 3 个源
	status, env := doJSON(t, srv, http.MethodPost, "/admin/quotas/plans/"+itoa(planID)+"/sources", map[string]interface{}{
		"sources": []string{"fake_a", "fake_b", "fake_c"}, "limit": 100,
	}, authHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("批量授权失败（status=%d msg=%s）", status, env.Msg)
	}
	if d := env.DataMap(t); d["created"].(float64) != 3 || d["skipped"].(float64) != 0 {
		t.Fatalf("批量授权统计错误：%v", env.Data)
	}
	for _, n := range []string{"fake_a", "fake_b", "fake_c"} {
		if !gate.PlanHasSource(planID, n) {
			t.Fatalf("批量授权后 %s 未被授权", n)
		}
	}
	if l := planSourceLimit(t, planID, "fake_a"); l != 100 {
		t.Fatalf("fake_a 限额 = %d, want 100", l)
	}

	// 幂等：重复调用全部跳过、不重复建行
	status, env = doJSON(t, srv, http.MethodPost, "/admin/quotas/plans/"+itoa(planID)+"/sources", map[string]interface{}{
		"sources": []string{"fake_a", "fake_b", "fake_c"}, "limit": 100,
	}, authHeader(admin))
	if status != http.StatusOK {
		t.Fatalf("重复批量授权 status=%d", status)
	}
	if d := env.DataMap(t); d["created"].(float64) != 0 || d["skipped"].(float64) != 3 {
		t.Fatalf("重复批量授权应全跳过：%v", env.Data)
	}

	// 含非法源名 → 400 且提示不存在；且不能污染已有授权
	status, env = doJSON(t, srv, http.MethodPost, "/admin/quotas/plans/"+itoa(planID)+"/sources", map[string]interface{}{
		"sources": []string{"fake_a", "no_such_src"}, "limit": 100,
	}, authHeader(admin))
	if status != http.StatusBadRequest || !strings.Contains(env.Msg, "数据源不存在") {
		t.Fatalf("非法源名应 400 且提示不存在（status=%d msg=%s）", status, env.Msg)
	}
	if !gate.PlanHasSource(planID, "fake_a") {
		t.Fatalf("非法名请求污染了已有授权")
	}

	// 缺 limit → 400（与 P69 一致，避免静默落成 0）
	status, _ = doJSON(t, srv, http.MethodPost, "/admin/quotas/plans/"+itoa(planID)+"/sources", map[string]interface{}{
		"sources": []string{"fake_a"},
	}, authHeader(admin))
	if status != http.StatusBadRequest {
		t.Fatalf("缺 limit 应 400，status=%d", status)
	}
}
