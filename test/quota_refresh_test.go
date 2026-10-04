package test

// 单日额度手动刷新（待办清单 P41）的端到端用例。
//
// 要钉住的是三件事，缺一件这个功能就会变成「说不清自己做了什么」的那种按钮：
//  1. 刷的是**起算点**，不是限额——所以放开的是「再一天的量」，紧接着的第二次消耗照样撞 429；
//  2. **流水一行都不许少**：删行等于毁掉「今天到底用了多少」的证据；
//  3. **判定与读数同起算点**：面板的「已用」必须跟着水印走，否则一处归零、另一处还挂着旧值。

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"loomproxy/db"
	"loomproxy/models"
)

func refreshQuotaAs(t *testing.T, srv *httptest.Server, token, uid string) (int, apiEnvelope) {
	t.Helper()
	return doJSON(t, srv, http.MethodPost, "/admin/users/"+uid+"/refresh-quota", nil, authHeader(token))
}

func quotaAdminView(t *testing.T, srv *httptest.Server, admin, uid string) map[string]interface{} {
	t.Helper()
	status, env := doJSON(t, srv, http.MethodGet, "/admin/users/"+uid+"/quota", nil, authHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("额度详情 status = %d code = %d msg = %s", status, env.Code, env.Msg)
	}
	return env.dataMap(t)
}

// userLedger 该用户在流水表里的行数与合计（跨全天，不看任何起算点）
func userLedger(t *testing.T, username string) (rows, sum int64) {
	t.Helper()
	var u models.User
	if err := db.DB.Where("username = ?", username).First(&u).Error; err != nil {
		t.Fatalf("查用户 %s 失败: %v", username, err)
	}
	if err := db.DB.Model(&models.QuotaUsageLog{}).Where("user_id = ?", u.ID).Count(&rows).Error; err != nil {
		t.Fatalf("数流水行失败: %v", err)
	}
	if err := db.DB.Model(&models.QuotaUsageLog{}).Where("user_id = ?", u.ID).
		Select("COALESCE(SUM(cost), 0)").Scan(&sum).Error; err != nil {
		t.Fatalf("合计流水失败: %v", err)
	}
	return rows, sum
}

// usedFromAdminView 额度详情里某数据源的「当日已用」
func usedFromAdminView(t *testing.T, view map[string]interface{}, sourceCode string) int64 {
	t.Helper()
	raw, _ := view["items"].([]interface{})
	for _, r := range raw {
		it, _ := r.(map[string]interface{})
		if it["source_code"] == sourceCode {
			v, _ := it["used"].(float64)
			return int64(v)
		}
	}
	t.Fatalf("额度详情里没有 %s 这一行（items=%d 条）", sourceCode, len(raw))
	return -1
}

// TestRefreshUserQuotaReopensDailyWindow 刷完能再用一次，而限额没被偷偷改大；流水一行没少。
func TestRefreshUserQuotaReopensDailyWindow(t *testing.T) {
	srv := newTestServer(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(upstream.Close)
	setAUpstream(t, upstream.URL)
	setSearchCost(t, 1)

	if err := db.DB.Model(&models.QuotaLimit{}).
		Where("plan_id = ? AND scope = ? AND target = ?", planIDByCode(t, "free"), "source", "fake_a").
		Update("limit", 1).Error; err != nil {
		t.Fatalf("改限额失败: %v", err)
	}

	admin := adminToken(t, srv)
	token := registerUser(t, srv, "q_refresh1", "q_refresh1@example.com", "pass1234")
	uid := strconv.Itoa(int(userIDByName(t, "q_refresh1")))

	if status, raw := searchA(t, srv, token); status != http.StatusOK {
		t.Fatalf("首次请求 status = %d, want 200（body=%s）", status, truncate(string(raw), 200))
	}
	if status, raw := searchA(t, srv, token); status != http.StatusTooManyRequests {
		t.Fatalf("限额 1 用满后应 429，实得 %d（body=%s）", status, truncate(string(raw), 200))
	}
	rowsBefore, sumBefore := userLedger(t, "q_refresh1")
	if rowsBefore != 1 || sumBefore != 1 {
		t.Fatalf("前置读数应为 1 行 1 点，实得 %d 行 %d 点", rowsBefore, sumBefore)
	}

	status, env := refreshQuotaAs(t, srv, admin, uid)
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("刷新额度 status = %d code = %d msg = %s", status, env.Code, env.Msg)
	}
	data := env.dataMap(t)
	if usedBefore, _ := data["used_before"].(float64); usedBefore != 1 {
		t.Errorf("响应 used_before = %v, want 1——刷掉了多少必须如实报，不然管理员不知道自己放开了什么", data["used_before"])
	}
	if resetAt, ok := data["quota_reset_at"].(string); !ok || resetAt == "" {
		t.Errorf("响应没带新的起算时刻: %+v", data)
	}

	// 刷完照常可用一次；而**紧接着的第二次照样撞墙**——放开的是「再一天的量」，不是把 limit 改了
	if status, raw := searchA(t, srv, token); status != http.StatusOK {
		t.Fatalf("刷新后应放行，实得 %d（body=%s）", status, truncate(string(raw), 200))
	}
	status, raw := searchA(t, srv, token)
	if status != http.StatusTooManyRequests {
		t.Errorf("刷新后再用满应 429，实得 %d（body=%s）——限额被改大了还是水印被当成不限额用了？",
			status, truncate(string(raw), 200))
	}

	// 流水只追加：刷新不删行、不写冲正
	rowsAfter, sumAfter := userLedger(t, "q_refresh1")
	if rowsAfter < rowsBefore {
		t.Errorf("流水行数从 %d 变成 %d——刷新额度不许删行", rowsBefore, rowsAfter)
	}
	if sumAfter != sumBefore+1 {
		t.Errorf("流水合计从 %d 变成 %d, want %d（刷新不该动账本，只该动起算点）", sumAfter, sumBefore, sumBefore+1)
	}

	// 判定与读数同起算点：面板看到的「已用」是水印之后的那 1 点，而账本上总共 2 点
	view := quotaAdminView(t, srv, admin, uid)
	if got := usedFromAdminView(t, view, "fake_a"); got != 1 {
		t.Errorf("面板当日已用 = %d, want 1（账本合计 %d；差值正是被起算点排除掉的旧用量）", got, sumAfter)
	}
	if view["quota_reset_at"] == nil {
		t.Errorf("额度详情没下发 quota_reset_at——「已用 1」没有起算点就说不然是怎么算的")
	}
	if view["usage_since"] == nil {
		t.Errorf("额度详情没下发 usage_since——面板必须能显示「从几点开始算」")
	}
}

