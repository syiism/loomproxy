# 中间件拆分方案（自注册 + 目录分层）

> **状态：已落地（`31f0c91`，2026-09-30）**。`make build` 全绿 + pytest 18 passed + fakegateway 实跑复核
> 顺序敏感项。三点与本文的出入，记在记忆文档 §5：补了 `GlobalNames()`（守护用例需要断言全局链顺序）、
> billing/ratelimit 的 `Applies` 使控制面路由不再空跑查库（行为等价）、
> `gate` 里 `callsToday` / `userHasDataSourceAccess` 两处既有死代码按「纯搬迁」原则保留未删。

## Context

现状：全部中间件挤在两个文件里，顺序与装配逻辑又硬编码在 `app/app.go`。

- `app/middleware.go`（226 行）装了 9 个互不相干的中间件：`requestID / recovery / logging / cors / cacheControl / ipBlock`（全局链）+ `auth / baseURLCheck / monitor`（单路由链）。
- `gate/middleware.go`（849 行）装了管控三轴 `BillingMiddleware / DataSourceAccessMiddleware / RateLimitMiddleware`，外加限额解析、用量统计、限流器状态机——四种职责共用一个文件。
- `app/app.go:341-347` 逐行 `r.Use(...)` 拼全局链；`app/app.go:191-209` 逐行 append 拼单路由链，链顺序（监控必须在访问控制/计费之前才能覆盖 403/429）只以注释形式存在，无机制保证。
- 新增一个中间件要同时改 `middleware.go` + `app.go` 两处，且顺序改动无对比审查。

目标：仿 `sources/all.go` 的「集合清单 = 本部署携带哪些」形态，把中间件按职责拆包、各自 `init()` 自注册，`app.go` 不再逐行导入与逐行拼装；**纯结构重构，运行时行为逐字节不变**。

## 决策（已与用户确认）

1. 粒度：按职责分组 4 个包（不做「一中间件一包」，避免 15 行代码一个包）。
2. `gate/` 纳入：拆文件 + 一起自注册，保持 AGENTS.md §4/§9 的「gate = 管控三轴」定位不变。

## 关键约束：为什么集合文件必须单独一个包

`sources/all.go` 能做「空白导入清单」是因为它所在的 `sources` 包**不声明任何 API**，只做聚合；各源包只 import `base`（叶子注册表）。

中间件这边如果 `middleware/all.go` 与注册表同在 `middleware` 根包，就会形成 `middleware` → `middleware/transport` → `middleware` 的导入环（Go 不允许，空白导入也不行）。`gate` 也要 import 注册表来 `Register`，同样受限。

因此**注册表必须是叶子包**，聚合清单必须是独立包：

```
middleware/            package middleware —— 叶子：注册表 + 链装配 + 共享 ctx key（只依赖 gin）
  middleware.go
middleware/all/        package all —— 空白导入清单（对应 sources/all.go 的地位）
  all.go
middleware/transport/  package transport —— 全局链：requestID / recovery / logging / cors / cacheControl
middleware/ipblock/    package ipblock   —— IP 黑名单拦截 + 自动拉黑计数（现 app/autoblock.go）
middleware/apiauth/    package apiauth   —— API 网关鉴权（现 authMiddleware）
middleware/source/     package source    —— baseUrl 解析 + SSRF + 调用监控（现 baseURLCheck/monitor）
gate/                  package gate      —— 管控三轴 + 限额解析（现 gate/middleware.go 按轴拆文件）
```

依赖方向（全部单向，无环）：
`transport|ipblock|apiauth|source|gate → middleware`（取注册 API）；
`source/monitor → ipblock`（RecordFailure，保持现 `middleware.go:183` 的耦合方向）；
`middleware/all → 上述 5 个包`（仅空白导入）；
`app → middleware + _ "middleware/all"`。
`middleware` 根包**不**导入任何子包与 `gate`——这是无环的成立条件，请在 `middleware.go` 的包注释里写明，防后人「顺手」在根包加导入。

