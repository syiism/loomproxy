package test

// 禁用/删除用户之后，他手里的 API 密钥必须**立刻**失去访问（待办清单 P47）。
//
// 为什么单独钉一条：密钥的身份是 `utils.LookupApiKeyIdentity` 解析的，那条路不看会话——
// 管理员禁用一个人时吊销的是会话（`admin.go` 那句注释写的是「阻止已登录的禁用用户继续使用 API」），
// 而归属缓存的**命中分支不复查 `users.status`**：禁用之前只要用这把密钥打过一次网关，
// 身份就躺在缓存里，到 TTL（`CACHE_TTL` 默认 300 秒）过期之前照样按原归属通过。
// 所以这条断言的形状是「先打一次把缓存养起来 → 禁用 → 立刻再打」，
// 少了第一步就是纯机制测试，测不到那个窗口。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"loomproxy/base"
	"loomproxy/db"
	"loomproxy/models"
)

func TestApiKeyDisabledUserLosesAccessImmediately(t *testing.T) {
	srv := newTestServer(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(upstream.Close)
	setAUpstream(t, upstream.URL)
	setSearchCost(t, 1)

	admin := adminToken(t, srv)
	token := registerUser(t, srv, "ak_off1", "ak_off1@example.com", "pass1234")
	uid := userIDByName(t, "ak_off1")

	// 建一把密钥（明文只在创建响应里出现）
	status, env := doJSON(t, srv, http.MethodPost, "/apikey", map[string]string{"name": "p47"}, authHeader(token))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("创建密钥 status = %d msg = %s", status, env.Msg)
	}
	plain, _ := env.dataMap(t)["key"].(string)
	if plain == "" {
		t.Fatalf("创建响应没有 key: %+v", env.dataMap(t))
	}

	call := func(tag string) int {
		st, raw := doRaw(t, srv, http.MethodGet, "/fake_a/search"+buildQuery(map[string]string{"query": "测试"}),
			nil, map[string]string{"X-API-Key": plain})
		if st != http.StatusOK && st != http.StatusUnauthorized && st != http.StatusForbidden {
			t.Fatalf("%s: 非预期状态 %d（body=%s）", tag, st, truncate(string(raw), 200))
		}
		return st
	}
	usageCount := func() int64 {
		var n int64
		if err := db.DB.Model(&models.QuotaUsageLog{}).
			Where("user_id = ? AND group_code = ? AND interface = ?", uid, "fake_a", "search").
			Count(&n).Error; err != nil {
			t.Fatalf("查用量流水失败: %v", err)
		}
		return n
	}

	// 1) 先用一次：身份进入归属缓存，用量落 1 条
	if st := call("启用期间"); st != http.StatusOK {
		t.Fatalf("禁用前密钥调网关 status = %d, want 200", st)
	}
	before := usageCount()
	if before != 1 {
		t.Fatalf("禁用前用量流水 = %d, want 1（这条用例的前提是归属可用）", before)
	}

	// 2) 管理员禁用该用户
	status, env = doJSON(t, srv, http.MethodPatch, fmt.Sprintf("/admin/users/%d", uid),
		map[string]interface{}{"status": 0}, authHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("禁用用户失败: status=%d msg=%s", status, env.Msg)
	}

	// 3) **紧接着**再打一次：必须被拒。实测被拒的形状是 403 +「无效的 API Key」
	//    （网关在归属查不到人时给的就是这一条，不是 401——断言写成具体值，
	//    这样「因为别的原因变非 200」不会被当成通过）
	if st := call("禁用之后"); st != http.StatusForbidden {
		t.Fatalf("禁用之后密钥 status = %d, want 403——200 就是 P47 的原样：会话被吊销了，密钥这条通路还躺在归属缓存里", st)
	}
	if got := usageCount(); got != before {
		t.Errorf("禁用之后的那次调用仍然落了用量流水（%d → %d）——被拒的请求不该按归属扣额", before, got)
	}

	// 4) 启用回来：同一把密钥照旧可用（失效不是撤销，不能把人家的密钥弄死）
	status, env = doJSON(t, srv, http.MethodPatch, fmt.Sprintf("/admin/users/%d", uid),
		map[string]interface{}{"status": 1}, authHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("启用用户失败: status=%d msg=%s", status, env.Msg)
	}
	if st := call("重新启用之后"); st != http.StatusOK {
		t.Fatalf("重新启用后密钥 status = %d, want 200——禁用失效不等于撤销密钥", st)
	}
}

