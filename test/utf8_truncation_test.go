package test

// 待办清单 P83：按**字节**截断 UTF-8 文本，会在尾部留下半个字符。
// 两处落点都是"把上游响应体截一段进错误文案"——发码通道失败（`handlers/verify`）与
// 上游非 2xx 的响应摘要（`base.errBodySnippet`）。中文正文是上游响应的常态，所以这不是理论风险：
// 截断点只要不落在 rune 边界上（256 与 200 都不是 3 的倍数，中文每字 3 字节），尾巴必留半个字。
// 后果不是崩溃而是**读数骗人**：日志与面板里出现乱码（U+FFFD），排错的人要先猜那是不是上游真给了乱码。
//
// 用例的形状是"截一个刻意跨 rune 的中文串，然后问结果还是不是合法 UTF-8"，
// 并且自己数样本（截断确实发生了、CJK 确实在结果里），否则 ValidString 会空转。

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"loomproxy/db"
)

func TestUpstreamBodySnippetStaysValidUTF8(t *testing.T) {
	srv := newTestServer(t)
	admin := authHeader(adminToken(t, srv))

	// 100 个「测」= 300 字节；errBodySnippet 的预算是 256 字节 → 256 = 85*3 + 1，
	// 按字节截会留下 1 个孤立续字节
	body := strings.Repeat("测", 100)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(up.Close)
	setPlatformUpstream(t, "fake_b", up.URL)

	var buf bytes.Buffer
	oldOut := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(oldOut) })

	// 走一次真实请求，让摘要进服务端日志
	doRaw(t, srv, http.MethodGet, "/fake_b/detail?id=x", nil, admin)

	line := ""
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.Contains(l, "响应:") && strings.Contains(l, "测") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatalf("日志里没出现带响应摘要的行——样本不成立，下面的 ValidString 断言是空转\n日志：%s", buf.String())
	}
	if !utf8.ValidString(line) {
		t.Errorf("上游响应摘要里有非法 UTF-8（按字节截断留下的半个字符）\n  读数：%q", line)
	}
	if strings.ContainsRune(line, utf8.RuneError) {
		t.Errorf("摘要里出现了 U+FFFD 替代符——截断把 rune 切断了：\n  读数：%q", line)
	}
}

func TestVerifyChannelFailureTextStaysValidUTF8(t *testing.T) {
	srv := newTestServer(t)
	enableVerifyScene(t, "register")

	// 300 个 rune 的中文响应体；truncateStr 的预算是 200 → 按字节截时 200 不是 3 的倍数
	body := "发码失败：" + strings.Repeat("渠", 300)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(up.Close)

	setVerifySetting(t, "verify_provider", "http")
	setVerifySetting(t, "verify_http_url", up.URL)
	setVerifySetting(t, "verify_http_method", "POST")
	setVerifySetting(t, "verify_http_body", "")
	setVerifySetting(t, "verify_http_headers", "")
	setVerifySetting(t, "verify_http_success_keyword", "")
	db.InvalidateSettingCache("")

	var buf bytes.Buffer
	oldOut := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(oldOut) })

	sendCode(t, srv, "register", "utf8-probe@unit.test")

	line := ""
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.Contains(l, "验证码发送失败（通道=") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatalf("发码失败在服务端没出声——前提不成立（这条通路本来就该有日志）\n日志：%s", buf.String())
	}
	if !utf8.ValidString(line) {
		t.Errorf("发码失败的错误文案里有非法 UTF-8（truncateStr 按字节截断了中文响应体）\n  读数：%q", line)
	}
	if !strings.Contains(line, "渠") {
		t.Errorf("截断后连响应体都没了（样本不成立，上面的合法性断言就是空转）：%q", line)
	}
}
