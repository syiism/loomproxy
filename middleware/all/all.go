// Package all 中间件导入集合：本文件的空白导入清单即「本部署装载哪些中间件」。
//
// 接入一个新中间件 = 新建 middleware/<职责> 包（或进 gate 的对应文件）+ init() 里
// 一次 middleware.Register + 本文件加一行空白导入；`app.go` 免改——链顺序由各 Def.Order
// 声明、装配由 Globals/RouteChain 完成，不再有「逐行 r.Use / 逐行 append」。
//
// 与 sources/all.go 的分工是两件事：这里决定「带哪些中间件」，那里决定「带哪些数据源」。
// 清单之所以单独成包（而不是像 sources 那样放在包根），是因为 middleware 根包必须是叶子包
// （只依赖 gin）：清单若与注册表同包，就形成 middleware → transport → middleware 的导入环。
package all

import (
	_ "loomproxy/gate"                 // 管控三轴：access / billing / ratelimit
	_ "loomproxy/middleware/apiauth"   // API 网关鉴权
	_ "loomproxy/middleware/ipblock"   // IP 黑名单拦截 + 自动拉黑计数
	_ "loomproxy/middleware/source"    // baseUrl 解析与 SSRF 校验 + 调用监控
	_ "loomproxy/middleware/transport" // requestid / recovery / logging / cors / cachecontrol
)
