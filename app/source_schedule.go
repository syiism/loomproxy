package app

// 数据源定时启用的后台循环。判定的那一轮在 handlers/admin（admin.RunSourceScheduleTick）——
// 与本仓后台任务的既有分工一致：循环在这里起，语义在 admin 里，用例直接驱动那一轮而不必起协程
// （见 app/monitor.go 的 startSubjectNameBackfill / admin.RunSubjectBackfillTick）。

import (
	"context"
	"log"
	"time"

	"loomproxy/base"
	"loomproxy/handlers/admin"
)

// sourceScheduleTick 判定间隔。窗口的最细粒度是分钟，30 秒一轮把触发误差压在一分钟以内；
// 再密没有意义——判定本身只在进出窗口的那一刻写库，中间那些 tick 是空转。
const sourceScheduleTick = 30 * time.Second

// startSourceScheduler 启动定时启用循环。
// 放在 app.Run 里 db.Init 与 conf.Load 之后：判定要读库，也要用平台时区（TZ_OFFSET_HOURS）。
func startSourceScheduler(ctx context.Context) {
	// 启动先跑一轮：重启时若正落在某个窗口里，源应当立刻回到启用态，
	// 而不是等第一个 tick——那一等最长 30 秒，期间的访问全是 403。
	base.Supervised("数据源定时启用的一轮（启动时）", func() { admin.RunSourceScheduleTick(time.Now()) })

	go func() {
		ticker := time.NewTicker(sourceScheduleTick)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// 后台协程不在 gin Recovery 覆盖范围内（待办清单 P103），一轮 panic 会带走整个进程
				base.Supervised("数据源定时启用的一轮", func() { admin.RunSourceScheduleTick(time.Now()) })
			}
		}
	}()
	log.Printf("数据源定时启用任务已启动：每 %v 一轮", sourceScheduleTick)
}
