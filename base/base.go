package base

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"loomproxy/conf"
)

type contextKey string

const RequestIDKey contextKey = "request_id"

var (
	ErrHandlerNotFound   = errors.New("handler not found")
	ErrAlreadyRegistered = errors.New("handler already registered")
	ErrInvalidHandler    = errors.New("invalid handler type")
	ErrNotAClass         = errors.New("handler is an instance, not a class")
	ErrNotAnInstance     = errors.New("handler is a class, not an instance")
	ErrUnsafeBaseURL     = errors.New("unsafe base_url")
)

type UpstreamError struct {
	StatusCode int
	Message    string
	Cause      error
}

func (e *UpstreamError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return "upstream error"
}

func (e *UpstreamError) Unwrap() error {
	return e.Cause
}

func NewUpstreamError(statusCode int, message string, cause error) *UpstreamError {
	return &UpstreamError{
		StatusCode: statusCode,
		Message:    message,
		Cause:      cause,
	}
}

func IsUpstreamError(err error) (*UpstreamError, bool) {
	var ue *UpstreamError
	if errors.As(err, &ue) {
		return ue, true
	}
	return nil, false
}

type TransformFunc func(map[string]interface{}) map[string]interface{}

type APIConfig struct {
	BaseURL       string
	Endpoint      string
	Method        string
	Headers       map[string]string
	Timeout       int
	AuthRequired  bool
	FieldMapping  map[string]string
	TransformFunc TransformFunc
}

func NewAPIConfig(baseURL, endpoint string) *APIConfig {
	return &APIConfig{
		BaseURL:      baseURL,
		Endpoint:     endpoint,
		Method:       "GET",
		Headers:      make(map[string]string),
		Timeout:      30,
		AuthRequired: true,
		FieldMapping: make(map[string]string),
	}
}

type Handler interface {
	Handle(ctx context.Context, params map[string]interface{}) (interface{}, error)
	GetPath() string
	GetMethods() []string
	GetName() string
	GetDescription() string
	GetQueryParams() []string
	AuthRequired() bool
}

type ConfigurableHandler interface {
	Handler
	Configure(config *APIConfig)
}

type Normalizer interface {
	Normalize(rawData map[string]interface{}) map[string]interface{}
}

// 全局默认注册表：泛型实现 + sync.Map 无锁读（见 registry.go）。
// 包级函数保持原有签名，全部委托给 defaultRegistry，调用方零改动。
var defaultRegistry = NewRegistry[Handler]()

func Register(name string, factory func(*APIConfig) Handler, priority int, metadata map[string]interface{}) error {
	return defaultRegistry.Register(name, factory, priority, metadata)
}

func RegisterInstance(name string, instance Handler, priority int, metadata map[string]interface{}) error {
	return defaultRegistry.RegisterInstance(name, instance, priority, metadata)
}

func Get(name string) (Handler, error) {
	return defaultRegistry.Get(name)
}

func GetOrCreate(name string, config *APIConfig) (Handler, error) {
	return defaultRegistry.GetOrCreate(name, config)
}

func GetInfo(name string) (*HandlerInfo[Handler], bool) {
	return defaultRegistry.GetInfo(name)
}

func All() []Handler {
	return defaultRegistry.All()
}

func AllClasses() []func(*APIConfig) Handler {
	return defaultRegistry.AllClasses()
}

func AllInstances() []Handler {
	return defaultRegistry.AllInstances()
}

func AllSorted() []Handler {
	return defaultRegistry.AllSorted()
}

func FilterByMetadata(key string, value interface{}) []Handler {
	return defaultRegistry.FilterByMetadata(key, value)
}

func Remove(name string) bool {
	return defaultRegistry.Remove(name)
}

func Clear() {
	defaultRegistry.Clear()
}

func Routes() map[string]map[string]interface{} {
	return defaultRegistry.Routes()
}

