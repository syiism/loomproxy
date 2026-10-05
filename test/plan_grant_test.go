package test

// 套餐授权 = 一行 scope=source 的限额（待办清单 P34，方案 A）。
// 这组用例守的是这次合并的那条不变式：**限额表是唯一真相**——
// 旧关联表 quota_plan_data_sources 里有没有行，一律不影响判定；
// 而「加一行限额」与「授权这个源」是同一个动作，删掉它就是回收。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"loomproxy/db"
	"loomproxy/gate"
	"loomproxy/models"
)

// requestDetail 以指定 token 请求 fake_b 的 detail 动作。
// 不配 baseUrl，所以授权在位时它走不到 200——用例只分辨「有没有被访问控制挡下」，
// 判据取响应文案而不是状态码：403 也可能是「接口未开放」，那句话才是 access 自己的。
func requestDetail(t *testing.T, srv *httptest.Server, token string) string {
	t.Helper()
	status, body := doRaw(t, srv, http.MethodGet, "/fake_b/detail", nil, authHeader(token))
	if status == http.StatusForbidden && strings.Contains(string(body), "套餐不包含") {
		return "plan-missing"
	}
	return "allowed"
}

// grantRow 取「套餐 × fake_b」那行限额
func grantRow(t *testing.T, planID uint) models.QuotaLimit {
	t.Helper()
	var row models.QuotaLimit
	err := db.DB.Where("plan_id = ? AND scope = ? AND target = ?", planID, "source", "fake_b").First(&row).Error
	if err != nil {
		t.Fatalf("查 fake_b 的限额行失败（plan=%d）: %v", planID, err)
	}
	return row
}

func countGrantRows(t *testing.T, planID uint) int64 {
	t.Helper()
	var n int64
	if err := db.DB.Model(&models.QuotaLimit{}).
		Where("plan_id = ? AND scope = ? AND target = ?", planID, "source", "fake_b").
		Count(&n).Error; err != nil {
		t.Fatalf("统计限额行失败: %v", err)
	}
	return n
}

// TestGrantIsTheLimitRow 删限额行即回收、加限额行即授权（走面板用的那两个端点）
func TestGrantIsTheLimitRow(t *testing.T) {
	srv := newTestServer(t)
	token := registerUser(t, srv, "grant_u1", "grant_u1@example.com", "pass1234")
	admin := adminToken(t, srv)
	freePlan := planIDByCode(t, "free")

	// seed 已把 fake_b 授权给三个内置套餐
	if got := requestDetail(t, srv, token); got != "allowed" {
		t.Fatalf("播种后普通用户被访问控制挡下（%s），want 放行", got)
	}

	// 播种出来的授权行带的是各套餐的默认限额（free 每源 100），不是 -1
	row := grantRow(t, freePlan)
	if row.Limit != 100 {
		t.Fatalf("free 套餐 fake_b 的播种限额 = %d, want 100", row.Limit)
	}

	// 面板删掉这一行 = 回收该源
	status, env := doJSON(t, srv, http.MethodDelete, "/admin/quotas/limits/"+itoa(row.ID), nil, authHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("删除限额行失败（status=%d msg=%s）", status, env.Msg)
	}
	if got := requestDetail(t, srv, token); got != "plan-missing" {
		t.Fatalf("删除限额行后仍放行（%s），want 403「套餐不包含」", got)
	}

	// 面板加回一行 = 重新授权（限额由调用方给）
	status, env = doJSON(t, srv, http.MethodPost, "/admin/quotas/limits", map[string]interface{}{
		"plan_id": freePlan, "scope": "source", "target": "fake_b", "limit": 50,
	}, authHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("新增限额行失败（status=%d msg=%s）", status, env.Msg)
	}
	if got := requestDetail(t, srv, token); got != "allowed" {
		t.Fatalf("加回限额行后仍被挡下（%s），want 放行", got)
	}
	if n := countGrantRows(t, freePlan); n != 1 {
		t.Fatalf("fake_b 的限额行数 = %d, want 1（重复行会让「限额即授权」读出不一致）", n)
	}

	// 程序侧授权落地的数值必须和播种同源：free 套餐的每源默认档是 100
	if err := gate.UngrantPlanSource(freePlan, "fake_b"); err != nil {
		t.Fatalf("回收 fake_b 授权失败: %v", err)
	}
	created, err := gate.GrantPlanSource(freePlan, "fake_b")
	if err != nil || !created {
		t.Fatalf("GrantPlanSource 应新建一行（created=%v err=%v）", created, err)
	}
	if row := grantRow(t, freePlan); row.Limit != 100 {
		t.Errorf("GrantPlanSource 建的行限额 = %d, want 100（与播种同一份默认档）", row.Limit)
	}
	// 同一目标再加一次必须撞 409，不能长出第二行
	if status, _ := doJSON(t, srv, http.MethodPost, "/admin/quotas/limits", map[string]interface{}{
		"plan_id": freePlan, "scope": "source", "target": "fake_b", "limit": 9,
	}, authHeader(admin)); status != http.StatusConflict {
		t.Errorf("重复授权 status = %d, want 409", status)
	}
}

