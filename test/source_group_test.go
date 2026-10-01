package test

// 数据源分组集成测试：组是「归类与筛选视图」——增删改、成员整盘提交、按组批量套用限额
// （落库仍是每源一行 quota_limits），以及首页/下游输出的组信息。
// 限额、计费、限速的键一律是数据源码，组不出现在任何解析路径里。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"loomproxy/db"
	"loomproxy/models"
)

type dashSource struct {
	SourceCode     string `json:"source_code"`
	GroupName      string `json:"group_name"`
	EffectiveTotal int64  `json:"effective_total"`
}

type dashResp struct {
	Sources []dashSource `json:"sources"`
	Groups  []struct {
		ID    uint   `json:"id"`
		Name  string `json:"name"`
		Count int64  `json:"count"`
	} `json:"groups"`
	UngroupedCount int64  `json:"ungrouped_count"`
	PlanName       string `json:"plan_name"`
}

func dashboard(t *testing.T, srv *httptest.Server, token string) dashResp {
	t.Helper()
	status, env := doJSON(t, srv, http.MethodGet, "/quota/dashboard", nil, authHeader(token))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("GET /quota/dashboard 失败（status=%d code=%d msg=%s）", status, env.Code, env.Msg)
	}
	var out dashResp
	if err := json.Unmarshal(env.Data, &out); err != nil {
		t.Fatalf("解析 dashboard 响应失败: %v", err)
	}
	return out
}

func groupIDOfSource(t *testing.T, source string) uint {
	t.Helper()
	var ds models.DataSource
	if err := db.DB.Where("name = ?", source).First(&ds).Error; err != nil {
		t.Fatalf("查询数据源 %s 失败: %v", source, err)
	}
	if ds.GroupID == nil {
		return 0
	}
	return *ds.GroupID
}

func createGroup(t *testing.T, srv *httptest.Server, admin, name string, sort int) uint {
	t.Helper()
	status, env := doJSON(t, srv, http.MethodPost, "/admin/source-groups", map[string]interface{}{
		"name":       name,
		"sort_order": sort,
	}, authHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("建组 %s 失败（status=%d code=%d msg=%s）", name, status, env.Code, env.Msg)
	}
	var created struct {
		ID uint `json:"id"`
	}
	if err := json.Unmarshal(env.Data, &created); err != nil || created.ID == 0 {
		t.Fatalf("建组 %s 响应缺少 id: %v", name, err)
	}
	return created.ID
}

func putGroupMembers(t *testing.T, srv *httptest.Server, admin string, groupID uint, names []string) (int, apiEnvelope) {
	t.Helper()
	return doJSON(t, srv, http.MethodPut, fmt.Sprintf("/admin/source-groups/%d/members", groupID),
		map[string]interface{}{"source_names": names}, authHeader(admin))
}