type BaseHandler struct {
	Path        string
	Name        string
	Methods     []string
	QueryParams []string
	Description string
	Auth        bool
	Config      *APIConfig

	// UpstreamCacheTTL 上游响应缓存时长（0 表示不缓存）。
	// detail/chapter 等幂等接口应设置短 TTL（秒级），content 等按章节唯一的接口保持 0。
	UpstreamCacheTTL time.Duration
}

var defaultHTTPClient *http.Client
var clientOnce sync.Once

func DefaultHTTPClient() *http.Client {
	clientOnce.Do(func() {
		transport := &http.Transport{
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			IdleConnTimeout:       90 * time.Second,
			ForceAttemptHTTP2:     true,
			DisableCompression:    false,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		}
		timeout := 30 * time.Second
		if conf.Config != nil {
			timeout = time.Duration(conf.Config.TimeoutPool) * time.Second
		}
		defaultHTTPClient = &http.Client{
			Transport: transport,
			Timeout:   timeout,
		}
	})
	return defaultHTTPClient
}

func NewBaseHandler() *BaseHandler {
	DefaultHTTPClient()
	return &BaseHandler{
		Methods:     []string{"GET"},
		QueryParams: []string{"api_key"},
		Auth:        true,
	}
}

func (h *BaseHandler) GetPath() string {
	return h.Path
}

func (h *BaseHandler) GetMethods() []string {
	if len(h.Methods) == 0 {
		return []string{"GET"}
	}
	return h.Methods
}

func (h *BaseHandler) GetName() string {
	return h.Name
}

func (h *BaseHandler) GetDescription() string {
	return h.Description
}

func (h *BaseHandler) GetQueryParams() []string {
	return h.QueryParams
}

func (h *BaseHandler) AuthRequired() bool {
	return h.Auth
}

func (h *BaseHandler) Handle(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	return nil, errors.New("not implemented")
}

func (h *BaseHandler) Fetch(ctx context.Context, url string, method string, headers map[string]string, body io.Reader) (*http.Response, error) {
	if method == "" {
		method = "GET"
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	// 传播 X-Request-ID 以便追踪跨服务请求
	if rid, ok := ctx.Value(RequestIDKey).(string); ok && rid != "" {
		if req.Header.Get("X-Request-ID") == "" {
			req.Header.Set("X-Request-ID", rid)
		}
	}

	// UA 轮换：Handler 未显式设置 UA 时按请求伪装
	if uaRotateEnabled() && req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", pickUserAgent())
	}

	// 熔断检查：开启期间快速失败，避免堆积在挂起的上游上
	host := upstreamHost(url)
	if !circuitAllow(host) {
		return nil, NewUpstreamError(http.StatusServiceUnavailable, "上游暂时不可用（熔断保护中），请稍后重试", nil)
	}

	// IP 代理：随机选取健康代理，代理网络错误时换代理/回退直连重试一次；
	// 按数据源接口门控（系统设置 proxy_enabled_sources，见 proxyAllowedForPath）
	client := DefaultHTTPClient()
	proxyUsed := ""
	if proxyAllowedForPath(strings.TrimPrefix(h.Path, "/")) {
		if p := pickProxy(); p != "" {
			client = clientForProxy(p)
			proxyUsed = p
		}
	}

	resp, err := client.Do(req)
	if err != nil && proxyUsed != "" && !errors.Is(err, context.Canceled) {
		reportProxyResult(proxyUsed, false)
		failed := proxyUsed
		client = DefaultHTTPClient()
		proxyUsed = ""
		if p2 := pickProxyExcluding(failed); p2 != "" {
			client = clientForProxy(p2)
			proxyUsed = p2
		}
		resp, err = client.Do(req)
	}

	if err != nil {
		if proxyUsed != "" {
			reportProxyResult(proxyUsed, false)
		}
		// 客户端主动取消不算上游故障
		if !errors.Is(err, context.Canceled) {
			circuitRecord(host, false)
		}
		return nil, err
	}
	if proxyUsed != "" {
		// 403 视为该代理 IP 被上游拒绝（上游按 IP 风控），计入代理失败促使其被封禁轮换；
		// 资源级 403（下架等）请求量小且随机分布，不易达到连续失败阈值，误封代价仅 60s
		reportProxyResult(proxyUsed, resp.StatusCode < 500 && resp.StatusCode != http.StatusForbidden)
	}
	circuitRecord(host, resp.StatusCode < 500)
	return resp, nil
}

