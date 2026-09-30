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
		dnsCacheMu.Lock()
		now := time.Now()
		for host, entry := range dnsCache {
			if now.Sub(entry.added) > dnsCacheTTL {
				delete(dnsCache, host)
			}
		}
		dnsCacheMu.Unlock()
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
	dnsCacheMu.RLock()
	if cached, ok := dnsCache[host]; ok && time.Since(cached.added) < dnsCacheTTL {
		dnsCacheMu.RUnlock()
		return cached.ips, nil
	}
	dnsCacheMu.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("DNS resolution failed: %w", err)
	}

	dnsCacheMu.Lock()
	dnsCache[host] = dnsCacheEntry{ips: ips, added: time.Now()}
	dnsCacheMu.Unlock()

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
