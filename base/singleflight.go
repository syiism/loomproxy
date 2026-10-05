package base

import (
	"errors"
	"sync"
	"time"
)

// 请求合并（singleflight）：并发相同的下游请求只触发一次真实调用，
// 其余 goroutine 等待并共享结果，避免缓存未命中瞬间的并发击穿。
// 自包含实现，不引入 golang.org/x/sync 依赖。
//
// 两道保险：
//   - 超时兜底：leader 超过 timeout 未返回时 follower 脱离共享自行执行，
//     防止 leader 卡死拖死全部并发请求；
//   - 结果校验：leader 返回错误或空结果时 follower 不共享失败，而是重新竞争执行，
//     避免一次瞬时故障放大为全部并发请求失败；
//   - panic 防护：leader 的 fn panic 时 defer 兜底清理并标记错误，
//     follower 走结果校验重试，不会永久阻塞。

var errSfLeaderPanic = errors.New("singleflight: leader panicked")

type sfCall struct {
	done chan struct{}
	val  interface{}
	err  error
}

// Flight 一组按 key 合并的调用；零值不可用，请经 NewFlight 构造。
type Flight struct {
	timeout time.Duration

	mu    sync.Mutex
	calls map[string]*sfCall
}

// NewFlight 构造请求合并组，timeout 为 follower 等待 leader 的兜底上限。
func NewFlight(timeout time.Duration) *Flight {
	return &Flight{timeout: timeout, calls: make(map[string]*sfCall)}
}

var defaultFlight = NewFlight(15 * time.Second) // 上游客户端超时默认 10s（TIMEOUT_POOL），留 5s 余量

// doSingleflight 相同 key 的并发调用只执行一次 fn，全部调用方共享 (val, err)
func doSingleflight(key string, fn func() (interface{}, error)) (interface{}, error) {
	return defaultFlight.Do(key, fn)
}

func (f *Flight) Do(key string, fn func() (interface{}, error)) (interface{}, error) {
	for {
		// **查与占必须在同一段临界区里**：本轮先把它拆成"读一次、再写一次"两个闭包，
		// 于是两个 goroutine 都读到空、都去登记，后写的覆盖先写的——同一 key 的并发不再合并，
		// 上游被打两发（`TestSingleflightCoalescesConcurrentRequests` 当场报 2 次）。
		// 包闭包改的只是解锁姿势，**不能顺手把 check-and-set 拆开**。
		// 也不能就地写 defer：函数体是 `for`，defer 要等 Do 返回才执行，第二轮抢同一把锁就是自死锁。
		wait, mine := func() (*sfCall, *sfCall) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if c, ok := f.calls[key]; ok {
				return c, nil
			}
			c := &sfCall{done: make(chan struct{})}
			f.calls[key] = c
			return nil, c
		}()
		if wait != nil {
			select {
			case <-wait.done:
				if wait.err == nil && wait.val != nil {
					return wait.val, nil
				}
				// 结果校验：失败/空结果不共享，重新竞争成为新 leader
				continue
			case <-time.After(f.timeout):
				// 超时兜底：脱离共享，自己执行
				return fn()
			}
		}
		return f.exec(key, mine, fn)
	}
}

func (f *Flight) exec(key string, c *sfCall, fn func() (interface{}, error)) (interface{}, error) {
	defer f.finish(key, c)
	c.val, c.err = fn()
	return c.val, c.err
}

// finish 收口一次共享：登记 panic 标记、摘槽、唤醒 follower。
// 锁一律 defer 释放——原来那段是 `Lock() … Unlock()` 的尾解锁写法，
// 而这个闭包里 `close(c.done)` **本身就能 panic**（重复关同一个 channel），
// 一旦 panic 走到这里，单飞锁就被永久持有，全站每一次上游取数都会卡在 `Do` 里（待办清单 P93）。
func (f *Flight) finish(key string, c *sfCall) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c.val == nil && c.err == nil {
		// fn 未正常返回（panic），标记错误让 follower 走结果校验重试；
		// panic 本身继续向上传播由 gin recovery 处理
		c.err = errSfLeaderPanic
	}
	delete(f.calls, key)
	close(c.done)
}
