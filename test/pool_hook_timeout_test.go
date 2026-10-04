package test

// 第二十一遍巡检的两条（待办清单 P81 / P82），判法是「这条调用带的是谁的 context、超时从哪来」。
//
// P81：号池调源写的钩子时传的是裸 `context.Background()`——没有截止。而维护与装填是**持着 p.mu**
// 调它们的（一个卡住的上游能把该池所有请求的 Acquire 整个挂住，且维护协程一去不回）。
// P82：Redis 客户端的 `ReadTimeout` 取的是 `CACHE_TTL` 的秒数——一个**缓存存活期**的运营口径
// 被当成了**socket 读超时**。现网 CACHE_TTL=300，所以那个读超时其实是 300 秒（库里默认 3 秒）。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"loomproxy/base/pool"
)

// hookBlockProvider 的 Refresh 在 blockNow 置真后**只在 ctx 结束时返回**：
// 它代表的是"守规矩、但上游卡住了"的源。修前框架传的 ctx 没有截止，于是它永远不返回——
// 而调用它的维护循环正持着 p.mu（待办清单 P81）。
type hookBlockProvider struct {
	*fakeProvider
	blockNow bool
	mu       sync.Mutex
	sawCtx   bool
	hadDL    bool
	deadline time.Time
}

func (h *hookBlockProvider) Refresh(ctx context.Context, dev *pool.Device) (pool.Quota, error) {
	h.mu.Lock()
	blocked := h.blockNow
	dl, ok := ctx.Deadline()
	h.sawCtx, h.hadDL, h.deadline = true, ok, dl
	h.mu.Unlock()
	if !blocked {
		return h.fakeProvider.Refresh(ctx, dev)
	}
	select {
	case <-ctx.Done():
		return pool.Quota{}, ctx.Err()
	case <-time.After(20 * time.Second):
		return pool.Quota{}, fmt.Errorf("钩子等了 20 秒还没被叫醒——框架没给 ctx 设截止")
	}
}

func (h *hookBlockProvider) setBlock(v bool) {
	h.mu.Lock()
	h.blockNow = v
	h.mu.Unlock()
}

func (h *hookBlockProvider) snapshot() (saw, had bool, dl time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sawCtx, h.hadDL, h.deadline
}

func TestPoolHookCallsCarryDeadline(t *testing.T) {
	newTestServer(t)
	hp := &hookBlockProvider{fakeProvider: newFakeProvider()}
	hp.claimExtend = time.Minute // 转正后即临期，下一轮 Maintain 会去刷新台账

	const ident = "a1a1a1a1-0000-0000-0000-0000000000f5"
	insertDevice(t, ident, pool.StatusCold, 0, fakeQuotaMax, nil)
	hp.quota[ident] = pool.Quota{Total: fakeQuotaMax} // 不带 ExpiresAt：领取后有效期只有 claimExtend 那 1 分钟，才真的是「临期号」

	cfg := fakePoolConfig()
	cfg.HookTimeout = 300 * time.Millisecond
	p := newFakePool(t, pool.New(hp, cfg))
	p.Start()
	if got := p.HotCount(); got != 1 {
		t.Fatalf("预置活跃号 = %d, want 1——前提不成立，后面的 Maintain 什么都验不了", got)
	}

	spoken, text := capturePoolLog(t)
	hp.mu.Lock()
	hp.sawCtx = false // 只数 Maintain 这一段的调用
	hp.mu.Unlock()
	hp.setBlock(true)

	done := make(chan time.Duration, 1)
	start := time.Now()
	go func() {
		p.Maintain()
		done <- time.Since(start)
	}()
	var elapsed time.Duration
	select {
	case elapsed = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Maintain 10 秒没回来——钩子调用仍然没有截止，一个卡住的上游就是整个池卡住")
	}
	if elapsed > 5*time.Second {
		t.Errorf("一轮维护花了 %v，远超配置的 300ms 钩子超时", elapsed)
	}

	saw, had, dl := hp.snapshot()
	if !saw {
		t.Error("Refresh 一次都没被调用——用例没经过被测通路（断言空转）")
	}
	if !had {
		t.Error("传给 Provider 的 ctx 没有截止——P81 没修上")
	} else if rem := time.Until(dl); rem > 300*time.Millisecond || rem <= -300*time.Millisecond {
		t.Errorf("ctx 的截止 = %v（距今 %v），应落在 300ms 量级内", dl, rem)
	}
	if !lockFree(p) {
		t.Fatal("钩子超时之后 p.mu 拿不到——超时没有把临界区放开")
	}
	if n := spoken("refresh hot device"); n != 1 {
		t.Errorf("钩子超时的失败日志 %d 条, want 1（应走既有的失败出口，不另加噪音）\n日志：%s", n, text())
	}
	if !strings.Contains(text(), "deadline") {
		t.Errorf("失败日志里看不出是超时（读数要能指到成因）\n日志：%s", text())
	}
	hp.setBlock(false)
}

// TestCacheTTLNotUsedAsNetworkTimeout 是 P82 的守卫：**网络超时参数不许取自缓存存活期**。
// 之所以扫源码而不是构造一个 Redis 客户端：用例环境里没有可连的 Redis，
// 而这条改的本来就是"初始化那一次赋值取了谁"——判据落在那个表达式上最直接。
func TestCacheTTLNotUsedAsNetworkTimeout(t *testing.T) {
	var hits []string
	root := filepath.Join("..", "utils")
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil // 本文件与对照组里同时出现两个词
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for i, line := range strings.Split(string(data), "\n") {
			l := strings.TrimSpace(line)
			if strings.HasPrefix(l, "//") {
				continue
			}
			if strings.Contains(l, "Timeout") && strings.Contains(l, "CacheTTL") {
				rel, _ := filepath.Rel("..", path)
				hits = append(hits, fmt.Sprintf("%s:%d: %s", rel, i+1, l))
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("遍历 utils 失败: %v", err)
	}
	if len(hits) > 0 {
		t.Errorf("有网络超时参数取自 CACHE_TTL（缓存存活期 ≠ 读超时，待办清单 P82）:\n  %s",
			strings.Join(hits, "\n  "))
	}
}
