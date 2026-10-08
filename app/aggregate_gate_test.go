package app

// 聚合搜索那道闸的钉法（待办清单 P110 的①）。
//
// 原来的形状是"每个请求新建一个容量 4 的信号量"——它挡得住一个请求同时打 8 个源，
// 挡不住 N 个用户各发一个聚合请求打同一个源，而注释写的威胁是后者。
// 现在闸按源建、跨请求共享，所以这里断言的是**跨请求**这一维。

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"loomproxy/conf"
)

func resetAggregateGates(t *testing.T) {
	t.Helper()
	aggregateGateMu.Lock()
	aggregateGateCache = map[string]chan struct{}{}
	aggregateGateMu.Unlock()
	saved := conf.Config
	conf.Config = &conf.ConfMgr{}
	t.Cleanup(func() { conf.Config = saved })
}

func TestAggregateGateIsSharedAcrossRequests(t *testing.T) {
	resetAggregateGates(t)

	// 模拟三个并发用户、每人 8 个目标源都指向同一个源：旧形状下同时在飞最多 3×4=12，
	// 按源共享的上限是 4。
	var mu sync.Mutex
	var cur, peak int
	releaseAll := make(chan struct{})
	var wg sync.WaitGroup
	for r := 0; r < 3; r++ {
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				done, err := aggregateAcquire(context.Background(), "shared_source")
				if err != nil {
					t.Errorf("排队报错: %v", err)
					return
				}
				mu.Lock()
				cur++
				if cur > peak {
					peak = cur
				}
				mu.Unlock()
				<-releaseAll
				done()
				mu.Lock()
				cur--
				mu.Unlock()
			}()
		}
	}
	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	got := peak
	mu.Unlock()
	if got > 4 {
		t.Errorf("同一源同时在飞 %d 个，超过按源上限 4——跨请求共享没生效", got)
	}
	if got < 2 {
		t.Errorf("峰值只有 %d，并发没同时进来，用例在空转", got)
	}
	close(releaseAll)
	wg.Wait()
}

func TestAggregateGateIsPerSource(t *testing.T) {
	resetAggregateGates(t)
	var wg sync.WaitGroup
	block := make(chan struct{})
	// 源 A 占满 4 格，源 B 的 4 发不该被 A 堵住
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel, err := aggregateAcquire(context.Background(), "busy_source")
			if err != nil {
				t.Error(err)
				return
			}
			<-block
			rel()
		}()
	}
	time.Sleep(80 * time.Millisecond)
	ok := make(chan struct{})
	go func() {
		rel, err := aggregateAcquire(context.Background(), "other_source")
		if err != nil {
			t.Error(err)
			return
		}
		rel()
		close(ok)
	}()
	select {
	case <-ok:
	case <-time.After(300 * time.Millisecond):
		t.Error("另一个源被 busy_source 堵住了——闸必须按源分")
	}
	close(block)
	wg.Wait()
}

func TestAggregateGateHonorsCancel(t *testing.T) {
	resetAggregateGates(t)
	block := make(chan struct{})
	for i := 0; i < 4; i++ {
		rel, err := aggregateAcquire(context.Background(), "full_source")
		if err != nil {
			t.Fatal(err)
		}
		defer rel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := aggregateAcquire(ctx, "full_source")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("满了又没人等，应当立刻回 ctx.Err()，实际: %v", err)
	}
	close(block)
}

func TestAggregateGateReadsConfiguredMax(t *testing.T) {
	resetAggregateGates(t)
	conf.Config.AggregatePerSourceMax = 2
	var wg sync.WaitGroup
	cur, peak := 0, 0
	var mu sync.Mutex
	block := make(chan struct{})
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel, err := aggregateAcquire(context.Background(), "cfg_source")
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			cur++
			if cur > peak {
				peak = cur
			}
			mu.Unlock()
			<-block
			rel()
		}()
	}
	time.Sleep(120 * time.Millisecond)
	mu.Lock()
	got := peak
	mu.Unlock()
	close(block)
	wg.Wait()
	if got > 2 {
		t.Errorf("配置写的是 2，同时在飞 %d 个", got)
	}
	if got != 2 {
		t.Errorf("峰值 %d，没占满配置的 2 格，用例在空转", got)
	}
}
