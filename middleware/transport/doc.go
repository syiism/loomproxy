// Package transport 与业务无关的传输层环节：请求标识、panic 兜底、访问日志、
// CORS、响应缓存控制。
//
// 全部 Scope=Global，链内顺序由 middleware 的 Order 常量决定（10/20/30/40/50），
// 每个文件在 init() 里注册自己那一条。
package transport
