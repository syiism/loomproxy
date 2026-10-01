package test

// 公开排行榜的权限边界：
//   - 匿名不可读（JWT 会话保护）
//   - 是否放行普通用户由管理员用系统设置 rank_public_enabled 决定，默认关闭
//   - 放行时也只回「名称 + 次数」，管理端才有的调用者/时间/延迟/数据源维度一律不出现在这里
//   - 与 /admin/monitor/subjects 错开：普通用户打管理端点仍是 403

import (
	"net/http"
	"testing"

	"loomproxy/base"
)

func TestRankBoardsAccessControl(t *testing.T) {
	srv := newTestServer(t)
	base.ResetMetrics()
	admin := adminToken(t, srv)

	insertSubjectCall(t, "fake_a", "search", "公开榜测试词", "", base.MediaNovel, 3, http.StatusOK)
	insertSubjectCall(t, "fake_a", "content", "", "公开榜测试书", base.MediaNovel, 1, http.StatusOK)
	// 详情与目录的书名不该进阅读榜（口径：只统计 content）
	insertSubjectCall(t, "fake_a", "detail", "", "只点开没读的书", base.MediaNovel, 1, http.StatusOK)

	user := registerUser(t, srv, "rank_u1", "rank_u1@example.com", "pass1234")

	// 1) 匿名被拒
	if status, _ := doJSON(t, srv, "GET", "/rank/boards", nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("匿名访问公开榜应 401，实为 %d", status)
	}

	// 2) 默认关闭：普通用户 403，管理员仍可读
	status, env := doJSON(t, srv, "GET", "/rank/boards", nil, authHeader(user))
	if status != http.StatusForbidden {
		t.Fatalf("开关关闭时普通用户应 403，实为 %d（msg=%s）", status, env.Msg)
	}
	if status, env = doJSON(t, srv, "GET", "/rank/boards", nil, authHeader(admin)); status != http.StatusOK {
		t.Fatalf("管理员在开关关闭时也应可读，实为 %d（msg=%s）", status, env.Msg)
	}

	// 3) 管理员开启后，普通用户可读，且字段被削到只剩名称与次数
	if status, env = doJSON(t, srv, "PUT", "/admin/settings/rank_public_enabled",
		map[string]string{"value": "true"}, authHeader(admin)); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("开启开关失败: status=%d msg=%s", status, env.Msg)
	}
	status, env = doJSON(t, srv, "GET", "/rank/boards?days=7", nil, authHeader(user))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("开启后普通用户应 200，实为 %d（msg=%s）", status, env.Msg)
	}
	data := env.dataMap(t)
	if data["days"].(float64) != 7 {
		t.Errorf("响应应回显窗口 days=7，实得 %v", data["days"])
	}
	list, _ := data["boards"].([]interface{})
	if len(list) != 2 {
		t.Fatalf("应固定两张榜，实得 %d", len(list))
	}
	byDim := map[string]map[string]interface{}{}
	for _, raw := range list {
		b, _ := raw.(map[string]interface{})
		byDim[b["dim"].(string)] = b
	}
	for _, dim := range []string{"keyword", "book"} {
		b, ok := byDim[dim]
		if !ok {
			t.Fatalf("缺少 %s 榜: %+v", dim, byDim)
		}
		rows, _ := b["rows"].([]interface{})
		if len(rows) == 0 {
			t.Fatalf("%s 榜为空", dim)
		}
		for _, r := range rows {
			it, _ := r.(map[string]interface{})
			for _, forbidden := range []string{"sources", "last_called_at", "avg_latency_ms", "success_rate", "empty_results", "username", "ip"} {
				if _, leaked := it[forbidden]; leaked {
					t.Errorf("%s 榜泄露了管理端字段 %q: %+v", dim, forbidden, it)
				}
			}
		}
	}
	names := map[string]bool{}
	for _, r := range byDim["keyword"]["rows"].([]interface{}) {
		it, _ := r.(map[string]interface{})
		names[it["name"].(string)] = true
	}
	if !names["公开榜测试词"] {
		t.Errorf("搜索词榜缺少条目，实得 %+v", names)
	}
	bookNames := map[string]int64{}
	for _, r := range byDim["book"]["rows"].([]interface{}) {
		it, _ := r.(map[string]interface{})
		bookNames[it["name"].(string)] = int64(it["total"].(float64))
	}
	if bookNames["公开榜测试书"] != 1 {
		t.Errorf("阅读榜应计入正文 1 次，实得 %+v", bookNames)
	}
	if _, ok := bookNames["只点开没读的书"]; ok {
		t.Errorf("详情调用不该进阅读榜，实得 %+v", bookNames)
	}

	// 4) 越界窗口回落 7（避免用天粒度反推到个人）
	if _, env = doJSON(t, srv, "GET", "/rank/boards?days=365", nil, authHeader(user)); env.dataMap(t)["days"].(float64) != 7 {
		t.Errorf("非法窗口应回落 7，实得 %v", env.dataMap(t)["days"])
	}

	// 5) 与内容维度端点错开：普通用户打 /admin/monitor/subjects 依旧 403
	if status, _ = doJSON(t, srv, "GET", "/admin/monitor/subjects?dim=keyword", nil, authHeader(user)); status != http.StatusForbidden {
		t.Errorf("普通用户访问管理端维度榜应 403，实为 %d", status)
	}
}