## 注册表 API（`middleware/middleware.go`）

```go
package middleware

type Scope uint8

const (
    Global Scope = iota // 引擎级，r.Use 一次
    Route               // 单路由级，随 source/action 参数化构建
)

// Spec 描述一条待挂载路由，供 Applies 判定与 Build 取参
type Spec struct {
    Source       string // 数据源码；控制面端点为空
    Action       string
    HandlerName  string // base.Handler.GetName()，data_files 一类特例用
    AuthRequired bool   // 取自 Handler.AuthRequired()
}

type Def struct {
    Name    string
    Scope   Scope
    Order   int                 // 显式顺序，唯一事实来源（见下表）
    Applies func(Spec) bool     // 仅 Route 作用域；nil = 全量生效
    Build   func(Spec) gin.HandlerFunc
}

func Register(Def)                 // init() 期调用；重名或重 Order 直接 log.Fatalf（宁启动失败不带病运行）
func Globals() []gin.HandlerFunc    // 按 Order 升序
func RouteChain(Spec) []gin.HandlerFunc // 过滤 Applies 后按 Order 升序
func RouteNames(Spec) []string     // 同上但只出名字，供守护用例与排障日志
func Build(name string, Spec) (gin.HandlerFunc, bool) // 单点取用，供 /endpoints 这类只挂鉴权的路由
func Describe() string             // 启动时一行 INFO：本部署装载了哪些中间件、什么顺序

// 共享上下文键（原 app.go 与 middleware.go 之间的 "_resolved_baseUrl" 裸字符串契约）
const CtxResolvedBaseURL = "_resolved_baseUrl"
const CtxRequestID = "request_id"
```

顺序常量集中在 `middleware.go`，留出 100 的间隙给后续插入：

```
全局链  RequestID 10 · Recovery 20 · Logging 30 · CORS 40 · CacheControl 50 · IPBlock 60
路由链  APIAuth 100 · Monitor 200 · BaseURLCheck 300 · Access 400 · Billing 500 · RateLimit 600
```

排序用 `sort.SliceStable` + Order，**不依赖 init() 执行顺序**（Go 的 init 顺序由导入图决定，跨包不可读且易被一次 gofmt/import 重排破坏）。

三条隐性不变量必须原样落在代码注释里，它们是 Order 数字存在的理由：
- `Monitor` 早于 `Access/Billing/RateLimit`，否则 403/429 不计入监控（AGENTS.md §5）。
- `IPBlock` 晚于 `CORS`，否则预检请求被拉黑 IP 拦死（现 `app.go:346` 注释）。
- `APIAuth` 早于 `Monitor`，否则监控明细拿不到 `username`（`baseURLCheck` 依赖 `user_id` 查用户配置，同理晚于 `APIAuth`）。

## 迁移映射

