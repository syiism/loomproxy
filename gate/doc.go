// Package gate 数据源请求链路上的管控环节：访问控制、额度计费、速率限制，
// 以及限额解析（用户覆盖 > 套餐限额 > 不限）。
//
// 与 handlers/ 的区别是方向性的：handlers 是被调用的端点，gate 是每条数据源请求
// 都要穿过的闸门。数据源包因此不需要（也不应）自己实现计费或限流。
//
// 文件划分即职责划分：access.go 访问控制、billing.go 额度计费、ratelimit.go 速率限制
// 与限流器状态机、plan.go 限额与套餐解析、usage.go 用量统计。三轴各自 init() 里向
// middleware 注册自己的 Def，链顺序由 middleware 的 Order 常量表达（400/500/600）。
package gate
