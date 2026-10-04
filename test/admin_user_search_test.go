package test

// 管理员用户列表的 keyword 搜索：LIKE 通配符必须转义，而且要带 ESCAPE 子句。
//
// 这条用例是 P43 实现过程中撞出来的：代码原本写成 `username LIKE '%al\_1%'`（转义了但没有 ESCAPE 子句），
// 而 **SQLite 的 LIKE 默认没有转义字符**（MySQL 靠字符串字面量里的反斜杠恰好生效），
// 于是搜一个含下划线的用户名返回 0 行——测试用户名撞上就全红，现网 MySQL 上却一直"看起来没问题"。
// `DB_TYPE` 的代码默认是 mysql、而 `.env.example` 默认 sqlite，所以这条只在交付形态上暴露。

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func listUsernames(t *testing.T, srv *httptest.Server, admin, keyword string) []string {
	t.Helper()
	status, env := doJSON(t, srv, http.MethodGet, "/admin/users"+buildQuery(map[string]string{"keyword": keyword}), nil, authHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("列表查询 keyword=%q 失败: status=%d msg=%s", keyword, status, env.Msg)
	}
	rows, _ := env.dataMap(t)["list"].([]interface{})
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		m, _ := r.(map[string]interface{})
		if n, _ := m["username"].(string); n != "" {
			out = append(out, n)
		}
	}
	return out
}

func TestAdminUserKeywordEscapesLikeWildcards(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)

	// 目标名带下划线；邻居名把下划线那几个位置换成别的字符——
	// 只有当 `_` 被当成单字符通配符时，搜目标名才会连邻居一起捞进来
	for _, name := range []string{"esc_like_1", "escXlikeX1"} {
		if status, env := doJSON(t, srv, http.MethodPost, "/admin/users",
			map[string]string{"username": name, "email": name + "@example.com", "password": "pass1234"},
			authHeader(admin)); status != http.StatusOK || env.Code != 0 {
			t.Fatalf("建用户 %q 失败: status=%d msg=%s", name, status, env.Msg)
		}
	}

	hits := listUsernames(t, srv, admin, "esc_like_1")
	if len(hits) != 1 || hits[0] != "esc_like_1" {
		t.Errorf("keyword=esc_like_1 命中 %v, want 只有 esc_like_1（下划线被当成单字符通配了？）", hits)
	}

	// `%` 更不能当通配：搜 esc_pct_9 时把 `%` 写进关键词，若未转义就等于搜「任意串」
	greedy := listUsernames(t, srv, admin, "%")
	if len(greedy) > 0 {
		t.Errorf("keyword=%% 命中了 %d 个用户, want 0（未转义时它匹配全表）", len(greedy))
	}
}