// TestSourceGroupLifecycle 组的增删改与成员归属：一源至多一组，删组只摘归属不删源
func TestSourceGroupLifecycle(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)

	novel := createGroup(t, srv, admin, "小说", 10)
	audio := createGroup(t, srv, admin, "有声", 20)

	// 同名组：先查再拦，不能让唯一约束撞到驱动变成 500
	if status, env := doJSON(t, srv, http.MethodPost, "/admin/source-groups", map[string]interface{}{"name": "小说"}, authHeader(admin)); status != http.StatusConflict {
		t.Fatalf("同名组 status = %d, want 409（msg=%s）", status, env.Msg)
	}

	if status, env := putGroupMembers(t, srv, admin, novel, []string{fakeA, fakeB}); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("成员整盘提交失败（status=%d msg=%s）", status, env.Msg)
	}
	if got := groupIDOfSource(t, fakeA); got != novel {
		t.Errorf("%s 的 group_id = %d, want %d", fakeA, got, novel)
	}
	if got := groupIDOfSource(t, fakeB); got != novel {
		t.Errorf("%s 的 group_id = %d, want %d", fakeB, got, novel)
	}

	// 整盘提交：列表外的旧成员回落未分组
	if status, env := putGroupMembers(t, srv, admin, novel, []string{fakeA}); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("收缩成员失败（status=%d msg=%s）", status, env.Msg)
	}
	if got := groupIDOfSource(t, fakeB); got != 0 {
		t.Errorf("被移出整盘列表的 %s group_id = %d, want 0（未分组）", fakeB, got)
	}

	// 一源至多一组：进新组等于自动从他组移出
	if status, env := putGroupMembers(t, srv, admin, audio, []string{fakeA}); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("迁移成员失败（status=%d msg=%s）", status, env.Msg)
	}
	if got := groupIDOfSource(t, fakeA); got != audio {
		t.Fatalf("%s 的 group_id = %d, want %d（他组应自动摘除）", fakeA, got, audio)
	}

	// 清空成员：空列表必须真的把整组摘干净（NOT IN 空集在 SQL 里恒不成立，拼上去会原地不动）
	if status, env := putGroupMembers(t, srv, admin, audio, []string{}); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("清空成员失败（status=%d msg=%s）", status, env.Msg)
	}
	if got := groupIDOfSource(t, fakeA); got != 0 {
		t.Fatalf("清空成员后 %s group_id = %d, want 0", fakeA, got)
	}
	if status, env := putGroupMembers(t, srv, admin, audio, []string{fakeA}); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("重新加回成员失败（status=%d msg=%s）", status, env.Msg)
	}

	// 不存在的源码要显式拒绝，不能静默丢掉让管理员以为勾上了
	if status, _ := putGroupMembers(t, srv, admin, audio, []string{"no_such_source"}); status != http.StatusNotFound {
		t.Errorf("未知成员源码 status = %d, want 404", status)
	}

	// 单源改组走 PATCH /admin/data-sources/:id
	dsID := adminDataSourceID(t, srv, admin, fakeB)
	patch := fmt.Sprintf("/admin/data-sources/%d", dsID)
	if status, env := doJSON(t, srv, http.MethodPatch, patch, map[string]interface{}{"group_id": novel}, authHeader(admin)); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("单源改组失败（status=%d msg=%s）", status, env.Msg)
	}
	if got := groupIDOfSource(t, fakeB); got != novel {
		t.Errorf("%s 的 group_id = %d, want %d", fakeB, got, novel)
	}
	if status, env := doJSON(t, srv, http.MethodPatch, patch, map[string]interface{}{"group_id": 999999}, authHeader(admin)); status != http.StatusNotFound {
		t.Errorf("挂到不存在的组 status = %d, want 404（msg=%s）", status, env.Msg)
	}
	if status, env := doJSON(t, srv, http.MethodPatch, patch, map[string]interface{}{"group_id": novel, "clear_group": true}, authHeader(admin)); status != http.StatusBadRequest {
		t.Errorf("group_id 与 clear_group 同时给 status = %d, want 400（msg=%s）", status, env.Msg)
	}
	if status, env := doJSON(t, srv, http.MethodPatch, patch, map[string]interface{}{"clear_group": true}, authHeader(admin)); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("摘出分组失败（status=%d msg=%s）", status, env.Msg)
	}
	if got := groupIDOfSource(t, fakeB); got != 0 {
		t.Errorf("clear_group 后 %s group_id = %d, want 0", fakeB, got)
	}

	// 改名
	var renamed string
	if status, env := doJSON(t, srv, http.MethodPatch, fmt.Sprintf("/admin/source-groups/%d", novel),
		map[string]interface{}{"name": "小说精选"}, authHeader(admin)); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("改组名失败（status=%d msg=%s）", status, env.Msg)
	}
	if err := db.DB.Model(&models.SourceGroup{}).Where("id = ?", novel).Select("name").Scan(&renamed).Error; err != nil || renamed != "小说精选" {
		t.Errorf("组名 = %q, want 小说精选（err=%v）", renamed, err)
	}

	// 删组：成员回落未分组，源本身一行不少
	var before int64
	db.DB.Model(&models.DataSource{}).Count(&before)
	if status, env := doJSON(t, srv, http.MethodDelete, fmt.Sprintf("/admin/source-groups/%d", audio), nil, authHeader(admin)); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("删组失败（status=%d msg=%s）", status, env.Msg)
	}
	var after int64
	db.DB.Model(&models.DataSource{}).Count(&after)
	if after != before {
		t.Errorf("删组后数据源行数 %d → %d, want 不变", before, after)
	}
	if got := groupIDOfSource(t, fakeA); got != 0 {
		t.Errorf("删组后 %s group_id = %d, want 0（不得留指向已删组的引用）", fakeA, got)
	}

	// 列表：成员数与未分组计数
	status, env := doJSON(t, srv, http.MethodGet, "/admin/source-groups", nil, authHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("GET /admin/source-groups 失败（status=%d）", status)
	}
	var list struct {
		Groups []struct {
			ID          uint   `json:"id"`
			Name        string `json:"name"`
			MemberCount int64  `json:"member_count"`
		} `json:"groups"`
		UngroupedCount int64 `json:"ungrouped_count"`
	}
	if err := json.Unmarshal(env.Data, &list); err != nil {
		t.Fatalf("解析组列表失败: %v", err)
	}
	if len(list.Groups) != 1 || list.Groups[0].Name != "小说精选" || list.Groups[0].MemberCount != 0 {
		t.Errorf("组列表 = %+v, want 只剩「小说精选」且成员数为 0", list.Groups)
	}
	if list.UngroupedCount != before {
		t.Errorf("未分组计数 = %d, want %d（全部源都未分组了）", list.UngroupedCount, before)
	}

	// 同名组删除后可立刻重建（硬删，不留占住唯一索引的行）
	if got := createGroup(t, srv, admin, "小说", 30); got == 0 {
		t.Error("删除同名组后重建失败")
	}

	// 非管理员不得触碰组管理
	userToken := registerUser(t, srv, "grp_user1", "grp_user1@example.com", "pass1234")
	if status, _ := doJSON(t, srv, http.MethodGet, "/admin/source-groups", nil, authHeader(userToken)); status != http.StatusForbidden {
		t.Errorf("普通用户读组列表 status = %d, want 403", status)
	}
}

