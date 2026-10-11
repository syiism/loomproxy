package test

// 注册入口的邮箱域名白/黑名单（系统设置 `email_domain_allowlist` / `email_domain_blocklist`）。
//
// 语义：后缀匹配、大小写不敏感（填 example.com 命中 example.com 与 mail.example.com）；
// 白名单非空 → 只放行名单内，随后一律排除黑名单内；两者皆空 = 不限制（默认）。

import (
	"net/http"
	"strings"
	"testing"

	"loomproxy/testkit"
)

func TestRegisterEmailDomainPolicy(t *testing.T) {
	srv := newTestServer(t)

	// ① 黑名单：example.com 及其子域、任意大小写都被拒
	testkit.WriteSettingValue(t, "email_domain_blocklist", "example.com")

	for _, email := range []string{"blocked1@example.com", "blocked2@mail.example.com", "blocked3@EXAMPLE.com"} {
		status, env := doJSON(t, srv, http.MethodPost, "/auth/register", map[string]interface{}{
			"username": "blk" + strings.SplitN(email, "@", 2)[0],
			"email":    email,
			"password": "pass1234",
		}, nil)
		if status != http.StatusForbidden {
			t.Fatalf("黑名单域名 %s 注册 status = %d, want 403（msg=%s）", email, status, env.Msg)
		}
		if !strings.Contains(env.Msg, "域名") {
			t.Errorf("拒绝提示 = %q，期望含「域名」", env.Msg)
		}
	}

	// 不在名单内的域名照常注册
	if tok := registerUser(t, srv, "okuser", "ok@other.com", "pass1234"); tok == "" {
		t.Fatal("未被名单覆盖的域名应能注册")
	}

	// ② 白名单：非空时只放行名单内（黑名单清空后仍生效）
	testkit.WriteSettingValue(t, "email_domain_blocklist", "")
	testkit.WriteSettingValue(t, "email_domain_allowlist", "trusted.com")

	status, env := doJSON(t, srv, http.MethodPost, "/auth/register", map[string]interface{}{
		"username": "outsider", "email": "outsider@other.com", "password": "pass1234",
	}, nil)
	if status != http.StatusForbidden {
		t.Fatalf("白名单外注册 status = %d, want 403（msg=%s）", status, env.Msg)
	}
	if !strings.Contains(env.Msg, "名单") {
		t.Errorf("白名单拒绝提示 = %q，期望含「名单」", env.Msg)
	}

	if tok := registerUser(t, srv, "insider", "insider@trusted.com", "pass1234"); tok == "" {
		t.Fatal("白名单内域名应能注册")
	}

	// ③ 两个名单皆空 = 不限制
	testkit.WriteSettingValue(t, "email_domain_allowlist", "")
	if tok := registerUser(t, srv, "freeone", "free1@anywhere.net", "pass1234"); tok == "" {
		t.Fatal("名单皆空时应能注册")
	}
}
