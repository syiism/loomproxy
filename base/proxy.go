package base

import (
	"log"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"loomproxy-go/conf"
)

// 上游 IP 代理池 + 请求头轮换：
//   - 代理来源：环境变量 UPSTREAM_PROXIES（逗号分隔）、UPSTREAM_PROXY_FILE（每行一个，# 注释，5s 热加载）、
//     UPSTREAM_PROXY_API（动态代理 API，定时拉取 + 逐个健康校验后入池，免费代理存活率极低，校验必不可少）
//   - 调度：随机选取健康代理；连续失败 3 次封禁 60s；代理网络错误时自动换代理/回退直连重试一次
//   - 请求头：UPSTREAM_UA_ROTATE=true 时按请求轮换 User-Agent（Handler 显式设置的 UA 优先）

const (
	proxyBanThreshold = 3
	proxyBanDuration  = 60 * time.Second
	proxyFileReload   = 5 * time.Second
	// 代理健康校验超时（免费代理多数已死，快速失败）
	proxyCheckTimeout = 6 * time.Second
)

var (
	proxyMu       sync.Mutex
	proxyFailures = make(map[string]int)
	proxyBanned   = make(map[string]time.Time)

	proxyFileMtime time.Time
	proxyFileList  []string
	proxyFileCheck time.Time

	proxyAPIList []string // API 来源且通过健康校验的代理

	proxyClients sync.Map // proxyURL -> *http.Client

	proxySourcesMu     sync.RWMutex
	proxySourcesGetter func() string // 见 SetProxySourcesGetter
)

// SetProxySourcesGetter 注入「启用代理的数据源/接口列表」读取函数（由 app 层注入，避免 base 依赖 db）。
// 返回值为逗号分隔列表，列表项支持两种粒度：数据源名（novel_a，该源全部接口）与
// 接口路径（novel_a/chapter，单个接口）；返回空串或未注入 = 不限制（保持历史行为：池非空则全部走代理）
func SetProxySourcesGetter(f func() string) {
	proxySourcesMu.Lock()
	proxySourcesGetter = f
	proxySourcesMu.Unlock()
}

// proxyAllowedForPath 判定指定路由（不含前导斜杠，如 novel_a/chapter）的上游请求是否应走代理池
func proxyAllowedForPath(path string) bool {
	proxySourcesMu.RLock()
	g := proxySourcesGetter
	proxySourcesMu.RUnlock()
	if g == nil {
		return true
	}
	raw := strings.TrimSpace(g())
	if raw == "" {
		return true
	}
	source := sourceFromPath(path)
	for _, s := range strings.Split(raw, ",") {
		item := strings.TrimSpace(s)
		if item == source || item == path {
			return true
		}
	}
	return false
}

// sourceFromPath 从路由 Path（/{source}/{action}）解析数据源名
func sourceFromPath(path string) string {
	p := strings.TrimPrefix(path, "/")
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return p
}

// proxyPool 返回当前可用代理列表（env + 文件 + API 合并）
func proxyPool() []string {
	var pool []string
	if conf.Config != nil {
		pool = append(pool, conf.Config.UpstreamProxies...)
		if f := conf.Config.UpstreamProxyFile; f != "" {
			pool = append(pool, loadProxyFile(f)...)
		}
	}
	proxyMu.Lock()
	pool = append(pool, proxyAPIList...)
	proxyMu.Unlock()
	return pool
}

// loadProxyFile 读取代理文件，按 mtime 热加载（5s 内不重复 stat）
func loadProxyFile(path string) []string {
	proxyMu.Lock()
	defer proxyMu.Unlock()

	if time.Since(proxyFileCheck) < proxyFileReload {
		return proxyFileList
	}
	proxyFileCheck = time.Now()

	fi, err := os.Stat(path)
	if err != nil {
		return proxyFileList
	}
	if fi.ModTime().Equal(proxyFileMtime) {
		return proxyFileList
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return proxyFileList
	}
	var list []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		list = append(list, line)
	}
	proxyFileMtime = fi.ModTime()
	proxyFileList = list
	return proxyFileList
}

