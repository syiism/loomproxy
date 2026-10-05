package lifecycle

import (
	"sync"
)

var (
	cleanupFuncs []func()
	cleanupMu    sync.Mutex
)

// RegisterCleanup 注册关闭时执行的清理函数
func RegisterCleanup(fn func()) {
	cleanupMu.Lock()
	defer cleanupMu.Unlock()
	cleanupFuncs = append(cleanupFuncs, fn)
}

// RunCleanups 执行所有注册的清理函数
func RunCleanups() {
	// 取快照是一小段临界区，包进闭包 defer 解锁；清理函数照旧在锁外跑——
	// 包闭包而不是在函数开头写 defer，是为了**不把持锁宽度扩到那些回调上**（原来解锁就在循环之前）。
	// 原来的尾解锁在 panic 时会把 `cleanupMu` 永久留在手里（待办清单 P93）。
	fns := func() []func() {
		cleanupMu.Lock()
		defer cleanupMu.Unlock()
		return cleanupFuncs
	}()
	for _, fn := range fns {
		fn()
	}
}
