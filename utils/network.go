package utils

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"
)

var privateNetworks []*net.IPNet

func init() {
	privateCIDRs := []string{
		"127.0.0.0/8",
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"169.254.0.0/16",
		"224.0.0.0/4",
		"240.0.0.0/4",
		"::1/128",
		"fe80::/10",
		"fc00::/7",
	}
	for _, cidr := range privateCIDRs {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err == nil {
			privateNetworks = append(privateNetworks, ipNet)
		}
	}
}

var loopbackNames = map[string]bool{
	"localhost":       true,
	"localhost.local": true,
}

type dnsCacheEntry struct {
	ips   []net.IP
	added time.Time
}

var dnsCache = map[string]dnsCacheEntry{}
var dnsCacheMu sync.RWMutex
var dnsCacheTTL = 5 * time.Minute

func init() {
	go dnsCacheCleanup()
}

func dnsCacheCleanup() {
	ticker := time.NewTicker(dnsCacheTTL)
	defer ticker.Stop()
	for range ticker.C {
		// 循环体里就地 defer 要等协程退出才解锁，下一次 tick 就抢不到锁了；包闭包（待办清单 P93）
		func() {
			dnsCacheMu.Lock()
			defer dnsCacheMu.Unlock()
			now := time.Now()
			for host, entry := range dnsCache {
				if now.Sub(entry.added) > dnsCacheTTL {
					delete(dnsCache, host)
				}
			}
		}()
	}
}

func IsSafeURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	host := parsed.Hostname()
	if host == "" {
		return false
	}
	if loopbackNames[strings.ToLower(host)] {
		return false
	}

	ips, err := resolveIPs(host)
	if err != nil || len(ips) == 0 {
		return false
	}

	for _, ip := range ips {
		if isPrivateIP(ip) {
			return false
		}
	}

	return true
}

func resolveIPs(host string) ([]net.IP, error) {
	// 查缓存那一段包闭包 defer 解锁：原来的尾解锁夹着一支 `return`，
	// 中间 panic 会把 `dnsCacheMu` 永久留在手里——此后每一次上游域名解析都卡死（待办清单 P93）。
	// 过期与未命中在闭包里合成同一个答案，语义与原来一致（过期 = 未命中，照旧去解析）。
	if cached, hit := func() (dnsCacheEntry, bool) {
		dnsCacheMu.RLock()
		defer dnsCacheMu.RUnlock()
		e, ok := dnsCache[host]
		if !ok || time.Since(e.added) >= dnsCacheTTL {
			return dnsCacheEntry{}, false
		}
		return e, true
	}(); hit {
		return cached.ips, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("DNS resolution failed: %w", err)
	}

	func() {
		dnsCacheMu.Lock()
		defer dnsCacheMu.Unlock()
		dnsCache[host] = dnsCacheEntry{ips: ips, added: time.Now()}
	}()

	return ips, nil
}

func isPrivateIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	if ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	for _, network := range privateNetworks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}