// maxResponseBodySize 上游响应体最大读取量：防止异常/恶意上游返回超大 body 引发 OOM
const maxResponseBodySize int64 = 10 * 1024 * 1024 // 10MB

// bodyBufPool 上游响应体读取缓冲池，减少每请求的内存分配
var bodyBufPool = sync.Pool{
	New: func() interface{} {
		return bytes.NewBuffer(make([]byte, 0, 32*1024))
	},
}

func acquireBodyBuf() *bytes.Buffer {
	return bodyBufPool.Get().(*bytes.Buffer)
}

func releaseBodyBuf(buf *bytes.Buffer) {
	buf.Reset()
	bodyBufPool.Put(buf)
}

// headersKey 将请求头序列化为缓存/合并键的一部分（不同请求头的响应可能不同）
func headersKey(headers map[string]string) string {
	if len(headers) == 0 {
		return ""
	}
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString("|")
		sb.WriteString(k)
		sb.WriteString("=")
		sb.WriteString(headers[k])
	}
	return sb.String()
}

// FetchJSON 发起 GET 请求并解析 JSON body，返回顶层 map。
// 自动关闭 response body，HTTP 状态码非 2xx 返回 UpstreamError。
// 并发相同请求会被合并为单次上游调用（singleflight）；
// 上游故障且存在缓存时降级返回过期缓存。
func (h *BaseHandler) FetchJSON(ctx context.Context, url string, headers map[string]string) (map[string]interface{}, error) {
	cacheKey := ""
	if h.UpstreamCacheTTL > 0 {
		cacheKey = "json:" + url + headersKey(headers)
		if cached, ok := getUpstreamCache(cacheKey); ok {
			if m, ok2 := cached.(map[string]interface{}); ok2 {
				return m, nil
			}
		}
	}

	v, err := doSingleflight("json:"+url+headersKey(headers), func() (interface{}, error) {
		// double-check：等待期间缓存可能已被先行者填充
		if cacheKey != "" {
			if cached, ok := getUpstreamCache(cacheKey); ok {
				if m, ok2 := cached.(map[string]interface{}); ok2 {
					return m, nil
				}
			}
		}
		result, err := h.fetchJSONDirect(ctx, url, headers)
		if err == nil && cacheKey != "" {
			setUpstreamCache(cacheKey, result, h.UpstreamCacheTTL)
		}
		return result, err
	})
	if err != nil {
		// 降级：上游故障时返回过期缓存（有总比没有强）
		if cacheKey != "" {
			if stale, ok := getUpstreamCacheStale(cacheKey); ok {
				if m, ok2 := stale.(map[string]interface{}); ok2 {
					return m, nil
				}
			}
		}
		return nil, err
	}
	return v.(map[string]interface{}), nil
}

// errBodySnippet 截取上游错误响应体前 256 字节用于错误诊断
// （上游风控拒绝与资源下架可能同为 403，需凭响应体区分）
func errBodySnippet(body io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(body, 256))
	s := strings.TrimSpace(string(b))
	if s == "" {
		return ""
	}
	return ", 响应: " + s
}

