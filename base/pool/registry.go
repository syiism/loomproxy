package pool

import (
	"log"
	"sort"
	"sync"

	"loomproxy/conf"
	"loomproxy/lifecycle"
)

// 号池注册表：数据源包把自建池登记进来（通常在源包 init/装配阶段），
// 应用启动统一 StartAll、关停统一 StopAll，管理面板经 StatusAll 按池展示状态。

var (
	registryMu sync.RWMutex
	pools      = map[string]*Pool{}
)

// Register 登记号池（同名覆盖），返回该池便于调用方继续持有
func Register(p *Pool) *Pool {
	registryMu.Lock()
	defer registryMu.Unlock()
	pools[p.Name()] = p
	return p
}

// Unregister 注销号池（不停止其协程，由调用方自行 Stop）
func Unregister(name string) {
	registryMu.Lock()
	defer registryMu.Unlock()
	delete(pools, name)
}

// Get 按名取池，未登记返回 nil
func Get(name string) *Pool {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return pools[name]
}

// registered 持读锁返回按池名排序的池列表
func registered() []*Pool {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]*Pool, 0, len(pools))
	for _, p := range pools {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// StartAll 启动全部已登记号池（POOL_ENABLED=false 时整体跳过）。
// 幂等：已运行的池不会重复启动。
func StartAll() {
	list := registered()
	if len(list) == 0 {
		return
	}
	if conf.Config != nil && !conf.Config.PoolEnabled {
		log.Println("pool: POOL_ENABLED=false，跳过全部号池")
		return
	}
	for _, p := range list {
		p.Start()
	}
	lifecycle.RegisterCleanup(StopAll)
}

// StatusAll 全部号池的状态快照（管理面板「号池」页数据源），按池名排序
func StatusAll() []*Status {
	list := registered()
	out := make([]*Status, 0, len(list))
	for _, p := range list {
		out = append(out, p.Status())
	}
	return out
}

// StopAll 停止全部已登记号池（进程关停时经 lifecycle 调用）
func StopAll() {
	for _, p := range registered() {
		p.Stop()
	}
}
