package testkit

// 集成用例的共享小夹具（待办清单 P104 正式拆分的下沉面）。
//
// 为什么在这里而不是留在 test/：`go test ./...` 是**跨包并行**的，而 test/ 那 92 个文件属于同一个
// 连通分量——按"不拆共享夹具"这条路切组根本不存在切法（实测：能自由搬动的孤立文件只有 9 个、合计 1.5s）。
// 把跨组引用的夹具下沉到这里，两组各自留一份同名转发（`test/testserver_test.go` 与
// `test/groupb/forward_test.go`），**存量用例文件一行调用点都不用改**。
//
// 这一批的下沉判据只有一条：**被另一组用到、而实现长在对面那一组的文件里**（数出来 12 条，
// 全是函数，没有带方法的类型——那条区别是 P104 第一次尝试撤回换来的，见条目与报告 §13）。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"loomproxy/db"
	"loomproxy/gate"
	"loomproxy/models"
	"loomproxy/utils"
)

// PlanIDByCode 查套餐 id（用例里不许硬编码套餐 id）。
func PlanIDByCode(t *testing.T, code string) uint {
	t.Helper()
	var id uint
	if err := db.DB.Table("quota_plans").Select("id").Where("code = ?", code).Scan(&id).Error; err != nil || id == 0 {
		t.Fatalf("查询套餐 %s 失败（id=%d err=%v）", code, id, err)
	}
	return id
}

// DataSourceIDByName 查数据源 id。
func DataSourceIDByName(t *testing.T, name string) uint {
	t.Helper()
	var id uint
	if err := db.DB.Table("data_sources").Select("id").Where("name = ?", name).Scan(&id).Error; err != nil || id == 0 {
		t.Fatalf("查询数据源 %s 失败（id=%d err=%v）", name, id, err)
	}
	return id
}

// DelCostCache 清掉接口成本的命名缓存项。
func DelCostCache(sourceCode, action string) {
	utils.DefaultCache().Del(gate.CostCacheKey(sourceCode, action))
}

// SetAUpstream 给假源 fake_a 写平台默认 baseUrl。
func SetAUpstream(t *testing.T, upstreamURL string) {
	t.Helper()
	if err := db.DB.Create(&models.PlatformSourceConfig{
		SourceName: "fake_a",
		BaseURL:    upstreamURL,
	}).Error; err != nil {
		t.Fatalf("写入平台默认 baseUrl 失败: %v", err)
	}
}

// AdminDataSourceID 从管理端数据源列表按名称取 id。
func AdminDataSourceID(t *testing.T, srv *httptest.Server, token, name string) int {
	t.Helper()
	status, env := DoJSON(t, srv, http.MethodGet, "/admin/data-sources", nil, AuthHeader(token))
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

// DatasourceNames 取 /datasources 出口里出现过的数据源 id 集合。
func DatasourceNames(t *testing.T, srv *httptest.Server) map[string]bool {
	t.Helper()
	status, env := DoJSON(t, srv, http.MethodGet, "/datasources", nil, nil)
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

// InsertCallLog 预置一条调用明细（监控类用例的时间窗口都靠它摆）。
func InsertCallLog(t *testing.T, source, action string, status int, createdAt time.Time) {
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

// DBGetSetting 直读设置表（绕开设置缓存，用例要的是"库里到底是什么"）。
func DBGetSetting(t *testing.T, key string) string {
	t.Helper()
	var row models.SystemSetting
	if err := db.DB.Where(map[string]interface{}{"key": key}).First(&row).Error; err != nil {
		t.Fatalf("读设置 %s 失败: %v", key, err)
	}
	return row.Value
}

// WriteSettingValue 直写设置表的 value 并清缓存（用例造"库里填了什么"的形态）。
func WriteSettingValue(t *testing.T, key, value string) {
	t.Helper()
	if err := db.DB.Model(&models.SystemSetting{}).
		Where(map[string]interface{}{"key": key}).Update("value", value).Error; err != nil {
		t.Fatalf("改设置 %s 失败: %v", key, err)
	}
	db.InvalidateSettingCache(key)
}

// ListUserIDs 按查询串取 /admin/users 结果里的用户 id 集合。
func ListUserIDs(t *testing.T, srv *httptest.Server, admin, query string) map[uint]bool {
	t.Helper()
	status, env := DoJSON(t, srv, http.MethodGet, "/admin/users"+query, nil, AuthHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("GET /admin/users%s status=%d code=%d msg=%s", query, status, env.Code, env.Msg)
	}
	raw, _ := env.DataMap(t)["list"].([]interface{})
	out := map[uint]bool{}
	for _, r := range raw {
		m, _ := r.(map[string]interface{})
		id, _ := m["id"].(float64)
		out[uint(id)] = true
	}
	return out
}

// threeFormSeq 保证同一次跑里三种形态的用户名不重复（下沉前它是 test/ 的包级变量）。
var threeFormSeq atomic.Uint64

// ThreeFormUser 造一个"token / cookie / apiKey 三形态都可用"的用户，返回会话 token 与明文密钥。
func ThreeFormUser(t *testing.T, srv *httptest.Server) (string, string) {
	t.Helper()
	name := fmt.Sprintf("tf_%d", threeFormSeq.Add(1))
	token := RegisterUser(t, srv, name, name+"@example.com", "pass1234")

	status, env := DoJSON(t, srv, http.MethodPost, "/apikey", map[string]string{"name": "ci"}, AuthHeader(token))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("建密钥 status = %d code = %d msg = %s", status, env.Code, env.Msg)
	}
	plain, _ := env.DataMap(t)["key"].(string)
	if !strings.HasPrefix(plain, "lp_") {
		t.Fatalf("密钥格式异常: %q", plain)
	}
	return token, plain
}

// WriteDict 往 DATA_DIR 下写一个字典文件（数据文件类用例的产物准备）。
func WriteDict(t *testing.T, root, dir, name, body string) {
	t.Helper()
	full := filepath.Join(root, dir)
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatalf("建字典目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(full, name+".json"), []byte(body), 0o644); err != nil {
		t.Fatalf("写字典失败: %v", err)
	}
}
