package groupb

// 待办清单 P86 的端到端凭据：越界页码不能"装着第一页的数据、回显一个不存在的页码"。
//
// 修前的六处内联写法是 `page, _ := strconv.Atoi(...)`——错误被丢掉，而 `strconv.Atoi`
// 对超出 int64 的输入返回 ErrRange 的同时**仍然把值钳成 MaxInt64**；于是
// `(page-1)*pageSize` 溢出成负数（实测 -40），GORM 对负 offset 是**整段不写 OFFSET**
// （DryRun 只剩 `… LIMIT 20`），响应里却回显 `"page": 9223372036854775807`。
// 现在六处都走 `utils.Paginate`，页码带上下限，回显与数据必须一致。

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"loomproxy/base"
)

func historyIDs(t *testing.T, srv *httptest.Server, admin, query string) ([]float64, float64) {
	t.Helper()
	status, env := doJSON(t, srv, http.MethodGet, "/admin/monitor/history"+query, nil, authHeader(admin))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("GET /admin/monitor/history%s status=%d code=%d msg=%s", query, status, env.Code, env.Msg)
	}
	data, _ := env.dataMap(t)["list"].([]interface{})
	ids := make([]float64, 0, len(data))
	for _, r := range data {
		m, _ := r.(map[string]interface{})
		id, _ := m["id"].(float64)
		ids = append(ids, id)
	}
	page, _ := env.dataMap(t)["page"].(float64)
	return ids, page
}

func TestOutOfRangePageEchoesThePageItReturned(t *testing.T) {
	srv := newTestServer(t)
	base.ResetMetrics()
	admin := adminToken(t, srv)

	for i := 0; i < 3; i++ {
		insertIdentRow(t, "fake_a", "bk-p86", "P86 测试书", "ch-p86", "P86 测试章")
	}

	first, firstPage := historyIDs(t, srv, admin, "?page=1&page_size=20")
	if len(first) == 0 {
		t.Fatal("第一页一行都没有——样本不成立，下面的比对是空转")
	}
	if firstPage != 1 {
		t.Fatalf("第一页的回显就不是 1（%v）——前提不成立", firstPage)
	}

	for _, hostile := range []string{"999999999999999999999", "9223372036854775807", "9223372036854775808"} {
		got, page := historyIDs(t, srv, admin, "?page="+hostile+"&page_size=20")
		if page != 1 {
			t.Errorf("?page=%s 的回显是 page=%v，want 1——回显必须说出它真给了哪一页", hostile, page)
		}
		if len(got) != len(first) {
			t.Errorf("?page=%s 给了 %d 行，第一页是 %d 行——两者必须同为一页", hostile, len(got), len(first))
			continue
		}
		for i := range got {
			if got[i] != first[i] {
				t.Errorf("?page=%s 第 %d 行是 %v，第一页是 %v——越界页码不该返回别的内容", hostile, i, got[i], first[i])
				break
			}
		}
	}
}
