package test

// 敏感设置键只写不回显（待办清单 P62）：verify_http_headers 里躺着对接发码通道的真实令牌，
// GET /admin/settings 原样返回等于把它交进面板 DOM 与所有管理员会话的读响应。
// 三段：读响应不带值且名单随下发；写侧留空 = 保持原值（防「看一眼再保存」把空串写回去）；
// 写入新值正常生效。面板按 sensitive_keys 渲染密钥控件（前端口径与后端同源）。

import (
	"loomproxy/testkit"
	"net/http"
	"strings"
	"testing"
)

func dbGetSetting(t *testing.T, key string) string {
	t.Helper()
	return testkit.DBGetSetting(t, key)
}

func TestSensitiveSettingMaskedAndWriteOnly(t *testing.T) {
	srv := newTestServer(t)
	admin := authHeader(adminToken(t, srv))

	// 铺一个"真实令牌"形状的值
	setVerifySetting(t, "verify_http_headers", `{"authorization":"Bearer REAL-TOKEN"}`)

	status, env := doJSON(t, srv, "GET", "/admin/settings", nil, admin)
	if status != http.StatusOK {
		t.Fatalf("GET /admin/settings = %d", status)
	}
	data := env.dataMap(t)
	items, _ := data["items"].([]interface{})
	if items == nil {
		t.Fatal("响应缺 items（P62 起形状为 {items, sensitive_keys}）")
	}
	var maskedValue string
	for _, it := range items {
		m, _ := it.(map[string]interface{})
		if m["key"] == "verify_http_headers" {
			maskedValue, _ = m["value"].(string)
		}
	}
	if maskedValue != "" {
		t.Errorf("敏感键的值被回显了：%q（真令牌不得出读接口）", maskedValue)
	}
	sk, _ := data["sensitive_keys"].([]interface{})
	found := false
	for _, k := range sk {
		if k == "verify_http_headers" {
			found = true
		}
	}
	if !found {
		t.Errorf("sensitive_keys 缺 verify_http_headers：%v", sk)
	}

	// 留空保存 = 保持原值（后端同口径兜底，防面板把掩码空串写回去）
	st, env2 := doJSON(t, srv, "PUT", "/admin/settings/verify_http_headers", map[string]string{"value": ""}, admin)
	if st != http.StatusOK || env2.Code != 0 {
		t.Fatalf("敏感键留空保存应成功 no-op：status=%d msg=%s", st, env2.Msg)
	}
	if v := dbGetSetting(t, "verify_http_headers"); !strings.Contains(v, "REAL-TOKEN") {
		t.Fatalf("留空保存把原值改掉了：现在 = %q", v)
	}

	// 写入新值正常生效（换令牌是管理员的常规动作）
	st, _ = doJSON(t, srv, "PUT", "/admin/settings/verify_http_headers",
		map[string]string{"value": `{"authorization":"Bearer NEW-TOKEN"}`}, admin)
	if st != http.StatusOK {
		t.Fatalf("写入新值 = %d", st)
	}
	if v := dbGetSetting(t, "verify_http_headers"); !strings.Contains(v, "NEW-TOKEN") {
		t.Errorf("新值未落库：%q", v)
	}
	// 再读一次：新值同样不回显
	_, env3 := doJSON(t, srv, "GET", "/admin/settings", nil, admin)
	if strings.Contains(string(env3.Data), "NEW-TOKEN") {
		t.Error("更新后的令牌仍然从读接口泄漏")
	}
}
