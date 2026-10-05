package test

// 待办清单 P94：`verify_http_headers` 的形状错了会被**静默丢掉请求头**。
//
// 两半各钉一件事，缺一半都还是"静默"：
//   - 写入端（`json_object` 一档）：数组 / 裸字符串 / 值写成数字，**过去都能通过 `json.Compact` 存进库**，
//     面板上看着也对，只有真发一次码才暴露——而暴露出来的是平台的 401，不是我们的形状错误。
//   - 发送端：解不开就**中止发码**（原来没有失败分支，等于带着空请求头打给上游）。
//     这一半管的是**库里已有的旧形状值**——写入端的校验够不到存量。
//
// 反向自检也在一起：合法对象必须存得进去、也必须真的带上请求头，
// 否则"没多发一次"那几条断言全都是空转（判据页「断言空转」那一族）。

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"loomproxy/db"
	"loomproxy/models"
)

// TestVerifyHeadersBadShapeRejectedAtWrite 写入端按声明的 type 拒坏形状，且不覆盖库里的原值。
func TestVerifyHeadersBadShapeRejectedAtWrite(t *testing.T) {
	srv := newTestServer(t)
	admin := authHeader(adminToken(t, srv))

	const good = `{"authorization":"Bearer keep-me"}`
	setVerifySetting(t, "verify_http_headers", good)

	// 先确认这一键的档位真的变成了 json_object：type 由 seed 的声明对账（seedSettings 只同步 type），
	// 如果生产那行还是 json，下面三条坏值就一条都拒不掉——那不是用例有牙齿，是前提没落地。
	var row models.SystemSetting
	if err := db.DB.Where(map[string]interface{}{"key": "verify_http_headers"}).First(&row).Error; err != nil {
		t.Fatalf("读设置行失败: %v", err)
	}
	if row.Type != "json_object" {
		t.Fatalf("verify_http_headers 的 type = %q, want json_object（P94 的判据挂在档位上）", row.Type)
	}

	bad := []struct {
		name  string
		value string
	}{
		{"数组包对象", `[{"authorization":"Bearer x"}]`},
		{"裸字符串", `"Bearer x"`},
		{"值写成数字", `{"authorization":123}`},
		{"值写成嵌套对象", `{"authorization":{"v":"x"}}`},
	}
	for _, b := range bad {
		status, env := doJSON(t, srv, http.MethodPut, "/admin/settings/verify_http_headers",
			map[string]interface{}{"value": b.value}, admin)
		if status != http.StatusBadRequest {
			t.Errorf("%s: PUT 返回 %d（%s），want 400——这一档必须当场拒，而不是留到发码那天", b.name, status, env.Msg)
			continue
		}
		if !strings.Contains(env.Msg, "字符串到字符串") {
			t.Errorf("%s: 拒的话没说清要什么形状: msg=%q", b.name, env.Msg)
		}
		if got := dbGetSetting(t, "verify_http_headers"); got != good {
			t.Errorf("%s: 坏提交把库里的原值顶掉了（现在 = %q）", b.name, got)
		}
	}

	// 反向自检：合法对象存得进去，且落库的是压缩后的单行
	const newGood = `{"authorization":"Bearer new-token","content-type":"application/json"}`
	status, env := doJSON(t, srv, http.MethodPut, "/admin/settings/verify_http_headers",
		map[string]interface{}{"value": newGood}, admin)
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("合法对象被拒了: status=%d code=%d msg=%s", status, env.Code, env.Msg)
	}
	if got := dbGetSetting(t, "verify_http_headers"); strings.Contains(got, "\n") || !strings.Contains(got, "Bearer new-token") {
		t.Errorf("落库的值不对: %q", got)
	}
}