// TestLegacyLinkTableGrantsNothing 旧关联表不再决定任何事：只有 link 行、没有限额行 → 403
func TestLegacyLinkTableGrantsNothing(t *testing.T) {
	srv := newTestServer(t)
	token := registerUser(t, srv, "grant_u2", "grant_u2@example.com", "pass1234")
	freePlan := planIDByCode(t, "free")

	// 这张表已经不在 AutoMigrate 清单里了（生产已 DROP，全新库不建它）。
	// 这条用例要验的是「**老库里它还在**」那个形状，所以自己把表建出来——
	// 建不出来就等于用例静默跳过，那才是这条守卫最怕的失效方式。
	if err := db.DB.Migrator().CreateTable(&models.QuotaPlanDataSource{}); err != nil {
		t.Fatalf("建旧关联表失败: %v", err)
	}
	if err := gate.UngrantPlanSource(freePlan, "fake_b"); err != nil {
		t.Fatalf("回收 fake_b 授权失败: %v", err)
	}
	// 往弃用表里塞一行「fake_b 属于 free」——这正是旧模型认为的授权
	link := models.QuotaPlanDataSource{PlanID: freePlan, DataSourceID: dataSourceIDByName(t, "fake_b")}
	if err := db.DB.Create(&link).Error; err != nil {
		t.Fatalf("写入旧关联行失败: %v", err)
	}

	if got := requestDetail(t, srv, token); got != "plan-missing" {
		t.Fatalf("旧关联表单方面存在时被判为可用（%s），want 仍 403——授权只认限额表", got)
	}
}

// TestSeedSurvivesDroppedLegacyTable 表被 DROP 之后 Seed 必须照常跑完。
// alignPlanGrants 过去无条件 `Find` 这张表，表没了就是「Seed 报错 → db.Init 之后起不来」，
// 症状长得像升级失败而不是迁移没做——所以这条守卫钉的是**起得来**，不是「搬了几行」。
func TestSeedSurvivesDroppedLegacyTable(t *testing.T) {
	newTestServer(t)
	if err := db.DB.Migrator().DropTable(&models.QuotaPlanDataSource{}); err != nil {
		t.Fatalf("DROP 旧关联表失败: %v", err)
	}
	if db.DB.Migrator().HasTable("quota_plan_data_sources") {
		t.Fatal("前置条件不成立：旧关联表还在")
	}
	before := countGrantRows(t, planIDByCode(t, "free"))
	if err := db.Seed(db.DB); err != nil {
		t.Fatalf("表已 DROP 时 Seed 失败: %v（升级路径会表现为服务起不来）", err)
	}
	if after := countGrantRows(t, planIDByCode(t, "free")); after != before {
		t.Errorf("Seed 改动了授权行数：%d → %d, want 不变", before, after)
	}
}

