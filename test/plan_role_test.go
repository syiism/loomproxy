package test

// 套餐绑定角色 + 角色单选集成测试：
// - 管理后台分配/清除套餐自动把用户角色同步为套餐绑定角色（seed 默认 free→user、vip→vip、admin→admin）
// - 用户角色接口为单选（role_code 整体替换；旧 role_codes 数组负载 400）
// - 已有 admin 角色的用户不被套餐同步降级；卡密自助兑换永不自动授予 admin 角色
// - 套餐 CRUD 支持角色绑定（role_id 校验 / 0 解绑）

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	"loomproxy/db"
)

// roleCodesOf 直接查库返回用户当前的全部角色 code
func roleCodesOf(t *testing.T, username string) []string {
	t.Helper()
	var codes []string
	if err := db.DB.Table("user_roles").
		Select("roles.code").
		Joins("JOIN roles ON roles.id = user_roles.role_id").
		Joins("JOIN users ON users.id = user_roles.user_id").
		Where("users.username = ?", username).
		Order("roles.id ASC").
		Scan(&codes).Error; err != nil {
		t.Fatalf("查询用户 %s 角色失败: %v", username, err)
	}
	return codes
}

// assertRoles 断言用户角色恰好为 want（单选模型下应只有一个元素）
func assertRoles(t *testing.T, username string, want ...string) {
	t.Helper()
	got := roleCodesOf(t, username)
	if len(got) != len(want) {
		t.Fatalf("用户 %s 角色 = %v，期望 %v", username, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("用户 %s 角色 = %v，期望 %v", username, got, want)
		}
	}
}

func putUserPlan(t *testing.T, srv *httptest.Server, adminTok, uid string, planID interface{}) int {
	t.Helper()
	status, env := doJSON(t, srv, http.MethodPut, "/admin/users/"+uid+"/plan",
		map[string]interface{}{"plan_id": planID}, authHeader(adminTok))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("分配套餐失败（status=%d code=%d msg=%s）", status, env.Code, env.Msg)
	}
	return status
}

// TestPlanRoleSyncOnPlanChange 管理后台套餐变更同步角色；角色接口单选
func TestPlanRoleSyncOnPlanChange(t *testing.T) {
	srv := newTestServer(t)
	adminTok := adminToken(t, srv)

	registerUser(t, srv, "plansync", "plansync@t.cn", "passw0rd1")
	uid := itoa(userIDByName(t, "plansync"))
	vipID := planIDByCode(t, "vip")

	// 注册默认 user 角色（free 套餐同样绑定 user）
	assertRoles(t, "plansync", "user")

	// 换到 vip 套餐 → 角色自动切换为 vip
	putUserPlan(t, srv, adminTok, uid, vipID)
	assertRoles(t, "plansync", "vip")

	// 清除套餐 → 按免费版回退 user
	putUserPlan(t, srv, adminTok, uid, nil)
	assertRoles(t, "plansync", "user")

	// 角色单选：role_code 整体替换
	status, env := doJSON(t, srv, http.MethodPost, "/admin/users/"+uid+"/roles",
		map[string]interface{}{"role_code": "vip"}, authHeader(adminTok))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("单选改角色失败（status=%d code=%d msg=%s）", status, env.Code, env.Msg)
	}
	assertRoles(t, "plansync", "vip")

	// 再改回 user，确认整体替换而非叠加
	if status, env = doJSON(t, srv, http.MethodPost, "/admin/users/"+uid+"/roles",
		map[string]interface{}{"role_code": "user"}, authHeader(adminTok)); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("改回角色失败（status=%d code=%d msg=%s）", status, env.Code, env.Msg)
	}
	assertRoles(t, "plansync", "user")

	// 旧的多选负载（role_codes 数组）应被拒绝：绑定失败而非静默忽略
	status, _ = doJSON(t, srv, http.MethodPost, "/admin/users/"+uid+"/roles",
		map[string]interface{}{"role_codes": []string{"user", "vip"}}, authHeader(adminTok))
	if status != http.StatusBadRequest {
		t.Fatalf("旧 role_codes 数组负载应 400，实际 %d", status)
	}

	// 不存在的角色应 400
	status, _ = doJSON(t, srv, http.MethodPost, "/admin/users/"+uid+"/roles",
		map[string]interface{}{"role_code": "no_such_role"}, authHeader(adminTok))
	if status != http.StatusBadRequest {
		t.Fatalf("不存在的角色应 400，实际 %d", status)
	}
}