| 现状 | 去处 | 取用/依赖 | 备注 |
|---|---|---|---|
| `app/middleware.go:196` `requestIDMiddleware` | `transport/requestid.go` | `base.RequestIDKey` | `generateRequestID`/`randomString` 一并迁入，仍包内私有 |
| `:21` `recoveryMiddleware` | `transport/recovery.go` | `conf.ErrorCode` | |
| `:37` `loggingMiddleware` | `transport/logging.go` | | |
| `:99` `corsMiddleware` | `transport/cors.go` | | CORS=\\* 的现状与注释保留 |
| `:187` `cacheControlMiddleware` | `transport/cachecontrol.go` | | |
| `:85` `ipBlockMiddleware` | `ipblock/ipblock.go` | `db.IsIPBlocked` | Scope=Global, Order 60 |
| `app/autoblock.go`（整文件） | `ipblock/autoblock.go` | `db.GetSetting`/`models` | 导出名 `RecordAutoBlockFailure` → `ipblock.RecordFailure`；`autoBlockState` 随文件走，仍包内私有 |
| `app/middleware.go:59` `authMiddleware` | `apiauth/auth.go` | `conf.AuthEnabled`/`utils.VerifyAuth` | Scope=Route, Order 100, `Applies: AuthRequired` |
| `:116` `baseURLCheckMiddleware` | `source/baseurl.go` | `base.GetSourceMeta`/`db`/`models`/`utils.IsSafeURL` | Order 300；写 `middleware.CtxResolvedBaseURL` |
| `:174` `monitorMiddleware` | `source/monitor.go` | `base.RecordCall` + `ipblock.RecordFailure` | Order 200，`Applies: spec.Source != ""`（等价现 `app.go:201`） |
| `gate/middleware.go:232` `BillingMiddleware` | `gate/billing.go` | 现 `billingEnabled()`/`captureWriter`/`getCachedCost` 同迁 | Order 500；`Applies: spec.Source != ""` |
| `:327` `DataSourceAccessMiddleware` | `gate/access.go` | `isDataSourceInFreePlan`/`userHasDataSourceAccess` 同迁 | Order 400；`""/datasources/data/panel` 的跳过逻辑留在函数体内，不改判 |
| `:756/763` `RateLimitMiddleware` + 限流器状态机 | `gate/ratelimit.go`（含 `rateLimiter`/`windowLimiter`/`allowRequest`/`tooFrequent`） | `limiterJanitorOnce` 等包级状态随文件走 | Order 600；`LimiterIdleExpiredForTest` 保持 `gate` 导出（`test/quota_integration_test.go:566` 在用） |
| `:88-165` 限额与套餐解析 + `:40-86` 用量统计 | `gate/plan.go` + `gate/usage.go` | | 非中间件，只是同文件里的邻居，拆出来让「一文件一职责」成立 |

`app/monitor.go`（落库链路）、`app/pprof.go`、`app/autoblock.go` 的路由侧无、`app/app.go` 的 `handleError`/`buildParams`/`makeEndpoint`/SPA 静态——**都不动**：它们是 handler 调用与持久化，不是中间件。

每个新文件顶部一行包内注释说明「本文件注册哪个 Def、Order 多少、为什么必须在这个位置」，顺序理由不再散落在 `app.go` 的行间注释里。

## `app/app.go` 改动（收敛到 3 处）

```go
// 导入：删去 "loomproxy/gate" 之外的中间件相关逐行，新增
//   "loomproxy/middleware"
//   _ "loomproxy/middleware/all"     // 本部署装载哪些中间件 = middleware/all/all.go
// gate 仍保留真实导入：app.go:405 用到 gate.ResolvePlanForUser

// :341-347 → 
for _, m := range middleware.Globals() { r.Use(m) }
log.Println("中间件链:", middleware.Describe())

// :191-209 →
spec := middleware.Spec{Source: source, Action: action, HandlerName: info.Handler, AuthRequired: h.AuthRequired()}
handlers := middleware.RouteChain(spec)
handlers = append(handlers, handlerFunc)

// :385 /endpoints →
if mw, ok := middleware.Build("apiauth", middleware.Spec{}); ok { r.GET("/endpoints", mw, ...) }
```

`buildParams`（`app.go:133`）读 `_resolved_baseUrl` 的字面量换成 `middleware.CtxResolvedBaseURL`。

**注意**：`_ "loomproxy/middleware/all"` 与 `_ "loomproxy/sources"` 是两件事——前者决定「带哪些中间件」，后者决定「带哪些数据源」。`middleware/all/all.go` 的包注释按 `sources/all.go` 的口径写清楚：接入新中间件只加一行空白导入 + 新包内一次 `Register`，`app.go` 免改。

## 行为不变性护栏

