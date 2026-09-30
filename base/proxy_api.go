package base

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"loomproxy/conf"
)

// 动态代理池（API 来源，如 https://proxy.scdn.io/api/get_proxy.php?protocol=socks5&count=20&country_code=CN）：
// 池自维护水位模型——维护协程周期性检查可用代理数（剔除封禁中），低于水位（proxyAPIMinAlive）
// 时自动补充：先复验池内存量丢弃已死代理，再逐轮拉取新代理并健康校验（经代理请求
// UPSTREAM_PROXY_CHECK_URL，<400 视为存活），直至回到水位或达到最大轮数。
// 消费方（Fetch）只从池中随机取用，不关心补充过程；运行期 3 次失败封禁 60s 的机制不变，
// 解封后代理自然回池，无需额外 API 调用。
// 免费代理存活率极低且寿命短（分钟级），校验与补充必不可少；API 拉取与校验均直连不经代理。

const (
	// proxyAPIMinAlive 池水位：可用代理数低于该值时触发补充
	proxyAPIMinAlive = 5
	// proxyAPIMaxRounds 单次补充的最大拉取轮数（防止代理 API 质量差时死循环）
	proxyAPIMaxRounds = 5
	// proxyAPIRetryDelay 补充过程中拉取轮次间的间隔
	proxyAPIRetryDelay = 3 * time.Second
	// proxyMaintainTick 水位检查周期
	proxyMaintainTick = 15 * time.Second
)

// proxyAPIResponse 代理 API 响应（兼容 data.proxies 字符串数组）
type proxyAPIResponse struct {
	Code int `json:"code"`
	Data struct {
		Proxies []string `json:"proxies"`
	} `json:"data"`
}

// StartProxyAPIRefresher 启动动态代理池维护协程（配置了 UPSTREAM_PROXY_API 时），随 ctx 退出。
// UPSTREAM_PROXY_API_INTERVAL 为两次补充之间的最小间隔（防止代理 API 被狂刷）
func StartProxyAPIRefresher(ctx context.Context) {
	if conf.Config == nil || conf.Config.UpstreamProxyAPI == "" {
		return
	}
	gap := time.Duration(conf.Config.UpstreamProxyAPIInterval) * time.Second
	if gap < 30*time.Second {
		gap = 30 * time.Second
	}
	go maintainProxyPool(ctx, gap)
	log.Printf("动态代理池已启用: %s（水位 >= %d，补充间隔 >= %v，校验 %s）",
		conf.Config.UpstreamProxyAPI, proxyAPIMinAlive, gap, conf.Config.UpstreamProxyCheckURL)
}

// maintainProxyPool 水位维护循环：低于水位且距上次补充超过 gap 时补充
func maintainProxyPool(ctx context.Context, gap time.Duration) {
	lastReplenish := time.Time{} // 零值：启动即首次补充
	t := time.NewTicker(proxyMaintainTick)
	defer t.Stop()
	for {
		if healthyAPICount() < proxyAPIMinAlive && time.Since(lastReplenish) >= gap {
			replenishProxies()
			lastReplenish = time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// healthyAPICount 当前 API 池剔除封禁中代理后的可用数
func healthyAPICount() int {
	proxyMu.Lock()
	defer proxyMu.Unlock()
	count := 0
	for _, p := range proxyAPIList {
		if until, banned := proxyBanned[p]; banned && time.Now().Before(until) {
			continue
		}
		count++
	}
	return count
}

// replenishProxies 补充代理池至水位：先复验存量丢弃已死代理，
// 再逐轮拉取新代理校验入池（跨轮去重，避免重复校验 API 重复下发的同一批死代理）
func replenishProxies() {
	proxyMu.Lock()
	existing := append([]string(nil), proxyAPIList...)
	proxyMu.Unlock()

	alive := validateProxies(existing)
	seen := make(map[string]bool)
	for _, p := range existing {
		seen[p] = true
	}

	totalFetched := 0
	for round := 1; len(alive) < proxyAPIMinAlive && round <= proxyAPIMaxRounds; round++ {
		proxies, err := fetchProxyAPI()
		if err != nil {
			log.Printf("动态代理池: 拉取失败（第 %d/%d 轮）: %v", round, proxyAPIMaxRounds, err)
			break
		}
		totalFetched += len(proxies)
		var fresh []string
		for _, p := range proxies {
			if !seen[p] {
				seen[p] = true
				fresh = append(fresh, p)
			}
		}
		alive = append(alive, validateProxies(fresh)...)
		if len(alive) < proxyAPIMinAlive && round < proxyAPIMaxRounds {
			time.Sleep(proxyAPIRetryDelay)
		}
	}

	proxyMu.Lock()
	proxyAPIList = alive
	proxyMu.Unlock()
	pruneProxyClients()
	if len(alive) < proxyAPIMinAlive {
		log.Printf("动态代理池: 复验 %d 个 + 新拉 %d 个后存活仅 %d 个（水位 %d，下轮补充将在间隔后触发）",
			len(existing), totalFetched, len(alive), proxyAPIMinAlive)
		return
	}
	log.Printf("动态代理池: 复验 %d 个 + 新拉 %d 个，当前存活 %d 个", len(existing), totalFetched, len(alive))
}

// fetchProxyAPI 请求代理 API，返回归一化（带 scheme）的代理列表（直连请求，不走代理）
func fetchProxyAPI() ([]string, error) {
	apiURL := conf.Config.UpstreamProxyAPI
	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("代理 API 返回 %s", resp.Status)
	}
	var parsed proxyAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("代理 API 响应解析失败: %w", err)
	}
	var out []string
	seen := make(map[string]bool)
	scheme := conf.Config.UpstreamProxyAPIScheme
	if scheme == "" {
		scheme = "http"
	}
	for _, p := range parsed.Data.Proxies {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.Contains(p, "://") {
			p = scheme + "://" + p
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out, nil
}

// validateProxies 并发健康校验：经代理请求校验 URL，能拿到 <400 的 HTTP 响应视为存活
func validateProxies(proxies []string) []string {
	if len(proxies) == 0 {
		return nil
	}
	checkURL := conf.Config.UpstreamProxyCheckURL
	if checkURL == "" {
		checkURL = "https://www.baidu.com"
	}
	var mu sync.Mutex
	var alive []string
	var wg sync.WaitGroup
	for _, p := range proxies {
		p := p
		wg.Add(1)
		go func() {
			defer wg.Done()
			if checkProxyAlive(p, checkURL) {
				mu.Lock()
				alive = append(alive, p)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return alive
}

// checkProxyAlive 单个代理健康校验
func checkProxyAlive(proxy, checkURL string) bool {
	req, err := http.NewRequest(http.MethodGet, checkURL, nil)
	if err != nil {
		return false
	}
	client := clientForProxy(proxy)
	// 校验用短超时，不能复用池化 client 的长 Timeout
	c := *client
	c.Timeout = proxyCheckTimeout
	resp, err := c.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode < 400
}

// pruneProxyClients 清理不再存在于任何来源的代理客户端，防止长期运行积累
func pruneProxyClients() {
	current := make(map[string]bool)
	for _, p := range proxyPool() {
		current[p] = true
	}
	proxyClients.Range(func(key, _ interface{}) bool {
		if !current[key.(string)] {
			proxyClients.Delete(key)
		}
		return true
	})
}
