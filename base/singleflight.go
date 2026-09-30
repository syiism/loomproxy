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
		f.mu.Lock()
		if c, ok := f.calls[key]; ok {
			f.mu.Unlock()
			select {
			case <-c.done:
				if c.err == nil && c.val != nil {
					return c.val, nil
				}
				// 结果校验：失败/空结果不共享，重新竞争成为新 leader
				continue
			case <-time.After(f.timeout):
				// 超时兜底：脱离共享，自己执行
				return fn()
			}
		}
		c := &sfCall{done: make(chan struct{})}
		f.calls[key] = c
		f.mu.Unlock()
		return f.exec(key, c, fn)
	}
}

func (f *Flight) exec(key string, c *sfCall, fn func() (interface{}, error)) (interface{}, error) {
	defer func() {
		f.mu.Lock()
		if c.val == nil && c.err == nil {
			// fn 未正常返回（panic），标记错误让 follower 走结果校验重试；
			// panic 本身继续向上传播由 gin recovery 处理
			c.err = errSfLeaderPanic
		}
		delete(f.calls, key)
		close(c.done)
		f.mu.Unlock()
	}()
	c.val, c.err = fn()
	return c.val, c.err
}