// TestVerifyHeadersBadShapeAbortsSend 发送端解不开形状就不发：上游一次都不能被打到。
func TestVerifyHeadersBadShapeAbortsSend(t *testing.T) {
	srv := newTestServer(t)
	enableVerifyScene(t, "register")
	// 冷却与日上限放宽：这条用例要连发四次，挡在门外的必须是形状判据而不是限流器
	setVerifySetting(t, "verify_send_interval_sec", "0")
	setVerifySetting(t, "verify_daily_send_limit", "1000")

	var hits int
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		gotAuth = r.Header.Get("authorization")
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer upstream.Close()

	setVerifySetting(t, "verify_provider", "http")
	setVerifySetting(t, "verify_http_method", "GET")
	setVerifySetting(t, "verify_http_url", upstream.URL+"/send?mobile={{target}}&code={{code}}")
	setVerifySetting(t, "verify_http_success_keyword", `"status":"ok"`)

	// 1) 合法形状：打出去一次，且请求头真的带上了（不带的话，后面"没多打"就说明不了任何事）
	setVerifySetting(t, "verify_http_headers", `{"authorization":"Bearer ok"}`)
	if status, env := sendCode(t, srv, "register", "shape-ok@t.cn"); status != http.StatusOK || env.Code != 0 {
		t.Fatalf("合法形状的发码失败: status=%d msg=%s", status, env.Msg)
	}
	if hits != 1 {
		t.Fatalf("上游收到 %d 次，want 1", hits)
	}
	if gotAuth != "Bearer ok" {
		t.Fatalf("请求头没带上: authorization=%q", gotAuth)
	}

	// 2) 三种坏形状：这一次**不能打到上游**，对外是一句固定话，真因只在服务端日志里
	var codesBefore int64
	if err := db.DB.Model(&models.VerificationCode{}).
		Where(map[string]interface{}{"scene": "register"}).Count(&codesBefore).Error; err != nil {
		t.Fatalf("数验证码行失败: %v", err)
	}
	oldOut := log.Writer()
	defer log.SetOutput(oldOut)
	for i, bad := range []string{`[{"authorization":"Bearer x"}]`, `"Bearer naked"`, `{"authorization":123}`} {
		setVerifySetting(t, "verify_http_headers", bad) // 绕过写入口，模拟库里躺着的旧形状值
		var buf bytes.Buffer
		log.SetOutput(&buf)
		status, env := sendCode(t, srv, "register", "shape-bad"+string(rune('a'+i))+"@t.cn")
		log.SetOutput(oldOut)

		if status != http.StatusBadRequest {
			t.Errorf("坏形状 %s: status=%d, want 400（发码必须中止）", bad, status)
		}
		if hits != 1 {
			t.Errorf("坏形状 %s: 上游被打到第 %d 次——空请求头的匿名请求还是发出去了", bad, hits)
		}
		if env.Msg != "验证码发送失败，请稍后再试" {
			t.Errorf("坏形状 %s: 对外那句话不是固定句: %q", bad, env.Msg)
		}
		for _, leak := range []string{"Bearer", "authorization", "shape-bad", "@"} {
			if strings.Contains(env.Msg, leak) {
				t.Errorf("坏形状 %s: 对外文案泄出内部细节 %q", bad, leak)
			}
		}
		if !strings.Contains(buf.String(), "verify_http_headers 不是字符串到字符串的 JSON 对象") {
			t.Errorf("坏形状 %s: 服务端没有那条归因日志（P67 的镜像那一半）——日志里是: %q", bad, buf.String())
		}
	}

	// 3) 中止的那几次不该留下验证码行：留行等于"没发出去却以为发出去了"
	var codesAfter int64
	if err := db.DB.Model(&models.VerificationCode{}).
		Where(map[string]interface{}{"scene": "register"}).Count(&codesAfter).Error; err != nil {
		t.Fatalf("数验证码行失败: %v", err)
	}
	if codesAfter != codesBefore {
		t.Errorf("发码中止仍落了行: before=%d after=%d", codesBefore, codesAfter)
	}
}
