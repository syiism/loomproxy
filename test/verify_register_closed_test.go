package test

// 关闭注册后，点「发送验证码」应**立刻**被告知「注册功能已关闭」——
// 否则用户填完表单、收码、回来注册，最后一步才弹关闭提示，体验割裂。
// 这条与 auth.Register 同源判据：两者读的都是 register_enabled，提示文案也一致。

import (
	"net/http"
	"testing"

	"loomproxy/testkit"
)

func TestVerifySendClosedWhenRegisterDisabled(t *testing.T) {
	srv := newTestServer(t)
	enableVerifyScene(t, "register")

	// 1) 关闭注册：发注册验证码应直接 403「注册功能已关闭」，不会走到发码/冷却
	testkit.WriteSettingValue(t, "register_enabled", "false")
	status, env := sendCode(t, srv, "register", "closed-probe@unit.test")
	if status != http.StatusForbidden {
		t.Fatalf("注册关闭时发注册码 status=%d, want 403（msg=%q）", status, env.Msg)
	}
	if env.Msg != "注册功能已关闭" {
		t.Errorf("msg=%q, want 注册功能已关闭（与 auth.Register 同源提示）", env.Msg)
	}

	// 2) 重新开启：同一路径应正常发码（默认 mock 发送器直接成功）
	testkit.WriteSettingValue(t, "register_enabled", "true")
	status, env = sendCode(t, srv, "register", "open-probe@unit.test")
	if status != http.StatusOK {
		t.Fatalf("注册开启时发注册码 status=%d, want 200（msg=%q）", status, env.Msg)
	}

	// 3) 关闭只针对 register 场景：forgot_password 不应回这句（该场景未启用时回「该场景未启用验证码」）
	testkit.WriteSettingValue(t, "register_enabled", "false")
	status, env = sendCode(t, srv, "forgot_password", "other@unit.test")
	if env.Msg == "注册功能已关闭" {
		t.Errorf("forgot_password 场景不应回「注册功能已关闭」：status=%d msg=%q", status, env.Msg)
	}
}
