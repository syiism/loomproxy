package test

// 单飞（`base.NewFlight`）**leader panic 那条收口路径**的用例，本轮因为改过它的锁结构才补
// （同 key 合并在 `resilience_test.go` 的 `TestSingleflightCoalescesConcurrentRequests` 里
// 已经从 handler 层测过；下面第一条是在 `Flight` 这一层直接验一遍合并——因为我改的就是这一层，
// 从上游打进来会经过缓存/熔断那些别的锁，挡不住这里改坏）。
// 要钉的三件事（待办清单 P93）：
//  1. leader **panic 时收口照做**（摘槽 + 唤醒 follower），panic 继续往上抛给调用方；
//  2. panic 之后同一把锁**还能用**——收口若写成尾解锁，一次 panic 会把锁永久留在手里，
//     此后同 key 的每一次取数都卡在 `Do` 里，而且**没有任何报错**
//     （gin 的 recovery 在最外层兜住了 panic，进程照旧 is-active）；
//  3. 失败/空结果**不被共享**（`Do` 里那段结果校验的契约）。
//
// **死锁不会让断言红，只会让测试挂**，所以每个阶段都带截止时间：卡住就 `t.Fatal` 并报出卡在哪一步。

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"loomproxy/base"
)

// callWithin 在一个 goroutine 里跑 fn，最多等 timeout；超时就直接判红并说明卡在哪。
func callWithin(t *testing.T, timeout time.Duration, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatalf("%s 在 %s 内没返回——锁被带走就是这个形状（没有 panic、没有报错，只有卡住）", what, timeout)
	}
}

// doWithin 是 `f.Do` 版的带截止调用：在别的 goroutine 里跑，panic 在那里 recover 回来。
// **每一次 `f.Do` 都必须走这两个包装之一**——少一处，锁被毒化时的表现就不是一条会红的断言，
// 而是整测试二进制 `panic: test timed out`（本轮第一次就撞成这样，堆栈指向用例自己）。
func doWithin(t *testing.T, f *base.Flight, key string, timeout time.Duration, fn func() (interface{}, error)) (interface{}, error, interface{}) {
	t.Helper()
	type outcome struct {
		v   interface{}
		err error
		rec interface{}
	}
	ch := make(chan outcome, 1)
	go func() {
		res := outcome{}
		defer func() { res.rec = recover(); ch <- res }()
		res.v, res.err = f.Do(key, fn)
	}()
	select {
	case o := <-ch:
		return o.v, o.err, o.rec
	case <-time.After(timeout):
		t.Fatalf("Do(%q) 在 %s 内没返回——单飞锁被上一次 panic 带走了（待办清单 P93 的形状）", key, timeout)
		return nil, nil, nil
	}
}

func TestSingleflightSharesOneCall(t *testing.T) {
	f := base.NewFlight(2 * time.Second)

	var runs int32
	var wg sync.WaitGroup
	results := make([]interface{}, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, err := f.Do("k-share", func() (interface{}, error) {
				atomic.AddInt32(&runs, 1)
				time.Sleep(50 * time.Millisecond) // 让后面的并发调用赶上进同一个 leader
				return "同一个结果", nil
			})
			if err != nil {
				t.Errorf("第 %d 个调用报错：%v", i, err)
			}
			results[i] = v
		}(i)
	}
	callWithin(t, 30*time.Second, "20 个并发同 key 取数", wg.Wait)

	if n := atomic.LoadInt32(&runs); n != 1 {
		t.Errorf("fn 执行了 %d 次，want 1 次——同 key 的并发没被合并（改成每轮重新竞争的那一步最容易碰坏这里）", n)
	}
	for i, v := range results {
		if v != "同一个结果" {
			t.Errorf("第 %d 个调用拿到的结果 = %v，want 共享的那一个", i, v)
		}
	}
}

func TestSingleflightLeaderPanicDoesNotPoisonLock(t *testing.T) {
	f := base.NewFlight(2 * time.Second)
	key := "k-panic"

	// ① follower 先挂上去等一个会 panic 的 leader
	followerDone := make(chan error, 1)
	go func() {
		_, err := f.Do(key, func() (interface{}, error) {
			time.Sleep(80 * time.Millisecond)
			return nil, nil // 空结果：按契约不共享，follower 会重新竞争
		})
		followerDone <- err
	}()
	time.Sleep(10 * time.Millisecond)

	// ② leader panic 必须把 panic 抛给调用方（不是被静默吞掉）
	_, _, rec := doWithin(t, f, key, 10*time.Second, func() (interface{}, error) {
		panic("上游炸了")
	})
	if rec == nil {
		t.Fatal("leader 的 panic 没有抛出来——收口把 panic 吞了，调用方会以为这次成功了")
	}

	// ③ follower 必须醒来并走完（`close(c.done)` 没做就是这里卡住）
	select {
	case <-followerDone:
	case <-time.After(10 * time.Second):
		t.Fatal("follower 没被唤醒——leader panic 后没摘槽/没关 channel，等待方永远挂在那里")
	}

	// ④ 关键的一条：panic 之后这把单飞锁**还能继续用**
	var ran int32
	v, err, rec2 := doWithin(t, f, key, 10*time.Second, func() (interface{}, error) {
		atomic.AddInt32(&ran, 1)
		return "恢复后的结果", nil
	})
	if rec2 != nil {
		t.Fatalf("panic 之后的取数 panic 了：%v", rec2)
	}
	if err != nil {
		t.Errorf("panic 之后的取数报错：%v", err)
	}
	if v != "恢复后的结果" {
		t.Errorf("panic 之后的取数结果 = %v", v)
	}
	if atomic.LoadInt32(&ran) != 1 {
		t.Errorf("panic 之后 fn 执行了 %d 次，want 1 次", atomic.LoadInt32(&ran))
	}

	// ⑤ 失败结果也不许被共享给后来的调用方（契约写在 `Do` 的结果校验那一段）
	wantErr := errors.New("上游 500")
	if _, e, _ := doWithin(t, f, "k-err", 10*time.Second, func() (interface{}, error) {
		return nil, wantErr
	}); !errors.Is(e, wantErr) {
		t.Errorf("失败调用的 err = %v，want 原样抛出 %v", e, wantErr)
	}
	called := false
	if _, e, _ := doWithin(t, f, "k-err", 10*time.Second, func() (interface{}, error) {
		called = true
		return "好的", nil
	}); e != nil || !called {
		t.Errorf("前一次失败之后的同 key 调用没有重新执行（called=%v err=%v）——失败/空结果不该被共享", called, e)
	}
}
