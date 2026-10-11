package groupb

// 从现有套餐复制数据源授权：保留各自限额、幂等跳过、不能从自身复制。

import (
	"net/http"
	"testing"
)

// TestClonePlanSources 复制后含源套餐全部源、各自限额保留、已存在不重复建、自复制拒绝。
func TestClonePlanSources(t *testing.T) {
	srv := newTestServer(t)
	src := createCustomPlan(t, srv, "clone_src")
	dst := createCustomPlan(t, srv, "clone_dst")
	admin := adminToken(t, srv)

	// 源套餐授权 fake_a=100, fake_b=200（不同限额，检验克隆「保留各自限额」）
	grantOne(t, srv, admin, src, "fake_a", 100)
	grantOne(t, srv, admin, src, "fake_b", 200)

	status, env := doJSON(t, srv, http.MethodPost, "/admin/quotas/plans/"+itoa(dst)+"/clone-sources", map[string]interface{}{
		"from_plan_id": src,
	}, authHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("复制源失败（status=%d msg=%s）", status, env.Msg)
	}
	if env.DataMap(t)["created"].(float64) != 2 {
		t.Fatalf("复制应建 2 行：%v", env.Data)
	}
	if l := planSourceLimit(t, dst, "fake_a"); l != 100 {
		t.Fatalf("复制后 fake_a 限额 = %d, want 100", l)
	}
	if l := planSourceLimit(t, dst, "fake_b"); l != 200 {
		t.Fatalf("复制后 fake_b 限额 = %d, want 200", l)
	}

	// 幂等：再复制一次全部跳过
	status, env = doJSON(t, srv, http.MethodPost, "/admin/quotas/plans/"+itoa(dst)+"/clone-sources", map[string]interface{}{
		"from_plan_id": src,
	}, authHeader(admin))
	if status != http.StatusOK {
		t.Fatalf("重复复制 status=%d", status)
	}
	if d := env.DataMap(t); d["created"].(float64) != 0 || d["skipped"].(float64) != 2 {
		t.Fatalf("重复复制应全跳过：%v", env.Data)
	}

	// 从自身复制 → 400
	status, _ = doJSON(t, srv, http.MethodPost, "/admin/quotas/plans/"+itoa(dst)+"/clone-sources", map[string]interface{}{
		"from_plan_id": dst,
	}, authHeader(admin))
	if status != http.StatusBadRequest {
		t.Fatalf("自复制应 400，status=%d", status)
	}
}
