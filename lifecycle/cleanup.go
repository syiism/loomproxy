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
	cleanupMu.Lock()
	fns := cleanupFuncs
	cleanupMu.Unlock()
	for _, fn := range fns {
		fn()
	}
}