// TestSourceGroupOutputsInDashboardAndCatalog 首页与下游输出的组信息，以及组改动后的缓存即时失效
func TestSourceGroupOutputsInDashboardAndCatalog(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)

	novel := createGroup(t, srv, admin, "小说", 10)
	if status, env := putGroupMembers(t, srv, admin, novel, []string{fakeA, fakeB}); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("成员提交失败（status=%d msg=%s）", status, env.Msg)
	}

	// 管理端列表带 group_id（面板据此渲染归属选择）
	dsID := adminDataSourceID(t, srv, admin, fakeA)
	status, env := doJSON(t, srv, http.MethodGet, "/admin/data-sources", nil, authHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("GET /admin/data-sources 失败（status=%d）", status)
	}
	var rows []struct {
		ID      int   `json:"id"`
		GroupID *uint `json:"group_id"`
	}
	if err := json.Unmarshal(env.Data, &rows); err != nil {
		t.Fatalf("解析管理列表失败: %v", err)
	}
	found := false
	for _, r := range rows {
		if r.ID == dsID {
			found = true
			if r.GroupID == nil || *r.GroupID != novel {
				t.Errorf("管理列表 %s group_id = %v, want %d", fakeA, r.GroupID, novel)
			}
		}
	}
	if !found {
		t.Fatalf("管理列表未包含 %s", fakeA)
	}

	// 首页：源带组名，groups 出筛选栏计数，未分组单独计
	dash := dashboard(t, srv, admin)
	byCode := map[string]dashSource{}
	for _, s := range dash.Sources {
		byCode[s.SourceCode] = s
	}
	if got := byCode[fakeA].GroupName; got != "小说" {
		t.Errorf("首页 %s 的 group_name = %q, want 小说", fakeA, got)
	}
	// 计数断言一律从响应自身推导：测试包里别的用例会往全局注册表补声明源，
	// seed 按声明播种，所以「库里一共有几个源」不是常数
	if len(dash.Groups) != 1 || dash.Groups[0].Name != "小说" || dash.Groups[0].Count != 2 {
		t.Errorf("首页分组项 = %+v, want 单组「小说」计数 2", dash.Groups)
	}
	var wantUngrouped int64
	for _, s := range dash.Sources {
		if s.GroupName == "" {
			wantUngrouped++
		}
	}
	if dash.UngroupedCount != wantUngrouped {
		t.Errorf("首页未分组计数 = %d, want %d（按响应里 group_name 为空的源数）", dash.UngroupedCount, wantUngrouped)
	}
	if byCode[fakeC].GroupName != "" {
		t.Errorf("未分组的 %s group_name = %q, want 空", fakeC, byCode[fakeC].GroupName)
	}

	// 下游清单下发组名（客户端不依赖，但要拿得到）
	var items []struct {
		ID    string `json:"id"`
		Group string `json:"group"`
	}
	status, env = doJSON(t, srv, http.MethodGet, "/datasources", nil, nil)
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("GET /datasources 失败（status=%d）", status)
	}
	if err := json.Unmarshal(env.Data, &items); err != nil {
		t.Fatalf("解析 /datasources 失败: %v", err)
	}
	groups := map[string]string{}
	for _, it := range items {
		groups[it.ID] = it.Group
	}
	if groups[fakeA] != "小说" {
		t.Errorf("/datasources 的 %s group = %q, want 小说（改组后缓存未失效？）", fakeA, groups[fakeA])
	}
	if groups[fakeC] != "" {
		t.Errorf("/datasources 的 %s group = %q, want 空", fakeC, groups[fakeC])
	}

	// 组改名后下游视图必须立刻反映（写操作要失效缓存）
	if status, env := doJSON(t, srv, http.MethodPatch, fmt.Sprintf("/admin/source-groups/%d", novel),
		map[string]interface{}{"name": "小说精选"}, authHeader(admin)); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("改组名失败（status=%d msg=%s）", status, env.Msg)
	}
	status, env = doJSON(t, srv, http.MethodGet, "/datasources", nil, nil)
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("二次 GET /datasources 失败（status=%d）", status)
	}
	items = items[:0]
	if err := json.Unmarshal(env.Data, &items); err != nil {
		t.Fatalf("解析 /datasources 失败: %v", err)
	}
	renamed := map[string]string{}
	for _, it := range items {
		renamed[it.ID] = it.Group
	}
	if renamed[fakeA] != "小说精选" {
		t.Errorf("改组名后 %s group = %q, want 小说精选（缓存未失效）", fakeA, renamed[fakeA])
	}

	// 停用的组：展示侧按未分组处理，源照样可见可用
	if status, env := doJSON(t, srv, http.MethodPatch, fmt.Sprintf("/admin/source-groups/%d", novel),
		map[string]interface{}{"status": 0}, authHeader(admin)); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("停用组失败（status=%d msg=%s）", status, env.Msg)
	}
	dash = dashboard(t, srv, admin)
	if len(dash.Groups) != 0 {
		t.Errorf("停用组后首页仍出分组项: %+v", dash.Groups)
	}
	if len(dash.Sources) == 0 {
		t.Error("停用组后首页源为空，want 源照常可见（分组不是准入开关）")
	}
}