// fetchJSONDirect 实际执行上游请求并解析 JSON（不经过缓存与合并）
func (h *BaseHandler) fetchJSONDirect(ctx context.Context, url string, headers map[string]string) (map[string]interface{}, error) {
	resp, err := h.Fetch(ctx, url, "GET", headers, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, NewUpstreamError(resp.StatusCode, "上游返回非 2xx: "+resp.Status+errBodySnippet(resp.Body), nil)
	}

	buf := acquireBodyBuf()
	defer releaseBodyBuf(buf)
	if _, err := io.Copy(buf, io.LimitReader(resp.Body, maxResponseBodySize)); err != nil {
		circuitRecord(upstreamHost(url), false)
		return nil, NewUpstreamError(0, "读取响应失败: "+err.Error(), err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		// 上游返回非法响应体（常见于限流），计入上游健康度
		circuitRecord(upstreamHost(url), false)
		return nil, NewUpstreamError(0, "解析 JSON 失败: "+err.Error(), err)
	}
	return result, nil
}

// FetchText 发起 GET 请求并返回文本 body（用于 HTML 抓取）。
func (h *BaseHandler) FetchText(ctx context.Context, url string, headers map[string]string) (string, error) {
	cacheKey := ""
	if h.UpstreamCacheTTL > 0 {
		cacheKey = "text:" + url + headersKey(headers)
		if cached, ok := getUpstreamCache(cacheKey); ok {
			if s, ok2 := cached.(string); ok2 {
				return s, nil
			}
		}
	}

	v, err := doSingleflight("text:"+url+headersKey(headers), func() (interface{}, error) {
		if cacheKey != "" {
			if cached, ok := getUpstreamCache(cacheKey); ok {
				if s, ok2 := cached.(string); ok2 {
					return s, nil
				}
			}
		}
		text, err := h.fetchTextDirect(ctx, url, headers)
		if err == nil && cacheKey != "" {
			setUpstreamCache(cacheKey, text, h.UpstreamCacheTTL)
		}
		return text, err
	})
	if err != nil {
		if cacheKey != "" {
			if stale, ok := getUpstreamCacheStale(cacheKey); ok {
				if s, ok2 := stale.(string); ok2 {
					return s, nil
				}
			}
		}
		return "", err
	}
	return v.(string), nil
}

// fetchTextDirect 实际执行上游请求并读取文本（不经过缓存与合并）
func (h *BaseHandler) fetchTextDirect(ctx context.Context, url string, headers map[string]string) (string, error) {
	resp, err := h.Fetch(ctx, url, "GET", headers, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", NewUpstreamError(resp.StatusCode, "上游返回非 2xx: "+resp.Status+errBodySnippet(resp.Body), nil)
	}

	buf := acquireBodyBuf()
	defer releaseBodyBuf(buf)
	if _, err := io.Copy(buf, io.LimitReader(resp.Body, maxResponseBodySize)); err != nil {
		circuitRecord(upstreamHost(url), false)
		return "", NewUpstreamError(0, "读取响应失败: "+err.Error(), err)
	}
	return buf.String(), nil
}

// GetParam 安全获取字符串参数
func GetParam(params map[string]interface{}, key string) string {
	if v, ok := params[key].(string); ok {
		return v
	}
	return ""
}

func (h *BaseHandler) NormalizeResponse(rawData map[string]interface{}) map[string]interface{} {
	if h.Config != nil && h.Config.TransformFunc != nil {
		return h.Config.TransformFunc(rawData)
	}

	if h.Config != nil && len(h.Config.FieldMapping) > 0 {
		normalized := make(map[string]interface{})
		for sourceField, targetField := range h.Config.FieldMapping {
			value := h.getNestedValue(rawData, sourceField)
			if value != nil {
				normalized[targetField] = value
			}
		}
		return normalized
	}

	return rawData
}

func (h *BaseHandler) getNestedValue(data map[string]interface{}, path string) interface{} {
	keys := splitPath(path)
	current := data
	for i, key := range keys {
		if val, ok := current[key]; ok {
			if i == len(keys)-1 {
				return val
			}
			if nested, ok := val.(map[string]interface{}); ok {
				current = nested
			} else {
				return nil
			}
		} else {
			return nil
		}
	}
	return nil
}

func splitPath(path string) []string {
	return strings.Split(path, ".")
}