// TestRankBoardsSourceFilter 公开榜的按数据源筛选：同一个搜索词在两个源里各有一次调用时，
// 「全部数据源」合并计数、选中某个源时只回该源的计数——否则用户看不出该用哪个源检索。
func TestRankBoardsSourceFilter(t *testing.T) {
	srv := newTestServer(t)
	base.ResetMetrics()
	admin := adminToken(t, srv)

	if status, env := doJSON(t, srv, "PUT", "/admin/settings/rank_public_enabled",
		map[string]string{"value": "true"}, authHeader(admin)); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("开启公开榜失败: status=%d msg=%s", status, env.Msg)
	}

	// 一次调用一行：fake_a 两次、fake_b 五次（insertSubjectCall 的数值参数是 result_count，不是次数）
	for i := 0; i < 2; i++ {
		insertSubjectCall(t, "fake_a", "search", "同一关键词", "", base.MediaNovel, 3, http.StatusOK)
	}
	for i := 0; i < 5; i++ {
		insertSubjectCall(t, "fake_b", "search", "同一关键词", "", base.MediaAudio, 7, http.StatusOK)
	}

	rowsOf := func(data map[string]interface{}, dim string) map[string]int64 {
		out := map[string]int64{}
		for _, raw := range data["boards"].([]interface{}) {
			b, _ := raw.(map[string]interface{})
			if b["dim"] != dim {
				continue
			}
			for _, r := range b["rows"].([]interface{}) {
				it, _ := r.(map[string]interface{})
				out[it["name"].(string)] = int64(it["total"].(float64))
			}
		}
		return out
	}

	// 候选源列表随响应下发（取启用中的数据源）
	_, all := doJSON(t, srv, "GET", "/rank/boards?days=7", nil, authHeader(admin))
	d := all.dataMap(t)
	list, _ := d["sources"].([]interface{})
	if len(list) < 2 {
		t.Fatalf("sources 候选应含测试假源，实得 %+v", list)
	}
	found := map[string]bool{}
	for _, raw := range list {
		o, _ := raw.(map[string]interface{})
		found[o["code"].(string)] = true
	}
	if !found["fake_a"] || !found["fake_b"] {
		t.Errorf("sources 缺少假源候选: %+v", found)
	}
	if got := rowsOf(d, "keyword")["同一关键词"]; got != 7 {
		t.Errorf("全部数据源应合并为 7，实得 %d", got)
	}

	// 选中单源后只回该源的计数
	_, one := doJSON(t, srv, "GET", "/rank/boards?days=7&source=fake_b", nil, authHeader(admin))
	if got := rowsOf(one.dataMap(t), "keyword")["同一关键词"]; got != 5 {
		t.Errorf("筛 fake_b 应得 5，实得 %d", got)
	}
	if echoed := one.dataMap(t)["source"]; echoed != "fake_b" {
		t.Errorf("响应应回显 source，实得 %v", echoed)
	}

	// 未知/未启用的源直接 400，避免返回一张空榜让人以为是没数据
	if status, _ := doJSON(t, srv, "GET", "/rank/boards?source=nope", nil, authHeader(admin)); status != http.StatusBadRequest {
		t.Errorf("非法 source 应 400，实为 %d", status)
	}
}
