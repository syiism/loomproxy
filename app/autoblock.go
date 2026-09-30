package app

// IP 自动拉黑：滑动窗口内 403/429 失败次数达阈值即写入黑名单（source=auto）。
//
// 信号源为 monitorMiddleware（数据源路由的最终响应状态码）；被拉黑后请求被全局
// ipBlockMiddleware 拦截、不再进入监控计数，累计天然停止。
//
// 配置（系统设置，db.GetSetting 10s 内存缓存，改后最长 10s 生效）：
//
//	auto_block_enabled     开关，默认 false
//	auto_block_threshold   窗口内 403/429 次数阈值，默认 30
//	auto_block_window_sec  滑动窗口秒数，默认 60
//
// 安全阀：回环地址（127.0.0.1/::1）永不自动拉黑——防本机部署的管理端、
// 健康检查或同机数据源项目被误伤自锁。

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"loomproxy/db"
	"loomproxy/models"
)

var autoBlockState = struct {
	sync.Mutex
	failures map[string][]time.Time
}{failures: make(map[string][]time.Time)}

func autoBlockConfig() (enabled bool, threshold int, window time.Duration) {
	enabled = db.GetSetting("auto_block_enabled") == "true"
	threshold, _ = strconv.Atoi(db.GetSetting("auto_block_threshold"))
	if threshold <= 0 {
		threshold = 30
	}
	sec, _ := strconv.Atoi(db.GetSetting("auto_block_window_sec"))
	if sec <= 0 {
		sec = 60
	}
	return enabled, threshold, time.Duration(sec) * time.Second
}

func isLoopbackIP(ip string) bool {
	if parsed := net.ParseIP(ip); parsed != nil {
		return parsed.IsLoopback()
	}
	return false
}

// RecordAutoBlockFailure 记录一次数据源路由的失败响应（仅 403/429 计数），
// 窗口内累计达阈值自动拉黑。由 monitorMiddleware 在 RecordCall 后调用；
// 导出以便集成测试直接驱动非回环地址场景。
func RecordAutoBlockFailure(ip string, status int) {
	if status != http.StatusForbidden && status != http.StatusTooManyRequests {
		return
	}
	enabled, threshold, window := autoBlockConfig()
	if !enabled || isLoopbackIP(ip) {
		return
	}

	now := time.Now()
	autoBlockState.Lock()
	// 剪枝窗口外记录后追加本次
	kept := autoBlockState.failures[ip][:0]
	for _, ts := range autoBlockState.failures[ip] {
		if now.Sub(ts) <= window {
			kept = append(kept, ts)
		}
	}
	kept = append(kept, now)
	autoBlockState.failures[ip] = kept
	n := len(kept)
	autoBlockState.Unlock()

	if n < threshold {
		return
	}

	// 已拉黑则跳过（10s 缓存，热路径友好）；拉黑成功后清掉累计
	if db.IsIPBlocked(ip) {
		return
	}
	rec := models.BlockedIP{
		IP:     ip,
		Note:   fmt.Sprintf("自动拉黑：%d 秒内 %d 次 403/429", int(window.Seconds()), n),
		Source: "auto",
	}
	if err := db.DB.Create(&rec).Error; err != nil {
		return // 唯一冲突（并发/已存在）等，忽略
	}
	autoBlockState.Lock()
	delete(autoBlockState.failures, ip)
	autoBlockState.Unlock()
	db.InvalidateBlockedIPCache()
	log.Printf("SECURITY: 自动拉黑 IP %s（%d 秒内 %d 次 403/429）", ip, int(window.Seconds()), n)
}