// healthyProxies 过滤掉封禁中的代理
func healthyProxies() []string {
	all := proxyPool()
	if len(all) == 0 {
		return nil
	}
	proxyMu.Lock()
	defer proxyMu.Unlock()
	out := make([]string, 0, len(all))
	for _, p := range all {
		if until, banned := proxyBanned[p]; banned && time.Now().Before(until) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// pickProxy 随机选取一个健康代理；无可用代理返回空串（调用方直连）
func pickProxy() string {
	pool := healthyProxies()
	if len(pool) == 0 {
		return ""
	}
	return pool[rand.IntN(len(pool))]
}

// HasHealthyProxies 当前是否存在可用代理（供上层决定风控应对策略：
// 有代理时按 IP 的封禁可通过换代理绕过，无需全局冷却）
func HasHealthyProxies() bool {
	return len(healthyProxies()) > 0
}

// pickProxyExcluding 选取一个与 exclude 不同的代理；没有则返回空串（回退直连）
func pickProxyExcluding(exclude string) string {
	pool := healthyProxies()
	filtered := pool[:0]
	for _, p := range pool {
		if p != exclude {
			filtered = append(filtered, p)
		}
	}
	if len(filtered) == 0 {
		return ""
	}
	return filtered[rand.IntN(len(filtered))]
}

// reportProxyResult 上报代理调用结果，连续失败达到阈值后封禁
func reportProxyResult(proxy string, success bool) {
	if proxy == "" {
		return
	}
	proxyMu.Lock()
	defer proxyMu.Unlock()
	if success {
		delete(proxyFailures, proxy)
		return
	}
	proxyFailures[proxy]++
	if proxyFailures[proxy] >= proxyBanThreshold {
		proxyBanned[proxy] = time.Now().Add(proxyBanDuration)
		delete(proxyFailures, proxy)
		log.Printf("代理连续失败 %d 次，封禁 %v: %s", proxyBanThreshold, proxyBanDuration, proxy)
	}
}

// clientForProxy 按代理懒创建独立 HTTP 客户端（复用默认传输参数）
func clientForProxy(proxy string) *http.Client {
	if c, ok := proxyClients.Load(proxy); ok {
		return c.(*http.Client)
	}
	proxyURL, err := url.Parse(proxy)
	if err != nil {
		return DefaultHTTPClient()
	}
	base := DefaultHTTPClient()
	transport := base.Transport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(proxyURL)
	timeout := 30 * time.Second
	if conf.Config != nil {
		timeout = time.Duration(conf.Config.TimeoutPool) * time.Second
	}
	c := &http.Client{Transport: transport, Timeout: timeout}
	actual, _ := proxyClients.LoadOrStore(proxy, c)
	return actual.(*http.Client)
}

// userAgents 请求头轮换池（移动端为主，贴近阅读类客户端真实分布）
var userAgents = []string{
	"Mozilla/5.0 (Linux; Android 13; 2304FPN6DC Build/TKQ1.221114.001) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Mobile Safari/537.36",
	"Mozilla/5.0 (Linux; Android 12; M2007J3SC Build/SKQ1.211006.001) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/118.0.0.0 Mobile Safari/537.36",
	"Mozilla/5.0 (Linux; Android 14; Pixel 8 Pro Build/UP1A.231005.007) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/121.0.0.0 Mobile Safari/537.36",
	"Mozilla/5.0 (Linux; Android 11; Redmi K40 Build/RKQ1.200826.002) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/117.0.0.0 Mobile Safari/537.36",
	"Mozilla/5.0 (Linux; Android 13; SM-S918B Build/TP1A.220624.014) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Mobile Safari/537.36",
	"Mozilla/5.0 (iPhone; CPU iPhone OS 17_2 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.2 Mobile/15E148 Safari/604.1",
	"Mozilla/5.0 (iPhone; CPU iPhone OS 16_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.6 Mobile/15E148 Safari/604.1",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/119.0.0.0 Safari/537.36",
	"okhttp/4.12.0",
	"Dalvik/2.1.0 (Linux; U; Android 13; 2304FPN6DC Build/TKQ1.221114.001)",
	"reading/3.5.0 (Android)",
}

// uaRotateEnabled 是否开启 UA 轮换
func uaRotateEnabled() bool {
	return conf.Config != nil && conf.Config.UpstreamUARotate
}

// pickUserAgent 随机返回一个 UA
func pickUserAgent() string {
	return userAgents[rand.IntN(len(userAgents))]
}