func TestApiKeyDeletedUserLosesAccessImmediately(t *testing.T) {
	srv := newTestServer(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(upstream.Close)
	setAUpstream(t, upstream.URL)
	setSearchCost(t, 1)

	admin := adminToken(t, srv)
	token := registerUser(t, srv, "ak_del1", "ak_del1@example.com", "pass1234")
	uid := userIDByName(t, "ak_del1")

	status, env := doJSON(t, srv, http.MethodPost, "/apikey", map[string]string{"name": "p47"}, authHeader(token))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("创建密钥失败: status=%d msg=%s", status, env.Msg)
	}
	plain, _ := env.dataMap(t)["key"].(string)

	// 养一次缓存
	if st, _ := doRaw(t, srv, http.MethodGet, "/fake_a/search"+buildQuery(map[string]string{"query": "测试"}),
		nil, map[string]string{"X-API-Key": plain}); st != http.StatusOK {
		t.Fatalf("删除前密钥调网关 status = %d, want 200", st)
	}

	// 软删除用户（会话吊销 + 归属缓存失效都在 DeleteUser 里）
	status, env = doJSON(t, srv, http.MethodDelete, fmt.Sprintf("/admin/users/%d", uid), nil, authHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("删除用户失败: status=%d msg=%s", status, env.Msg)
	}
	stDel, rawDel := doRaw(t, srv, http.MethodGet, "/fake_a/search"+buildQuery(map[string]string{"query": "测试"}),
		nil, map[string]string{"X-API-Key": plain})
	if stDel != http.StatusForbidden {
		t.Fatalf("删掉的用户密钥 status = %d, want 403（body=%s）", stDel, truncate(string(rawDel), 200))
	}
	// **这条断言为什么写 403 而不是「非 200」**：两个码分别指向两条通路——
	//   403「无效的 API Key」= `utils/auth.go:142`，归属**查库**没查到人才给的（缓存已失效，DB 分支跑了）；
	//   401「用户不存在」= `gate/access.go:170`，身份是从**缓存**里拿到的，往下走才发现库里没有这个人。
	// 所以 401 恰恰是「失效没做成」的形状；只断「非 200」的话两种结果都能过，这条就没牙齿了。
	// 变异验证分开量过：只摘 DeleteUser 的失效 → 这条红而禁用那条绿；只摘 UpdateUser 的 → 反过来红；
	// 把共用的 `InvalidateUserApiKeys` 查不到键 → 两条一起红。
}

// TestApiKeyIdentityFollowsUsernameChange 改用户名之后，密钥调用的**归属读数**必须跟到新名。
//
// 这条和上面两条是同一个洞的第三种形状：缓存里存的是 `ApiKeyIdentity{UserID, Username}`，
// 命中就不回库。禁用/删除是「该不该放行」的问题，改名是「这笔账记在谁名下」的问题——
// 计费随 user_id 所以钱是对的，`api_call_logs.username` 却会在 TTL 内一直写旧名，
// 而监控明细、书名榜的访问人数、面板的用户筛选都读那一栏（待办清单 P47 同族）。
func TestApiKeyIdentityFollowsUsernameChange(t *testing.T) {
	srv := newTestServer(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(upstream.Close)
	setAUpstream(t, upstream.URL)
	setSearchCost(t, 1)

	const oldName, newName = "ak_ren1", "ak_ren2"
	token := registerUser(t, srv, oldName, oldName+"@example.com", "pass1234")

	status, env := doJSON(t, srv, http.MethodPost, "/apikey", map[string]string{"name": "p47"}, authHeader(token))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("创建密钥失败: status=%d msg=%s", status, env.Msg)
	}
	plain, _ := env.dataMap(t)["key"].(string)

	callWithKey := func() {
		st, raw := doRaw(t, srv, http.MethodGet, "/fake_a/search"+buildQuery(map[string]string{"query": "测试"}),
			nil, map[string]string{"X-API-Key": plain})
		if st != http.StatusOK {
			t.Fatalf("密钥调网关 status = %d（body=%s）", st, truncate(string(raw), 200))
		}
	}
	// 最新一条明细里的用户名——这就是「这笔账记在谁名下」的读数。
	// 读**内存环形缓冲**而不是 `api_call_logs`：明细是攒满 50 条或关停时才批量落库的
	// （`base/metrics.go:53` 的 `flushBatchSize`），两条调用的量落不下去，查表只会读到空串。
	lastLogUsername := func() string {
		for _, rc := range base.RecentCalls(200) {
			if rc.Source == "fake_a" && rc.Action == "search" && rc.Status == http.StatusOK {
				return rc.Username
			}
		}
		return ""
	}

	// 1) 先把旧名养进归属缓存
	callWithKey()
	if got := lastLogUsername(); got != oldName {
		t.Fatalf("改名前的归属读数 = %q, want %q（这条用例的前提没成立）", got, oldName)
	}

	// 2) 本人改用户名（`PATCH /auth/me`：这里顺带吊销全部会话）
	status, env = doJSON(t, srv, http.MethodPatch, "/auth/me",
		map[string]interface{}{"username": newName, "email": oldName + "@example.com"}, authHeader(token))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("改用户名失败: status=%d msg=%s", status, env.Msg)
	}

	// 3) 再用同一把密钥打一次：归属必须已经跟到新名
	callWithKey()
	if got := lastLogUsername(); got != newName {
		t.Fatalf("改完名之后密钥调用的归属读数还是 %q——身份是从缓存里拿的旧名，"+
			"监控明细与按人统计都会记在不存在的人名下（P47 同族）", got)
	}
}
