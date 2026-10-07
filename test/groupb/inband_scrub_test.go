package groupb

// 带内错误正文的文案脱敏（待办清单 P23①）。
//
// §10 的约定是「对下游的错误文案必须脱敏」，但它原来只挂在 `handleError` 上；
// 源返回 `ContentType=="error"` 的正文时 handler 没有报错，那条出口绕过了收口，
// 而传输层失败的 message 常是 `*url.Error`——内嵌完整请求 URL（签名参数与上游域名）。
// 这里钉四件事：两种形状都收口、不含 URL 的本地文案照旧可见（排障要看得到）、
// 以及**正文不许被扫**（合法正文里本来就可能有 URL）。

import (
	"net/http"
	"strings"
	"testing"

	"loomproxy/testkit/fakesource"
)

const leakMarker = "TOPSECRET"

// TestInBandDTOMessageScrubbed 规范 DTO 形状（真实源的写法：legado.ContentResponse）
func TestInBandDTOMessageScrubbed(t *testing.T) {
	srv := newTestServer(t)
	up := staticUpstream(t, `{"contentType":"text","data":{"content":"正文"}}`)
	setPlatformUpstream(t, "fake_a", up.URL)

	fakesource.InBandDTO = true
	t.Cleanup(func() { fakesource.InBandDTO = false })

	status, raw := doRaw(t, srv, http.MethodGet, "/fake_a/content?bookId=b1&itemId=i1", nil, authHeader(adminToken(t, srv)))
	body := string(raw)
	if status != http.StatusOK {
		t.Fatalf("带内错误仍应是 200（P22② 改口径是另一回事），实得 %d", status)
	}
	if !strings.Contains(body, "上游请求失败，请稍后重试") {
		t.Fatalf("DTO 形状的 message 没收口，响应=%s", truncate(body, 260))
	}
	assertNoLeak(t, body)
}

// TestInBandLooseMapMessageScrubbed 松散 map 形状（源自己拼的信封）
func TestInBandLooseMapMessageScrubbed(t *testing.T) {
	srv := newTestServer(t)
	up := staticUpstream(t, `{"contentType":"error","data":{"message":"Get \"https://upstream.invalid/play?app_key=`+leakMarker+`&sig=deadbeef\": context deadline exceeded"}}`)
	setPlatformUpstream(t, "fake_b", up.URL)

	status, raw := doRaw(t, srv, http.MethodGet, "/fake_b/content?bookId=b1&itemId=i1", nil, authHeader(adminToken(t, srv)))
	body := string(raw)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200（body=%s）", status, truncate(body, 200))
	}
	if !strings.Contains(body, "上游请求失败，请稍后重试") {
		t.Fatalf("松散形状的 message 没收口，响应=%s", truncate(body, 260))
	}
	assertNoLeak(t, body)
}

// TestInBandLocalMessageKept 不含 URL 的本地文案必须照原样下发：脱敏不是把所有错误话术抹平，
// 「章节不存在」这类信息下游和用户都需要看到。
func TestInBandLocalMessageKept(t *testing.T) {
	srv := newTestServer(t)
	up := staticUpstream(t, `{"contentType":"error","data":{"message":"章节不存在"}}`)
	setPlatformUpstream(t, "fake_b", up.URL)

	_, raw := doRaw(t, srv, http.MethodGet, "/fake_b/content?bookId=b1&itemId=i1", nil, authHeader(adminToken(t, srv)))
	if !strings.Contains(string(raw), "章节不存在") {
		t.Fatalf("本地文案被误伤：响应=%s", truncate(string(raw), 260))
	}
}

// TestNormalContentURLUntouched 正常正文里的 URL 一个字都不许改——
// 收口只针对「错误形态的 message」，把正文扫成通用提示等于毁掉内容。
func TestNormalContentURLUntouched(t *testing.T) {
	srv := newTestServer(t)
	up := staticUpstream(t, `{"contentType":"text","data":{"content":"参见 https://reader.example/book/1 的说明"}}`)
	setPlatformUpstream(t, "fake_b", up.URL)

	_, raw := doRaw(t, srv, http.MethodGet, "/fake_b/content?bookId=b1&itemId=i1", nil, authHeader(adminToken(t, srv)))
	body := string(raw)
	if !strings.Contains(body, "https://reader.example/book/1") {
		t.Fatalf("正文被脱敏扫掉了，响应=%s", truncate(body, 260))
	}
}

func assertNoLeak(t *testing.T, body string) {
	t.Helper()
	for _, bad := range []string{leakMarker, "app_key", "upstream.invalid", "203.0.113.9", "sig="} {
		if strings.Contains(body, bad) {
			t.Errorf("响应里泄露了 %q（签名参数/上游域名/上游 IP 都不该出服务端）：%s", bad, truncate(body, 260))
		}
	}
}