// TestSeedAlignsLegacyLinksToLimits 生产升级路径：旧关联行有、限额行没有 → Seed 补齐（幂等）
func TestSeedAlignsLegacyLinksToLimits(t *testing.T) {
	newTestServer(t)
	freePlan := planIDByCode(t, "free")

	// 造出升级前的形状：授权只记在旧关联表里，限额表里没有对应行。
	// 同时保留 free 在其它源上的限额行，免得 seedPlanSourceGrants 走「整套餐为空、铺全量」
	// 那条路径，把这条对齐步骤测成了假绿灯。
	if err := db.DB.Migrator().CreateTable(&models.QuotaPlanDataSource{}); err != nil {
		t.Fatalf("建旧关联表失败: %v", err)
	}
	link := models.QuotaPlanDataSource{PlanID: freePlan, DataSourceID: dataSourceIDByName(t, "fake_b")}
	if err := db.DB.Create(&link).Error; err != nil {
		t.Fatalf("写入旧关联行失败: %v", err)
	}
	if err := db.DB.Where("scope = ? AND target = ?", "source", "fake_b").Delete(&models.QuotaLimit{}).Error; err != nil {
		t.Fatalf("清 fake_b 限额行失败: %v", err)
	}
	if countGrantRows(t, freePlan) != 0 {
		t.Fatal("前置条件不成立：free 套餐的 fake_b 限额行没清干净")
	}

	if err := db.Seed(db.DB); err != nil {
		t.Fatalf("Seed 出错: %v", err)
	}
	if n := countGrantRows(t, freePlan); n != 1 {
		t.Fatalf("Seed 后 free 套餐的 fake_b 限额行 = %d, want 1（从旧关联表补齐）", n)
	}
	if row := grantRow(t, freePlan); row.Limit != -1 {
		t.Errorf("补齐行的限额 = %d, want -1（搬迁不得改变额度口径）", row.Limit)
	}

	// 幂等：再跑一次不得补第二行
	if err := db.Seed(db.DB); err != nil {
		t.Fatalf("二次 Seed 出错: %v", err)
	}
	if n := countGrantRows(t, freePlan); n != 1 {
		t.Fatalf("二次 Seed 后限额行 = %d, want 仍为 1", n)
	}
}

// TestSourceLimitChangeInvalidatesDatasourcesCache 限额增删要立刻改到 /datasources 视图（缓存失效已接上）
func TestSourceLimitChangeInvalidatesDatasourcesCache(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)
	freePlan := planIDByCode(t, "free")

	if names := datasourceNames(t, srv); !names["fake_b"] {
		t.Fatal("匿名视图初始就不含 fake_b，夹具与预期不符")
	}
	row := grantRow(t, freePlan)
	if _, env := doJSON(t, srv, http.MethodDelete, "/admin/quotas/limits/"+itoa(row.ID), nil, authHeader(admin)); env.Code != 0 {
		t.Fatalf("删除限额行失败: %s", env.Msg)
	}
	if names := datasourceNames(t, srv); names["fake_b"] {
		t.Fatal("回收授权后匿名视图仍含 fake_b，缓存未即时失效")
	}

	status, env := doJSON(t, srv, http.MethodPost, "/admin/quotas/limits", map[string]interface{}{
		"plan_id": freePlan, "scope": "source", "target": "fake_b", "limit": -1,
	}, authHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("新增限额行失败（status=%d msg=%s）", status, env.Msg)
	}
	if names := datasourceNames(t, srv); !names["fake_b"] {
		t.Fatal("授权后匿名视图仍不含 fake_b，缓存未即时失效")
	}
}

// TestSourceLimitRejectsUnknownTarget 目标必须是真实数据源码：写错名字不会报错、只会静默不发源
func TestSourceLimitRejectsUnknownTarget(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)

	status, env := doJSON(t, srv, http.MethodPost, "/admin/quotas/limits", map[string]interface{}{
		"plan_id": planIDByCode(t, "free"), "scope": "source", "target": "fake_不存在", "limit": 10,
	}, authHeader(admin))
	if status != http.StatusBadRequest {
		t.Fatalf("未知数据源码的限额行 status = %d, want 400（msg=%s）", status, env.Msg)
	}
	if !strings.Contains(env.Msg, "数据源不存在") {
		t.Fatalf("错误文案 = %s, want 含「数据源不存在」", env.Msg)
	}
}