// TestApplyGroupLimitsWritesPerSourceRows 按组套用限额：存储仍是每源一行，键不含组
func TestApplyGroupLimitsWritesPerSourceRows(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)
	userToken := registerUser(t, srv, "grp_user2", "grp_user2@example.com", "pass1234")

	empty := createGroup(t, srv, admin, "空组", 5)
	if status, env := doJSON(t, srv, http.MethodPost, fmt.Sprintf("/admin/source-groups/%d/apply-limits", empty),
		map[string]interface{}{"plan_code": "free", "limit": 7}, authHeader(admin)); status != http.StatusBadRequest {
		t.Fatalf("空组套用限额 status = %d, want 400（msg=%s）", status, env.Msg)
	}

	novel := createGroup(t, srv, admin, "小说", 10)
	if status, env := putGroupMembers(t, srv, admin, novel, []string{fakeA, fakeB}); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("成员提交失败（status=%d msg=%s）", status, env.Msg)
	}

	status, env := doJSON(t, srv, http.MethodPost, fmt.Sprintf("/admin/source-groups/%d/apply-limits", novel),
		map[string]interface{}{"plan_code": "free", "limit": 7}, authHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("套用限额失败（status=%d msg=%s）", status, env.Msg)
	}
	out := env.dataMap(t)
	if out["applied"] != float64(2) {
		t.Errorf("applied = %v, want 2", out["applied"])
	}

	// 落库口径：quota_limits 每源一行，不新增按组的行
	var rows []models.QuotaLimit
	if err := db.DB.Where("plan_id = ? AND scope = ? AND target IN ?", planIDByCode(t, "free"), "source", []string{fakeA, fakeB}).
		Find(&rows).Error; err != nil {
		t.Fatalf("查询 quota_limits 失败: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("free 套餐下两个源的限额行数 = %d, want 2（整盘更新不得追加重复行）", len(rows))
	}
	for _, r := range rows {
		if r.Limit != 7 {
			t.Errorf("%s 的限额 = %d, want 7", r.Target, r.Limit)
		}
	}

	// 组名不得成为任何限额行的键（组只出现在 source_groups / data_sources.group_id）
	var byGroup int64
	db.DB.Model(&models.QuotaLimit{}).Where("target = ?", "小说").Count(&byGroup)
	if byGroup != 0 {
		t.Errorf("quota_limits 出现了按组名键控的行 %d 条, want 0", byGroup)
	}
	// 用户侧立刻生效
	dash := dashboard(t, srv, userToken)
	byCode := map[string]dashSource{}
	for _, s := range dash.Sources {
		byCode[s.SourceCode] = s
	}
	if got := byCode[fakeA].EffectiveTotal; got != 7 {
		t.Errorf("套用后 %s 的 effective_total = %d, want 7", fakeA, got)
	}
	// 不在组里的源不受影响
	if got := byCode[fakeC].EffectiveTotal; got == 7 {
		t.Errorf("未入组的 %s 被误改限额", fakeC)
	}

	// 不限（-1）同样按源写入
	if status, env := doJSON(t, srv, http.MethodPost, fmt.Sprintf("/admin/source-groups/%d/apply-limits", novel),
		map[string]interface{}{"plan_code": "free", "limit": -1}, authHeader(admin)); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("套用不限失败（status=%d msg=%s）", status, env.Msg)
	}
	var unlimited models.QuotaLimit
	var rowc int64
	if err := db.DB.Where("plan_id = ? AND scope = ? AND target = ?", planIDByCode(t, "free"), "source", fakeA).
		First(&unlimited).Error; err != nil {
		t.Fatalf("复查 %s 限额行失败: %v", fakeA, err)
	}
	if unlimited.Limit != -1 {
		t.Errorf("%s 的限额 = %d, want -1（不限）", fakeA, unlimited.Limit)
	}
	if err := db.DB.Model(&models.QuotaLimit{}).
		Where("plan_id = ? AND scope = ? AND target = ?", planIDByCode(t, "free"), "source", fakeA).
		Count(&rowc).Error; err != nil || rowc != 1 {
		t.Errorf("整盘套用后 %s 的限额行数 = %d, want 1（err=%v）", fakeA, rowc, err)
	}

	// 未知套餐
	if status, _ := doJSON(t, srv, http.MethodPost, fmt.Sprintf("/admin/source-groups/%d/apply-limits", novel),
		map[string]interface{}{"plan_code": "nope", "limit": 1}, authHeader(admin)); status != http.StatusNotFound {
		t.Errorf("未知套餐 status = %d, want 404", status)
	}
}