// TestPlanRoleAdminNotDowngraded admin 用户换套餐不被同步降级（降级需显式走角色操作）
func TestPlanRoleAdminNotDowngraded(t *testing.T) {
	srv := newTestServer(t)
	adminTok := adminToken(t, srv)

	registerUser(t, srv, "adminkeep", "adminkeep@t.cn", "passw0rd1")
	uid := itoa(userIDByName(t, "adminkeep"))
	vipID := planIDByCode(t, "vip")

	if status, env := doJSON(t, srv, http.MethodPost, "/admin/users/"+uid+"/roles",
		map[string]interface{}{"role_code": "admin"}, authHeader(adminTok)); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("授予 admin 角色失败（status=%d code=%d msg=%s）", status, env.Code, env.Msg)
	}

	// 换到 vip 套餐：绑定角色 vip 与现角色 admin 不同 → 跳过同步，不降级
	putUserPlan(t, srv, adminTok, uid, vipID)
	assertRoles(t, "adminkeep", "admin")

	// 清除套餐回退 free（绑定 user）→ 同样不动 admin
	putUserPlan(t, srv, adminTok, uid, nil)
	assertRoles(t, "adminkeep", "admin")
}

// TestPlanRoleOnRedeem 卡密兑换同步绑定角色；自助兑换不授予 admin
func TestPlanRoleOnRedeem(t *testing.T) {
	srv := newTestServer(t)
	adminTok := adminToken(t, srv)
	vipID := planIDByCode(t, "vip")

	mkCode := func(planID interface{}) string {
		status, env := doJSON(t, srv, http.MethodPost, "/admin/redeem-codes",
			map[string]interface{}{"plan_id": planID, "duration_days": 30, "count": 1}, authHeader(adminTok))
		if status != http.StatusOK || env.Code != 0 {
			t.Fatalf("生成卡密失败（status=%d code=%d msg=%s）", status, env.Code, env.Msg)
		}
		codes, _ := env.dataMap(t)["codes"].([]interface{})
		if len(codes) != 1 {
			t.Fatalf("卡密响应缺少明文: %v", env.dataMap(t))
		}
		return codes[0].(string)
	}

	userTok := registerUser(t, srv, "redeemrole", "redeemrole@t.cn", "passw0rd1")
	assertRoles(t, "redeemrole", "user")

	// 兑换 vip 卡密 → 角色自动切换为 vip
	if status, env := doJSON(t, srv, http.MethodPost, "/user/redeem",
		map[string]interface{}{"code": mkCode(vipID)}, authHeader(userTok)); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("兑换 vip 卡密失败（status=%d code=%d msg=%s）", status, env.Code, env.Msg)
	}
	assertRoles(t, "redeemrole", "vip")

	// 建自定义角色 + 绑定该角色的高等级套餐，兑换后角色随之切换
	status, env := doJSON(t, srv, http.MethodPost, "/admin/roles",
		map[string]interface{}{"code": "svip_test", "name": "SVIP测试"}, authHeader(adminTok))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("创建角色失败（status=%d code=%d msg=%s）", status, env.Code, env.Msg)
	}
	var svipRoleID uint
	if err := db.DB.Table("roles").Select("id").Where("code = ?", "svip_test").Scan(&svipRoleID).Error; err != nil || svipRoleID == 0 {
		t.Fatalf("查询角色 svip_test 失败: %v", err)
	}
	if status, env = doJSON(t, srv, http.MethodPost, "/admin/quotas/plans",
		map[string]interface{}{"code": "svip_plan", "name": "SVIP套餐", "level": 5, "role_id": svipRoleID}, authHeader(adminTok)); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("创建套餐失败（status=%d code=%d msg=%s）", status, env.Code, env.Msg)
	}
	if status, env = doJSON(t, srv, http.MethodPost, "/user/redeem",
		map[string]interface{}{"code": mkCode(planIDByCode(t, "svip_plan"))}, authHeader(userTok)); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("兑换 svip 卡密失败（status=%d code=%d msg=%s）", status, env.Code, env.Msg)
	}
	assertRoles(t, "redeemrole", "svip_test")

	// 套餐改绑 admin 角色后续兑：套餐照常续期，但角色不因自助兑换变成 admin
	var adminRoleID uint
	if err := db.DB.Table("roles").Select("id").Where("code = ?", "admin").Scan(&adminRoleID).Error; err != nil || adminRoleID == 0 {
		t.Fatalf("查询 admin 角色失败: %v", err)
	}
	if status, env = doJSON(t, srv, http.MethodPatch, "/admin/quotas/plans/"+itoa(planIDByCode(t, "svip_plan")),
		map[string]interface{}{"role_id": adminRoleID}, authHeader(adminTok)); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("套餐改绑角色失败（status=%d code=%d msg=%s）", status, env.Code, env.Msg)
	}
	if status, env = doJSON(t, srv, http.MethodPost, "/user/redeem",
		map[string]interface{}{"code": mkCode(planIDByCode(t, "svip_plan"))}, authHeader(userTok)); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("再次兑换失败（status=%d code=%d msg=%s）", status, env.Code, env.Msg)
	}
	assertRoles(t, "redeemrole", "svip_test")

	// 解绑（role_id=0）后落库为 NULL
	if status, env = doJSON(t, srv, http.MethodPatch, "/admin/quotas/plans/"+itoa(planIDByCode(t, "svip_plan")),
		map[string]interface{}{"role_id": 0}, authHeader(adminTok)); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("套餐解绑角色失败（status=%d code=%d msg=%s）", status, env.Code, env.Msg)
	}
	var roleIDNull sql.NullInt64
	if err := db.DB.Raw("SELECT role_id FROM quota_plans WHERE code = ?", "svip_plan").Scan(&roleIDNull).Error; err != nil {
		t.Fatalf("查询 role_id 失败: %v", err)
	}
	if roleIDNull.Valid {
		t.Fatalf("解绑后 role_id 应为 NULL（got=%d）", roleIDNull.Int64)
	}

	// 绑定不存在的角色应 400
	status, _ = doJSON(t, srv, http.MethodPost, "/admin/quotas/plans",
		map[string]interface{}{"code": "bad_plan", "name": "坏套餐", "role_id": 99999}, authHeader(adminTok))
	if status != http.StatusBadRequest {
		t.Fatalf("绑定不存在角色应 400，实际 %d", status)
	}

	// 被套餐绑定的角色不可删（svip_plan 此前绑定过 svip_test，现解绑后应可删）
	var svipRoleID2 uint
	if err := db.DB.Table("roles").Select("id").Where("code = ?", "svip_test").Scan(&svipRoleID2).Error; err != nil || svipRoleID2 == 0 {
		t.Fatalf("查询角色 svip_test 失败: %v", err)
	}
	// 重新绑定后再测删除拦截
	if status, env = doJSON(t, srv, http.MethodPatch, "/admin/quotas/plans/"+itoa(planIDByCode(t, "svip_plan")),
		map[string]interface{}{"role_id": svipRoleID2}, authHeader(adminTok)); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("套餐重新绑定角色失败（status=%d code=%d msg=%s）", status, env.Code, env.Msg)
	}
	if status, _ = doJSON(t, srv, http.MethodDelete, "/admin/roles/"+itoa(svipRoleID2), nil, authHeader(adminTok)); status != http.StatusBadRequest {
		t.Fatalf("被套餐绑定的角色删除应 400，实际 %d", status)
	}
	// 解绑后再删：先解绑套餐，再清掉用户角色（redeemrole 此前兑换同步了 svip_test，仍关联该角色）
	if status, env = doJSON(t, srv, http.MethodPatch, "/admin/quotas/plans/"+itoa(planIDByCode(t, "svip_plan")),
		map[string]interface{}{"role_id": 0}, authHeader(adminTok)); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("套餐解绑角色失败（status=%d code=%d msg=%s）", status, env.Code, env.Msg)
	}
	if status, env = doJSON(t, srv, http.MethodPost, "/admin/users/"+itoa(userIDByName(t, "redeemrole"))+"/roles",
		map[string]interface{}{"role_code": "user"}, authHeader(adminTok)); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("清用户角色失败（status=%d code=%d msg=%s）", status, env.Code, env.Msg)
	}
	if status, env = doJSON(t, srv, http.MethodDelete, "/admin/roles/"+itoa(svipRoleID2), nil, authHeader(adminTok)); status != http.StatusOK || env.Code != 0 {
		var rows []map[string]interface{}
		db.DB.Raw("SELECT id, code, role_id FROM quota_plans").Scan(&rows)
		t.Logf("调试 quota_plans: %v", rows)
		t.Fatalf("解绑后删除角色失败（status=%d code=%d msg=%s）", status, env.Code, env.Msg)
	}
}