// TestApplyGroupLimitsDoesNotGrant 按组套用限额只是改额度：不得替未授权的源补出授权行
func TestApplyGroupLimitsDoesNotGrant(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)
	freePlan := planIDByCode(t, "free")

	group := createGroup(t, srv, admin, "授权测试组", 40)
	if status, env := putGroupMembers(t, srv, admin, group, []string{"fake_a", "fake_b"}); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("设置组成员失败（status=%d msg=%s）", status, env.Msg)
	}
	if err := gate.UngrantPlanSource(freePlan, "fake_b"); err != nil {
		t.Fatalf("回收 fake_b 授权失败: %v", err)
	}

	status, env := doJSON(t, srv, http.MethodPost,
		"/admin/source-groups/"+itoa(group)+"/apply-limits",
		map[string]interface{}{"plan_code": "free", "limit": 9}, authHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("套用限额失败（status=%d msg=%s）", status, env.Msg)
	}
	out := env.dataMap(t)
	if out["applied"] != float64(1) {
		t.Errorf("applied = %v, want 1（只有已授权的 fake_a 该被改）", out["applied"])
	}
	raw, _ := json.Marshal(out["skipped_ungranted"])
	if !strings.Contains(string(raw), "fake_b") {
		t.Errorf("skipped_ungranted = %s, want 含 fake_b（漏了谁必须让操作者看见）", raw)
	}
	if n := countGrantRows(t, freePlan); n != 0 {
		t.Fatalf("套用限额给未授权的 fake_b 补出了 %d 行——「改额度」变成了「发权限」", n)
	}
}

// TestUserQuotaEditorMarksUngranted 用户额度编辑器要分清「未授权」与「不限额」
func TestUserQuotaEditorMarksUngranted(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)
	registerUser(t, srv, "grant_u3", "grant_u3@example.com", "pass1234")
	freePlan := planIDByCode(t, "free")

	var user models.User
	if err := db.DB.Where("username = ?", "grant_u3").First(&user).Error; err != nil {
		t.Fatalf("取用户失败: %v", err)
	}

	if err := gate.UngrantPlanSource(freePlan, "fake_b"); err != nil {
		t.Fatalf("回收 fake_b 授权失败: %v", err)
	}

	status, env := doJSON(t, srv, http.MethodGet, "/admin/users/"+itoa(user.ID)+"/quota", nil, authHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("读用户额度失败（status=%d msg=%s）", status, env.Msg)
	}
	var payload struct {
		Items []struct {
			SourceCode string `json:"source_code"`
			Granted    bool   `json:"granted"`
			PlanLimit  *int64 `json:"plan_limit"`
			Effective  int64  `json:"effective"`
		} `json:"items"`
	}
	if err := json.Unmarshal(env.Data, &payload); err != nil {
		t.Fatalf("解析用户额度失败: %v", err)
	}
	var grantedSeen, ungrantedSeen bool
	for _, it := range payload.Items {
		switch it.SourceCode {
		case "fake_a":
			grantedSeen = true
			if !it.Granted || it.PlanLimit == nil {
				t.Errorf("fake_a: granted=%v plan_limit=%v, want true 且有值", it.Granted, it.PlanLimit)
			}
		case "fake_b":
			ungrantedSeen = true
			// 旧形状在这里给的是 plan_limit=nil + effective=-1，面板读成「不限」——
			// 而实际是 403。granted 这一格就是为了让这两种情况不再长得一样。
			if it.Granted || it.PlanLimit != nil {
				t.Errorf("fake_b: granted=%v plan_limit=%v, want false 且 nil", it.Granted, it.PlanLimit)
			}
		}
	}
	if !grantedSeen || !ungrantedSeen {
		t.Fatalf("额度项里没同时出现 fake_a/fake_b（seen=%v/%v）", grantedSeen, ungrantedSeen)
	}
}