// TestRefreshQuotaWatermarkExpiresAtMidnight 水印过期不靠定时任务：零点之后起算点回到零点。
// 做法是把水印写成昨天（等价于「昨天刷的、今天已经跨零点」），再写成此刻。
func TestRefreshQuotaWatermarkExpiresAtMidnight(t *testing.T) {
	srv := newTestServer(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(upstream.Close)
	setAUpstream(t, upstream.URL)
	setSearchCost(t, 1)

	admin := adminToken(t, srv)
	token := registerUser(t, srv, "q_refresh2", "q_refresh2@example.com", "pass1234")
	uidNum := userIDByName(t, "q_refresh2")
	uid := strconv.Itoa(int(uidNum))

	if status, _ := searchA(t, srv, token); status != http.StatusOK {
		t.Fatalf("消耗一次失败")
	}

	setResetAt := func(v time.Time) {
		t.Helper()
		if err := db.DB.Model(&models.User{}).Where(map[string]interface{}{"id": uidNum}).
			Update("quota_reset_at", v).Error; err != nil {
			t.Fatalf("写水印失败: %v", err)
		}
	}

	yesterday := time.Now().Add(-24 * time.Hour)
	setResetAt(yesterday)
	view := quotaAdminView(t, srv, admin, uid)
	if got := usedFromAdminView(t, view, "fake_a"); got != 1 {
		t.Errorf("昨天的水印在今天应已失效（起算点回到零点），已用却读成 %d, want 1", got)
	}

	now := time.Now()
	setResetAt(now)
	view = quotaAdminView(t, srv, admin, uid)
	if got := usedFromAdminView(t, view, "fake_a"); got != 0 {
		t.Errorf("水印写成此刻后已用应归 0，实得 %d", got)
	}
}

// TestRefreshQuotaPermissions 只有管理员会话能刷：普通用户 403，管理员的长期密钥也进不去（P26）。
func TestRefreshQuotaPermissions(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)
	registerUser(t, srv, "q_perm1", "q_perm1@example.com", "pass1234")
	userToken := registerUser(t, srv, "q_perm2", "q_perm2@example.com", "pass1234")
	uid := strconv.Itoa(int(userIDByName(t, "q_perm2")))

	if status, _ := refreshQuotaAs(t, srv, userToken, uid); status == http.StatusOK {
		t.Errorf("普通用户会话竟能刷自己的额度——单日额度就成了自助水龙头")
	} else if status != http.StatusForbidden && status != http.StatusUnauthorized {
		t.Errorf("普通用户期望 401/403，实得 %d", status)
	}

	_, adminKey := func() (string, string) {
		st, env := doJSON(t, srv, http.MethodPost, "/apikey", map[string]string{"name": "ci-refresh"}, authHeader(admin))
		if st != http.StatusOK || env.Code != 0 {
			t.Fatalf("给管理员建密钥失败: %d %s", st, env.Msg)
		}
		plain, _ := env.dataMap(t)["key"].(string)
		return "", plain
	}()
	for _, f := range []struct {
		headers map[string]string
		query   string
	}{
		{map[string]string{"X-API-Key": adminKey}, ""},
		{nil, "?api_key=" + adminKey},
	} {
		status, _ := doJSON(t, srv, http.MethodPost, "/admin/users/"+uid+"/refresh-quota"+f.query, nil, f.headers)
		if status == http.StatusOK {
			t.Errorf("管理员密钥竟能刷额度（/admin/* 只认会话，P26）")
		} else if status != http.StatusUnauthorized && status != http.StatusForbidden {
			t.Errorf("管理员密钥期望 401/403，实得 %d", status)
		}
	}

	// 用户不存在是 404，不是「刷了个空」
	if status, _ := refreshQuotaAs(t, srv, admin, "999999"); status != http.StatusNotFound {
		t.Errorf("不存在的用户期望 404，实得 %d", status)
	}
}
