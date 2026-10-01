package test

// 系统设置的 JSON 型 key：保存期校验 + 压缩落库。
// 起因是真实事故——verify_http_body 被多行粘贴存成「字符串里带裸换行」的伪 JSON，
// 运行时原样发出、发码平台解析失败却返回 HTTP 200，于是面板显示「已发送」而邮件从未出去。
// 坏值在写入时拒绝，比在运行时发现便宜得多。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"loomproxy/db"
	"loomproxy/models"
)

func settingValue(t *testing.T, key string) (value, typ string) {
	t.Helper()
	var s models.SystemSetting
	if err := db.DB.Where(map[string]interface{}{"key": key}).First(&s).Error; err != nil {
		t.Fatalf("读取设置项 %s 失败: %v", key, err)
	}
	return s.Value, s.Type
}

func TestSettingsJSONValidateAndCompact(t *testing.T) {
	srv := newTestServer(t)
	tok := adminToken(t, srv)

	if _, typ := settingValue(t, "verify_http_body"); typ != "json" {
		t.Fatalf("seed 后 verify_http_body 的 type 应为 json，实际 %q", typ)
	}

	// 合法的多行 JSON：应接受，并压成单行落库
	pretty := "{\n  \"sendEmail\": \"loomproxy@example.com\",\n  \"subject\": \"LoomProxy 验证码\",\n  \"content\": \"<div>你的验证码是 <b>{{code}}</b></div>\"\n}"
	status, env := doJSON(t, srv, http.MethodPut, "/admin/settings/verify_http_body",
		map[string]string{"value": pretty}, authHeader(tok))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("合法多行 JSON 应保存成功: status=%d msg=%s", status, env.Msg)
	}
	stored, _ := settingValue(t, "verify_http_body")
	if strings.Contains(stored, "\n") {
		t.Fatalf("JSON 型应压缩为单行，实际存了多行: %q", stored)
	}
	if !json.Valid([]byte(stored)) {
		t.Fatalf("压缩后的值不是合法 JSON: %q", stored)
	}
	// 压缩只吃 token 之间的空白：中文与占位符必须原样保留（不能变成 \uXXXX 或丢转义）
	for _, want := range []string{"LoomProxy 验证码", "{{code}}", "loomproxy@example.com"} {
		if !strings.Contains(stored, want) {
			t.Fatalf("压缩后丢失原文 %q，实际: %s", want, stored)
		}
	}
	if got := env.dataMap(t)["value"]; got != stored {
		t.Fatalf("响应回填的 value 应与库中一致: 响应=%v 库=%q", got, stored)
	}

	// 字符串里带裸换行：正是生产上那个坏值，必须在写入时拒绝
	bad := "{\"subject\": \"LoomProxy \n验证码\"}"
	status, env = doJSON(t, srv, http.MethodPut, "/admin/settings/verify_http_body",
		map[string]string{"value": bad}, authHeader(tok))
	if status != http.StatusBadRequest {
		t.Fatalf("非法 JSON 应 400，实际 status=%d msg=%s", status, env.Msg)
	}
	if !strings.Contains(env.Msg, "不是合法 JSON") {
		t.Fatalf("错误文案应说明是 JSON 校验失败，实际: %s", env.Msg)
	}
	if after, _ := settingValue(t, "verify_http_body"); after != stored {
		t.Fatalf("校验失败不应改动库中现值: 前=%q 后=%q", stored, after)
	}

	// 空值合法（能力未启用）
	status, env = doJSON(t, srv, http.MethodPut, "/admin/settings/verify_http_body",
		map[string]string{"value": ""}, authHeader(tok))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("空值应被接受: status=%d msg=%s", status, env.Msg)
	}
	if v, _ := settingValue(t, "verify_http_body"); v != "" {
		t.Fatalf("空值应原样落库，实际 %q", v)
	}

	// 不存在的 key 仍是 404
	if status, _ = doJSON(t, srv, http.MethodPut, "/admin/settings/no_such_setting_key",
		map[string]string{"value": "{}"}, authHeader(tok)); status != http.StatusNotFound {
		t.Fatalf("不存在的设置项应 404，实际 status=%d", status)
	}
}

// TestSettingsTypeReconcileOnSeed 老库升级路径：这些 key 在旧版本里是 string，
// 面板不提供改类型的入口，所以 type 由声明对账——不对账就会错过保存期的 JSON 校验。
func TestSettingsTypeReconcileOnSeed(t *testing.T) {
	newTestServer(t)

	if err := db.DB.Model(&models.SystemSetting{}).
		Where(map[string]interface{}{"key": "verify_http_body"}).Update("type", "string").Error; err != nil {
		t.Fatalf("模拟老库 type=string 失败: %v", err)
	}
	if err := db.Seed(db.DB); err != nil {
		t.Fatalf("Seed 失败: %v", err)
	}
	if _, typ := settingValue(t, "verify_http_body"); typ != "json" {
		t.Fatalf("Seed 应把 type 对账为 json，实际 %q", typ)
	}
}
