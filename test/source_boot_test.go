package test

// 数据源装配后钩子 base.SourceMeta.OnBoot（声明位）的用例：
// 钩子跑在「conf.Load + db.Init 之后、号池装载之前」，且一个源的准备失败或 panic
// 不许拖垮启动。这条时序是番茄系设备会话池能在启动日志里出现的前提（分支待办 S10）。

import (
	"errors"
	"sync/atomic"
	"testing"

	"loomproxy/app"
	"loomproxy/base"
	"loomproxy/base/pool"
	"loomproxy/db"
	"loomproxy/models"
	"loomproxy/testkit/fakesource"
)

// TestSourceOnBootRunsBeforeLedgerLoad 钩子登记的池、导入的号，随后 StartAll 就能装载——
// 顺序反了的症状是「面板看得见、取号取不到」
func TestSourceOnBootRunsBeforeLedgerLoad(t *testing.T) {
	newTestServer(t)
	const probePool = "boot_probe"
	cleanupProbe(t, probePool)

	var ran atomic.Int64
	fakesource.BootHook = func() error {
		ran.Add(1)
		// 钩子里做的是源在启动时要做的事：先导号进库，再登记池
		row := models.PoolDevice{Pool: probePool, Ident: "bbbbbbbb-0000-0000-0000-0000000000b1",
			Status: pool.StatusCold, Attrs: "{}"}
		if err := db.DB.Create(&row).Error; err != nil {
			return err
		}
		fp := newFakeProvider()
		fp.poolName = probePool
		cfg := pool.Config{Kind: pool.KindSpread, MaxDevices: 3, TargetDevices: 1, Interval: 3600e9}
		pool.Register(pool.New(fp, cfg))
		probeCreated = &fp.created
		return nil
	}
	t.Cleanup(func() { fakesource.BootHook = nil; cleanupProbe(t, probePool) })

	base.RunSourceBoots()
	if got := ran.Load(); got != 1 {
		t.Fatalf("OnBoot 跑了 %d 次, want 1", got)
	}

	p := pool.Get(probePool)
	if p == nil {
		t.Fatal("钩子登记的池没进注册表（面板与 StartAll 都看不到它）")
	}
	p.Start() // StartAll 的那一次由 app.Run 触发；用例里手动等价调用
	if p.HotCount() != 1 {
		t.Fatalf("钩子导入的号没被装载：hot=%d, want 1", p.HotCount())
	}
	if probeCreated != nil && *probeCreated != 0 {
		t.Errorf("装载到号了却还去上游建了新号（created=%d）——说明导入没在装载前生效", *probeCreated)
	}
	p.Stop()
}

var probeCreated *int

// TestSourceOnBootFailureIsContained 单个源的启动准备失败或 panic，只留下一行日志：
// 「一个源把服务带崩」不该是声明位允许的后果
func TestSourceOnBootFailureIsContained(t *testing.T) {
	newTestServer(t)

	fakesource.BootHook = func() error { return errors.New("外部凭证文件读不到") }
	base.RunSourceBoots() // 不该 panic，也不该有返回值让调用方中止

	var ran atomic.Int64
	fakesource.BootHook = func() error {
		ran.Add(1)
		panic("钩子炸了")
	}
	base.RunSourceBoots()
	if ran.Load() != 1 {
		t.Errorf("panic 前钩子应已执行，实得 %d", ran.Load())
	}
	// panic 被 recover 吞掉后，后面的流程照常：再跑一次不报错
	fakesource.BootHook = nil
	base.RunSourceBoots()
}

// TestSourceOnBootNotCalledByRouting 装配路由（CreateApp）不触发启动准备：
// 它是 app.Run 的启动动作。测试环境里每条用例都要重新装配一遍路由，
// 若装配即触发，源的一次性准备会被跑上几十次。
func TestSourceOnBootNotCalledByRouting(t *testing.T) {
	newTestServer(t)
	t.Cleanup(func() { fakesource.BootHook = nil })

	var ran atomic.Int64
	fakesource.BootHook = func() error { ran.Add(1); return nil }

	app.CreateApp() // 只装配
	if got := ran.Load(); got != 0 {
		t.Fatalf("装配路由不该跑 OnBoot，实得 %d 次", got)
	}
	base.RunSourceBoots()
	if got := ran.Load(); got != 1 {
		t.Fatalf("RunSourceBoots 应跑一次钩子，实得 %d", got)
	}
}

func cleanupProbe(t *testing.T, name string) {
	t.Helper()
	if p := pool.Get(name); p != nil {
		p.Stop()
		pool.Unregister(name)
	}
	if db.DB != nil {
		db.DB.Where("pool = ?", name).Delete(&models.PoolDevice{})
	}
}
