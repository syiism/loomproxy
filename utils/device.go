package utils

import "strings"

// ParseDevice 从 User-Agent 解析出可读的“浏览器 · 操作系统”描述
func ParseDevice(ua string) string {
	if ua == "" {
		return "未知设备"
	}
	l := strings.ToLower(ua)

	browser := "未知浏览器"
	switch {
	case strings.Contains(l, "edg/") || strings.Contains(l, "edge/"):
		browser = "Edge"
	case strings.Contains(l, "firefox/"):
		browser = "Firefox"
	case strings.Contains(l, "opr/") || strings.Contains(l, "opera"):
		browser = "Opera"
	case strings.Contains(l, "chrome/"):
		browser = "Chrome"
	case strings.Contains(l, "safari/"):
		browser = "Safari"
	case strings.Contains(l, "okhttp"):
		browser = "App 内请求"
	case strings.Contains(l, "curl/"):
		browser = "curl"
	}

	os := ""
	switch {
	case strings.Contains(l, "windows"):
		os = "Windows"
	case strings.Contains(l, "android"):
		os = "Android"
	case strings.Contains(l, "iphone") || strings.Contains(l, "ipad"):
		os = "iOS"
	case strings.Contains(l, "mac os"):
		os = "macOS"
	case strings.Contains(l, "linux"):
		os = "Linux"
	}

	if os == "" {
		return browser
	}
	return browser + " · " + os
}
