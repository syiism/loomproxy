package groupb

// 本包的假数据源登记（待办清单 P104 正式拆分的每包样板，与 `test/fakesource_test.go` 同一份内容）。
//
// 为什么每个包都要自带一份：`fakesource.Register()` 原来只由 `test/` 的包级 `init()` 调用，
// 拆出去的子包没有它就拿不到那三个假源——表现为聚合搜索 / 计费 / 限流 / 访问控制 / 隐私留存 /
// 额度转移那一批 404 `{"code":404,"msg":"not found"}`（**路由不存在**，seed 里也没有那些数据源），
// 而不是"并行把用例撞坏了"。这一格是拆分第二步在一次性副本里量出来的，不是猜的。

import (
	"loomproxy/testkit/fakesource"
)

const (
	fakeA           = fakesource.A
	fakeB           = fakesource.B
	fakeC           = fakesource.C
	fakeLegacyGroup = fakesource.LegacyGroup
)

func init() { fakesource.Register() }
