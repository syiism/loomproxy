package base

import (
	"log"
	"runtime/debug"
)

// Guard 是后台协程的 panic 兜底，用法是 `defer base.Guard("这一轮在干什么")`。
//
// 为什么要有它（待办清单 P103）：Go 里**任何协程**的未恢复 panic 终止的都是整个进程，
// 不是那一条循环；而 `Restart=always` 会把崩溃循环伪装成"运行中"（AGENTS.md §3 换装三查第 3 条）。
// gin 的 Recovery 只覆盖请求协程，后台协程不在它里面。
//
// 语义要说准：**拦下只跳过这一轮**，不重试、不重启循环——周期性任务下一 tick 还会再来，
// 与"刷新失败下轮再试"是同一条规矩。一次性协程不该用它：那种 panic 该让进程下去，
// 因为没有"下一轮"会来纠正它，装作拦下了反而把缺陷藏起来。
//
// 这一族在本仓原本有四份各写各的：号池两条路径（`Pool.guardPanic`，P77/P78）、
// 源启动钩子（`RunSourceBoots`）、聚合搜索逐目标（`app.callSourceHandler`）。
// 判据页那条「同一个判据在几个包里各写一份」说的就是这个形状，所以新增的调用点一律走这里，
// 号池那两处也改成委托（它带池名的日志格式由调用方拼好传进来，读数不变）。
func Guard(what string) {
	if r := recover(); r != nil {
		log.Printf("ERROR: 后台路径「%s」panic 已拦下（本轮跳过，进程与下一轮照常）：%v\n%s", what, r, debug.Stack())
	}
}

// Supervised 把一轮具体工作包起来跑，等价于 `defer Guard(what); fn()`，
// 用在"循环体里的一轮"这种不能靠调用方 defer 的位置（defer 写在 for 里会攒到函数退出才执行）。
func Supervised(what string, fn func()) {
	defer Guard(what)
	fn()
}
