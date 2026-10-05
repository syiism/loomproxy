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
	// 写那一段包闭包 defer 解锁，日志照旧在锁外打（待办清单 P93）
	func() {
		mockMu.Lock()
		defer mockMu.Unlock()
		mockLastCode[scene+"|"+target] = code
	}()
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
		if err := json.Unmarshal([]byte(headers), &kv); err != nil {
			// **解不开就不发**（P94 的第二半）。原来这里没有失败分支：坏形状被当成"没有这个设置"，
			// 请求带着空请求头照样打出去——平台大概率回 401，运维看到的是「令牌不对」而不是「你那段 JSON 我们没用上」；
			// 更坏的一支是某个接口不校验鉴权时**静默成功**。宁可少发一条验证码，也不把匿名请求交给上游。
			// 这句 error 只进服务端日志（调用方 `verify.go` 已按 P67 那对镜像处理：对外固定句、对内带原始原因），
			// 所以带上坏值本身；但不带 target，那属 §11 的边界。
			return fmt.Errorf("verify_http_headers 不是字符串到字符串的 JSON 对象，本次未带任何请求头、已中止发码: %v（值=%s）", err, truncateStr(headers, 200))
		}
		for k, v := range kv {
			req.Header.Set(k, v)
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

// truncateStr 按 **rune** 截断（不是字节）：这里截的是发码平台的响应体，它是中文正文是常态，
// 按字节切会在尾部留下半个字符，而这段文本会进错误文案——出到日志与面板就是乱码（待办清单 P83）。
// base 层有一份同语义的 truncateRunes，两处不合并是分层约束（base 不得导入 utils/handlers）。
func truncateStr(s string, n int) string {
	r := []rune(s)
	if n <= 0 || len(r) <= n {
		return s
	}
	return string(r[:n])
}
