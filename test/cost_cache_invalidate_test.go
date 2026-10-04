package test

// 接口消耗缓存的失效（待办清单 P39）。
//
// 这条要钉的不是「扣得对不对」，而是**运维直觉**：面板改完单价点保存，接口上应当立刻按新价扣。
// 改之前全仓没有任何一处删除 `quota:cost:*`，所以「改了没反应」最长持续一个 CACHE_TTL（默认 300 秒），
// 而它的下一步通常是再点一次保存、或者重启进程去「确保生效」——重启会连带清掉所有 IP 的限流锁。
//
// 三段断言各有各的作用，缺一段就挡不住一种错法：
//   ① 键存在性：计费确实走 `gate.CostCacheKey` 那个键（键拼法一旦在别处再写一份，失效就会打空而构建照样绿）；
//   ② 改价后立即不扣：这是这条修复的本体；
//   ③ 禁用后立即 403：同一行、同一个缓存，但走的是另一条判定分支。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"loomproxy/conf"
	"loomproxy/db"
	"loomproxy/gate"
	"loomproxy/models"
	"loomproxy/utils"
)

// quotaCostRowID 取 fake_a/search 那行消耗配置的主键
func quotaCostRowID(t *testing.T, source, action string) uint {
	t.Helper()
	var id uint
	if err := db.DB.Model(&models.QuotaCost{}).
		Where("group_code = ? AND interface = ?", source, action).
		Select("id").Scan(&id).Error; err != nil || id == 0 {
		t.Fatalf("找不到 %s/%s 的消耗配置行: id=%d err=%v", source, action, id, err)
	}
	return id
}

func searchUsageLogs(t *testing.T, uid uint) int64 {
	t.Helper()
	var n int64
	if err := db.DB.Model(&models.QuotaUsageLog{}).
		Where("user_id = ? AND group_code = ? AND interface = ?", uid, "fake_a", "search").
		Count(&n).Error; err != nil {
		t.Fatalf("查用量流水失败: %v", err)
	}
	return n
}

func TestCostCacheInvalidatedByAdminUpdate(t *testing.T) {
	srv := newTestServer(t)

	// 关掉同内容扣减冷却：两次搜索用的是同一个关键词，
	// 冷却开着的话第二次**本来就不扣**，那 ② 会因为无关的机制而通过（假绿）。
	// 位置必须在 newTestServer 之后：`conf.Config` 是装配进程时才建的全局指针，
	// 用例开头就去读它，单跑这一条会直接 nil panic——整包跑着过只是**依赖了上一个用例的余荫**。
	prev := conf.Config.BillingDedupeSec
	conf.Config.BillingDedupeSec = 0
	t.Cleanup(func() { conf.Config.BillingDedupeSec = prev })

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(upstream.Close)
	setAUpstream(t, upstream.URL)

	admin := adminToken(t, srv)
	token := registerUser(t, srv, "cc_user1", "cc_user1@example.com", "pass1234")
	uid := userIDByName(t, "cc_user1")
	setSearchCost(t, 1)
	costID := quotaCostRowID(t, "fake_a", "search")
	key := gate.CostCacheKey("fake_a", "search")

	// ① 第一次请求：按 1 点扣，并且计费真的走了那个键
	if status, raw := searchA(t, srv, token); status != http.StatusOK {
		t.Fatalf("第 1 次搜索 status = %d, want 200（body=%s）", status, truncate(string(raw), 200))
	}
	if got := searchUsageLogs(t, uid); got != 1 {
		t.Fatalf("单价 1 时用量流水 = %d, want 1", got)
	}
	if _, ok := utils.DefaultCache().Get(key); !ok {
		t.Fatalf("计费没有填充 %s——读侧的键拼法与 gate.CostCacheKey 分叉了，"+
			"写侧再怎么失效都打不到它（待办清单 P39）", key)
	}

	// ② 面板把单价改成 0：键要立刻消失，下一次请求不再扣
	status, env := doJSON(t, srv, http.MethodPut, fmt.Sprintf("/admin/quota-costs/%d", costID),
		map[string]interface{}{"cost": 0}, authHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("改单价失败: status=%d msg=%s", status, env.Msg)
	}
	if _, ok := utils.DefaultCache().Get(key); ok {
		t.Errorf("改完单价缓存键 %s 还在——下一次请求仍按旧价扣，等于没修（等 TTL 是最长 300 秒）", key)
	}
	if status, raw := searchA(t, srv, token); status != http.StatusOK {
		t.Fatalf("第 2 次搜索 status = %d, want 200（body=%s）", status, truncate(string(raw), 200))
	}
	if got := searchUsageLogs(t, uid); got != 1 {
		t.Errorf("单价改成 0 之后仍然扣了（流水 %d 条, want 1）——改价没立即生效，就是这条缺陷的原样", got)
	}

	// ③ 同一行的「禁用」也必须立刻停用，而不是等一个缓存周期
	status, env = doJSON(t, srv, http.MethodPut, fmt.Sprintf("/admin/quota-costs/%d", costID),
		map[string]interface{}{"status": 0}, authHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("禁用接口失败: status=%d msg=%s", status, env.Msg)
	}
	status, raw := searchA(t, srv, token)
	if status != http.StatusForbidden {
		t.Fatalf("禁用后请求 status = %d, want 403（body=%s）——禁用不是立刻停用", status, truncate(string(raw), 200))
	}
	if !strings.Contains(string(raw), "接口已被管理员禁用") {
		t.Errorf("禁用后的文案 = %s, want 含「接口已被管理员禁用」", truncate(string(raw), 200))
	}
}