1. 守护用例（新增 `test/middleware_chain_test.go`）：断言 `middleware.RouteNames(middleware.Spec{Source:"fake_a", Action:"search", AuthRequired:true})` == `["apiauth","monitor","baseurl","access","billing","ratelimit"]`，`Spec{}`（控制面）不含 monitor/billing/ratelimit；`Globals()` 名字序列 == 现 6 项顺序，且 `ipblock` 在 `cors` 之后。这条用例就是「顺序不再有机制保证」的反制。
2. 现有黑盒用例原样通过即等价证据：`test/` 里覆盖 403（访问控制）、429（限流）、扣费、监控明细、自动拉黑、SSRF 400、`baseUrl` 回落链的用例都不改断言。唯一必改处：`test/monitor_integration_test.go:230-312` 的 `app.RecordAutoBlockFailure` → `ipblock.RecordFailure`（6 处 + 导入）。
3. 不引入兼容 shim：`app/middleware.go` 与 `app/autoblock.go` 直接删除，不留转发函数（AGENTS.md §10 约定）。

## 落地顺序

1. `middleware/middleware.go` 注册表 + Order 常量 + ctx key（无消费者，先落地）。
2. `transport` / `ipblock` / `apiauth` / `source` 四包，逐包把函数搬过去并加 `init(){ middleware.Register(...) }`；`app/autoblock.go` 整体并入 `ipblock`。
3. `gate/middleware.go` 按 plan/usage/billing/access/ratelimit 拆 5 文件，三轴各加 `init()` 注册。
4. `middleware/all/all.go` 写空白导入清单（5 行）+ 包注释。
5. 改 `app/app.go` 三处装配，删 `app/middleware.go`、`app/autoblock.go`；改 `test/monitor_integration_test.go`。
6. 加 `test/middleware_chain_test.go` 守护用例。
7. 同步 AGENTS.md：§4 目录结构（新增 `middleware/` 子树 + `gate/` 拆分后形态）、§5 第 4 点的单路由链描述改为「顺序由各中间件 `Def.Order` 声明、`middleware/all.go` 决定装载集合」、§10 补一句新中间件的接入姿势。
8. `make build`（含 vet + gofmt + `go test -race`）后按 §10 提交（含 AGENTS.md 一起提交）。

## 验证

```bash
make vet && make test                        # 编译期即排除导入环；黑盒用例证明行为不变
go build -o /tmp/lgw ./cmd/fakegateway && /tmp/lgw   # 起假源网关（fakegateway 与产品走同一 app.Run）
# 逐项人工确认（顺序敏感项）：
curl -s localhost:8081/endpoints | head -c 200                    # 无鉴权 401（apiauth 生效）
curl -s 'localhost:8081/fake_a/search?baseUrl=http://127.0.0.1:9/x&api_key=$K'  # 400 且文案脱敏（baseurl/SSRF 生效）
curl -s 'localhost:8081/fake_a/search?baseUrl=http://127.0.0.1:9/x' -H 'X-API-Key: no-such'  # 401 优先于 400（auth 早于 baseurl）
curl -sI localhost:8081/panel | grep -E 'X-Request-ID|Cache-Control|Access-Control-Allow'   # transport 三件套生效
# 启动日志应出现「中间件链: …」一行，顺序与 §行为不变性护栏 的期望序列逐字一致
```

前端（`web/`）不受影响：`app.js`/`styles.css` 的工作区改动与本方案无关。

## 已排除的替代方案

- **一个中间件一个包**：9~10 个包承载平均 20 行代码，`transport` 类聚合更自然；接入成本高于收益。
- **只拆文件、同包内不拆包**：`app.go` 仍需逐行拼装顺序（或维护一份显式名单），没解决「顺序无机制保证 + 两处改动」这两个真实痛点。
- **集合清单放 `middleware` 根包**（最贴近 `sources/all.go` 的字面形态）：因导入环不可行，这是本方案与 `sources/all.go` 唯一的形态差异，已在上文说明原因。
- **把注册表塞进 `base`**：`base` 是数据面核心且刻意不依赖 gin（`base/base.go` 只 import `conf`），为中间件引入 gin 会破坏 §8 的「base 保持无 HTTP 依赖」现状。
