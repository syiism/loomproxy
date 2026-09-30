package verify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"loomproxy/db"
)

// Sender 发码通道抽象。后期接入发码平台的扩展点：
//   - 通用 HTTP 平台：verify_provider=http + verify_http_* 模板配置，零代码；
//   - 平台专属 SDK/协议：在 senderFor() 加一个 case 实现 Sender 即可。
type Sender interface {
	Name() string
	// Send 发送验证码到目标（邮箱/手机号）。实现应自行处理超时与错误语义（返回 error 即发送失败）。
	Send(scene, target, code string) error
}

// getSender 按系统设置 verify_provider 返回发码通道（默认 mock）。
// db.GetSetting 有 10s 缓存，切换 provider 最长 10s 生效。
func getSender() Sender {
	switch db.GetSetting("verify_provider") {
	case "http":
		return httpSender{}
	default:
		return mockSender{}
	}
}

// mockSender 开发/测试通道：只把验证码打进服务端日志，不产生任何外部请求。
// mockLastCode 供集成测试断言（生产环境无副作用——mock 本就不发外部请求）。
type mockSender struct{}

var (
	mockMu       sync.Mutex
	mockLastCode = map[string]string{} // scene|target -> code
)

// MockLastCode 取 mock 通道最近发出的验证码（测试钩子）
func MockLastCode(scene, target string) string {
	mockMu.Lock()
	defer mockMu.Unlock()
	return mockLastCode[scene+"|"+target]
}

func (mockSender) Name() string { return "mock" }

func (mockSender) Send(scene, target, code string) error {
	mockMu.Lock()
	mockLastCode[scene+"|"+target] = code
	mockMu.Unlock()
	log.Printf("VERIFY [mock] scene=%s target=%s code=%s（mock 通道仅打印日志，不实际发送）", scene, target, code)
	return nil
}

// httpSender 通用 HTTP 模板适配器：绝大多数发码平台就是一个 HTTP 接口，
// 用模板配置即可对接（URL/请求头/请求体支持 {{target}} {{code}} {{scene}} 占位符）。
// 配置属管理员可信来源（同 platform_source_configs，允许指向内网）。
type httpSender struct{}

func (httpSender) Name() string { return "http" }

func (httpSender) Send(scene, target, code string) error {
	rawURL := db.GetSetting("verify_http_url")
	if rawURL == "" {
		return fmt.Errorf("verify_http_url 未配置")
	}
	replace := func(s string) string {
		s = strings.ReplaceAll(s, "{{target}}", target)
		s = strings.ReplaceAll(s, "{{code}}", code)
		s = strings.ReplaceAll(s, "{{scene}}", scene)
		return s
	}

	method := strings.ToUpper(db.GetSetting("verify_http_method"))
	if method == "" {
		method = http.MethodPost
	}

	var bodyReader io.Reader
	body := replace(db.GetSetting("verify_http_body"))
	if method != http.MethodGet && method != http.MethodHead && body != "" {
		bodyReader = strings.NewReader(body)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, replace(rawURL), bodyReader)
	if err != nil {
		return err
	}
	// 请求头：verify_http_headers 为 JSON 对象字符串，如 {"Authorization":"Bearer xx","Content-Type":"application/json"}
	if headers := strings.TrimSpace(db.GetSetting("verify_http_headers")); headers != "" {
		var kv map[string]string
		if json.Unmarshal([]byte(headers), &kv) == nil {
			for k, v := range kv {
				req.Header.Set(k, v)
			}
		}
	}
	if req.Header.Get("Content-Type") == "" && bodyReader != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("发码平台返回 %d: %s", resp.StatusCode, truncateStr(string(respBody), 200))
	}
	// 可选成功关键字：平台 200 但业务失败（body 带错误码）的场景用
	if keyword := db.GetSetting("verify_http_success_keyword"); keyword != "" && !bytes.Contains(respBody, []byte(keyword)) {
		return fmt.Errorf("发码平台响应未含成功标记: %s", truncateStr(string(respBody), 200))
	}
	return nil
}

func truncateStr(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
