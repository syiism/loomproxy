package test

// /datasources 缓存失效集成测试：管理端数据源变更后，缓存视图（匿名/登录）立即失效，
// 不等 CACHE_TTL（默认 300s）自然过期。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// datasourceNames 请求匿名视图的 /datasources，返回数据源标识（name）列表
func datasourceNames(t *testing.T, srv *httptest.Server) map[string]bool {
	t.Helper()
	status, env := doJSON(t, srv, http.MethodGet, "/datasources", nil, nil)
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("GET /datasources 失败（status=%d code=%d msg=%s）", status, env.Code, env.Msg)
	}
	var items []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(env.Data, &items); err != nil {
		t.Fatalf("解析 /datasources 响应失败: %v", err)
	}
	names := make(map[string]bool, len(items))
	for _, it := range items {
		names[it.ID] = true
	}
	return names
}

// adminDataSourceID 取管理后台数据源列表中指定 name 的数值 ID
func adminDataSourceID(t *testing.T, srv *httptest.Server, token, name string) int {
	t.Helper()
	status, env := doJSON(t, srv, http.MethodGet, "/admin/data-sources", nil, authHeader(token))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("GET /admin/data-sources 失败（status=%d）", status)
	}
	var items []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(env.Data, &items); err != nil {
		t.Fatalf("解析管理数据源列表失败: %v", err)
	}
	for _, it := range items {
		if it.Name == name {
			return it.ID
		}
	}
	t.Fatalf("管理列表中未找到数据源 %s", name)
	return 0
}

func TestDatasourcesCacheInvalidatedOnUpdate(t *testing.T) {
	srv := newTestServer(t)

	before := datasourceNames(t, srv)
	if !before["fake_c"] {
		t.Fatalf("seed 数据源应包含 fake_c，实际 %v", before)
	}

	token := adminToken(t, srv)
	id := adminDataSourceID(t, srv, token, "fake_c")

	// 禁用数据源：匿名视图应立即不再包含（此前需等 300s 缓存过期）
	status, env := doJSON(t, srv, http.MethodPatch, "/admin/data-sources/"+itoa(uint(id)),
		map[string]interface{}{"status": 0}, authHeader(token))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("禁用数据源失败（status=%d msg=%s）", status, env.Msg)
	}
	if after := datasourceNames(t, srv); after["fake_c"] {
		t.Fatalf("禁用后 /datasources 仍含 fake_c，缓存未即时失效")
	}

	// 恢复启用：同样立即生效
	status, env = doJSON(t, srv, http.MethodPatch, "/admin/data-sources/"+itoa(uint(id)),
		map[string]interface{}{"status": 1}, authHeader(token))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("恢复数据源失败（status=%d msg=%s）", status, env.Msg)
	}
	if restored := datasourceNames(t, srv); !restored["fake_c"] {
		t.Fatalf("恢复启用后 /datasources 仍缺 fake_c，缓存未即时失效")
	}
}

func TestDatasourcesCacheInvalidatedOnPlanLinkChange(t *testing.T) {
	srv := newTestServer(t)
	token := adminToken(t, srv)

	anon := datasourceNames(t, srv)
	if len(anon) == 0 {
		t.Fatal("匿名视图应有免费套餐数据源")
	}

	// 从免费套餐移除其包含的第一个数据源，匿名视图应立即减少
	status, env := doJSON(t, srv, http.MethodGet, "/admin/data-sources", nil, authHeader(token))
	if status != http.StatusOK {
		t.Fatalf("GET /admin/data-sources 失败（status=%d）", status)
	}
	var dsItems []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(env.Data, &dsItems); err != nil {
		t.Fatalf("解析数据源列表失败: %v", err)
	}
	var targetID int
	var targetName string
	for _, it := range dsItems {
		if anon[it.Name] {
			targetID, targetName = it.ID, it.Name
			break
		}
	}
	if targetID == 0 {
		t.Fatal("未找到免费套餐包含的数据源")
	}

	var freePlanID int
	{
		status, env := doJSON(t, srv, http.MethodGet, "/admin/quotas/plans", nil, authHeader(token))
		if status != http.StatusOK {
			t.Fatalf("GET /admin/quotas/plans 失败（status=%d）", status)
		}
		var plans []struct {
			ID   int    `json:"id"`
			Code string `json:"code"`
		}
		if err := json.Unmarshal(env.Data, &plans); err != nil {
			t.Fatalf("解析套餐列表失败: %v", err)
		}
		for _, p := range plans {
			if p.Code == "free" {
				freePlanID = p.ID
			}
		}
	}
	if freePlanID == 0 {
		t.Fatal("未找到 free 套餐")
	}

	status, env = doJSON(t, srv, http.MethodDelete,
		"/admin/quotas/plans/"+itoa(uint(freePlanID))+"/data-sources/"+itoa(uint(targetID)),
		nil, authHeader(token))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("移除套餐数据源关联失败（status=%d msg=%s）", status, env.Msg)
	}

	if after := datasourceNames(t, srv); after[targetName] {
		t.Fatalf("套餐关联移除后匿名视图仍含 %s，缓存未即时失效", targetName)
	}
}
