package base

import (
	"net/url"
	"sync"
	"time"

	"loomproxy/conf"
)

// 熔断器（按上游 host 维度）：连续失败达到阈值后开启，
// 冷却期内请求快速失败（503），避免线程堆积在挂起的上游上；
// 冷却结束后放行探测请求，成功则关闭，失败则重新开启。
//
// 计入失败的情形：网络错误、超时、上游 5xx、响应体解析失败。
// 不计入：客户端取消（context.Canceled）、上游 4xx（请求级错误，不代表上游健康度）。

type breakerState struct {
	failures int
	openedAt time.Time
}

var (
	breakerMu     sync.Mutex
	breakerStates = make(map[string]*breakerState)
)

func breakerThreshold() int {
	if conf.Config != nil && conf.Config.CircuitBreakerFailures > 0 {
		return conf.Config.CircuitBreakerFailures
	}
	return 5
}

func breakerCooldown() time.Duration {
	if conf.Config != nil && conf.Config.CircuitBreakerCooldown > 0 {
		return time.Duration(conf.Config.CircuitBreakerCooldown * float64(time.Second))
	}
	return 30 * time.Second
}

func breakerEnabled() bool {
	// conf 未加载时默认启用（库代码不应因配置缺失而 panic）
	return conf.Config == nil || conf.Config.CircuitBreakerEnabled
}

// upstreamHost 从 URL 提取 host 作为熔断维度；解析失败返回空串（不参与熔断）
func upstreamHost(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Host
}

// circuitAllow 发起上游请求前检查：熔断开启且在冷却期内返回 false
func circuitAllow(host string) bool {
	if !breakerEnabled() || host == "" {
		return true
	}
	breakerMu.Lock()
	defer breakerMu.Unlock()
	s := breakerStates[host]
	if s == nil || s.failures < breakerThreshold() {
		return true
	}
	return time.Since(s.openedAt) >= breakerCooldown()
}

// circuitRecord 上报一次上游调用结果
func circuitRecord(host string, success bool) {
	if !breakerEnabled() || host == "" {
		return
	}
	breakerMu.Lock()
	defer breakerMu.Unlock()
	s := breakerStates[host]
	if s == nil {
		s = &breakerState{}
		breakerStates[host] = s
	}
	if success {
		s.failures = 0
		return
	}
	s.failures++
	if s.failures >= breakerThreshold() {
		s.openedAt = time.Now()
	}
}
