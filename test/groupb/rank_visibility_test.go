package groupb

// 数据源列表里的「榜单可见」开关：它写回的仍是设置项 rank_public_sources 的逗号名单
// （面板只是把一长串名单拆成逐源按钮，不设第二份真相），放行范围立刻影响 /rank/boards；
// 删除数据源时要把自己的源码从名单里带走。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"loomproxy/base"
	"loomproxy/db"
	"loomproxy/models"
)

func rankPublicValue(t *testing.T) string {
	t.Helper()
	var s models.SystemSetting
	if err := db.DB.Where(map[string]interface{}{"key": "rank_public_sources"}).First(&s).Error; err != nil {
		t.Fatalf("读 rank_public_sources 失败: %v", err)
	}
	return s.Value
}

func keywordTotal(t *testing.T, srv *httptest.Server, token, name string) (int64, bool) {
	t.Helper()
	status, env := doJSON(t, srv, "GET", "/rank/boards?days=7", nil, authHeader(token))
	if status != http.StatusOK {
		t.Fatalf("/rank/boards status=%d msg=%s", status, env.Msg)
	}
	for _, raw := range env.dataMap(t)["boards"].([]interface{}) {
		b, _ := raw.(map[string]interface{})
		if b["dim"] != "keyword" {
			continue
		}
		for _, r := range b["rows"].([]interface{}) {
			it, _ := r.(map[string]interface{})
			if it["name"] == name {
				return int64(it["total"].(float64)), true
			}
		}
	}
	return 0, false
}

func TestRankPublicToggleFromDataSourceAdmin(t *testing.T) {
	srv := newTestServer(t)
	base.ResetMetrics()
	admin := adminToken(t, srv)
	user := registerUser(t, srv, "rank_toggle", "rank_toggle@example.com", "pass1234")

	const word = "开关测试词"
	// 一次调用一行（insertSubjectCall 的数值参数是 result_count，不是次数）
	insertSubjectCall(t, fakeA, "search", word, "", base.MediaNovel, 3, http.StatusOK)
	insertSubjectCall(t, fakeB, "search", word, "", base.MediaAudio, 5, http.StatusOK)

	// 名单为空：普通用户读不到榜
	if status, env := doJSON(t, srv, "GET", "/rank/boards", nil, authHeader(user)); status != http.StatusForbidden {
		t.Fatalf("名单为空时应 403，实为 %d（msg=%s）", status, env.Msg)
	}

	// 管理列表要把可见性带出来（面板据此渲染开关初值），默认全 false
	_, env := doJSON(t, srv, "GET", "/admin/data-sources", nil, authHeader(admin))
	var rows []struct {
		ID         int    `json:"id"`
		Name       string `json:"name"`
		RankPublic bool   `json:"rank_public"`
	}
	if err := json.Unmarshal(env.Data, &rows); err != nil {
		t.Fatalf("解析数据源列表失败: %v", err)
	}
	ids := map[string]int{}
	for _, r := range rows {
		ids[r.Name] = r.ID
		if r.RankPublic {
			t.Errorf("%s 默认 rank_public 应为 false", r.Name)
		}
	}
	if ids[fakeA] == 0 || ids[fakeB] == 0 {
		t.Fatalf("列表未带出假源: %+v", ids)
	}

	toggle := func(id int, enable bool) {
		t.Helper()
		// 只给 rank_public：不能被「无更新字段」拦掉
		if status, env := doJSON(t, srv, "PATCH", fmt.Sprintf("/admin/data-sources/%d", id),
			map[string]interface{}{"rank_public": enable}, authHeader(admin)); status != http.StatusOK || env.Code != 0 {
			t.Fatalf("切换 rank_public=%v 失败: status=%d msg=%s", enable, status, env.Msg)
		}
	}

	toggle(ids[fakeA], true)
	if got := rankPublicValue(t); got != fakeA {
		t.Errorf("放行后名单 = %q, want %q", got, fakeA)
	}
	if total, ok := keywordTotal(t, srv, user, word); !ok || total != 1 {
		t.Errorf("只放行 %s 时合并值应为 1（%s 的那次不计），实得 %d ok=%v", fakeA, fakeB, total, ok)
	}

	toggle(ids[fakeB], true)
	if got := rankPublicValue(t); got != fakeA+","+fakeB {
		t.Errorf("两次放行后的名单 = %q, want %q", got, fakeA+","+fakeB)
	}
	if total, _ := keywordTotal(t, srv, user, word); total != 2 {
		t.Errorf("两个源都放行后应为 2 次，实得 %d", total)
	}

	toggle(ids[fakeA], false)
	if got := rankPublicValue(t); got != fakeB {
		t.Errorf("关闭 %s 后名单 = %q, want %q", fakeA, got, fakeB)
	}
	if status, _ := doJSON(t, srv, "GET", "/rank/boards?source="+fakeA, nil, authHeader(user)); status != http.StatusForbidden {
		t.Errorf("关闭后单独筛 %s 应 403，实为 %d", fakeA, status)
	}

	// 删除数据源要把源码从名单里摘掉（留着不会放行什么，但名单会越积越脏）
	_, created := doJSON(t, srv, "POST", "/admin/data-sources",
		map[string]interface{}{"name": "rank_tmp", "display_name": "临时源", "category": "ranktmp"}, authHeader(admin))
	var tmp struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(created.Data, &tmp); err != nil || tmp.ID == 0 {
		t.Fatalf("建临时源失败: %v（data=%s）", err, created.Data)
	}
	toggle(tmp.ID, true)
	if got := rankPublicValue(t); got != fakeB+","+tmp.Name {
		t.Fatalf("放行临时源后名单 = %q, want %q", got, fakeB+","+tmp.Name)
	}
	if status, env := doJSON(t, srv, "DELETE", fmt.Sprintf("/admin/data-sources/%d", tmp.ID), nil, authHeader(admin)); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("删除临时源失败: status=%d msg=%s", status, env.Msg)
	}
	if got := rankPublicValue(t); got != fakeB {
		t.Errorf("删除源后名单 = %q, want 只剩 %q", got, fakeB)
	}
}
