// Package middleware 中间件注册表与链装配：请求链的顺序与装载集合的唯一事实来源。
//
// 本包必须是叶子包——只依赖 gin，**不得导入任何子包（transport / ipblock / apiauth /
// source / all），也不得导入 gate**。子包 import 本包调用 Register 自注册，
// middleware/all 提供空白导入清单，app 只消费 Globals 与 RouteChain。
// 根包一旦导入子包即形成 import 环（Go 不允许，空白导入也不行），这就是
// 「装载清单」放在 middleware/all 而不是包根本地的原因（与 sources/all.go 的唯一形态差异）。
//
// 顺序由 Def.Order 显式声明（本包集中定义常量），装配时 sort.SliceStable 排序，
// **不依赖 init() 的执行顺序**——init 顺序由导入图决定，跨包不可读，
// 一次 import 重排就能改掉链顺序而无人审查。
package middleware

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

// Scope 挂载位置
type Scope uint8

const (
	Global Scope = iota // 引擎级，r.Use 一次
	Route               // 单路由级，随 Spec 的 source/action 参数化构建
)

// 链顺序常量：唯一事实来源。留 100 的间隙给后续插入，改顺序 = 改这一个数字。
//
// 三条隐性不变量（历史上靠 app.go 的行间注释维持，现由 Order 数字表达）：
//   - Monitor(200) 早于 Access(400)/Billing(500)/RateLimit(600)，否则 403/429 不计入监控明细；
//   - IPBlock(60) 晚于 CORS(40)，否则预检请求被拉黑 IP 拦死；
//   - APIAuth(100) 早于 Monitor(200) 与 BaseURLCheck(300)：监控明细要 username，
//     baseUrl 的用户个人配置回落要 user_id；
//   - RequiredParams(350) 晚于 BaseURLCheck(300)、早于 Access(400)：`baseUrl` 是解析出来
//     放在 context 上的，校验得在它之后；而形态错误（缺必填参数）的请求不该再被套餐访问判定
//     与计费走一遍——它连 handler 都不该进。
const (
	OrderRequestID    = 10
	OrderRecovery     = 20
	OrderLogging      = 30
	OrderCORS         = 40
	OrderCacheControl = 50
	OrderIPBlock      = 60

	OrderAPIAuth      = 100
	OrderMonitor      = 200
	OrderBaseURLCheck = 300
	// OrderRequiredParams 按源的 RequiredParams 声明校验必填请求参数（缺失直接 400）
	OrderRequiredParams = 350
	OrderAccess         = 400
	OrderBilling        = 500
	OrderRateLimit      = 600
)

// 跨包共享的 gin 上下文键（原为 app.go 与 middleware.go 之间的裸字符串契约）
const (
	CtxResolvedBaseURL = "_resolved_baseUrl" // source/baseurl 解析出、app.buildParams 取用
	CtxRequestID       = "request_id"        // transport/requestid 写入，响应头同名
	// CtxCallSubject 本次调用的内容维度载体（*base.CallSubject）：app 在进 handler 前挂上，
	// handler 返回后由 legado.ObserveCall 回填，source/monitor 在 c.Next() 之后读取。
	// 归属本包而不是 base：base 不依赖 gin，跨包契约键与上面两个保持一致。
	CtxCallSubject = "call_subject"
)

// Spec 描述一条待挂载路由，供 Applies 判定与 Build 取参。
type Spec struct {
	Source       string // 数据源码；控制面端点（/datasources、/data、/auth/*）为空
	Action       string
	HandlerName  string // base.Handler.GetName()，data_files 一类特例用
	AuthRequired bool   // 取自 Handler.AuthRequired()
}

// Def 一条中间件的注册项。
type Def struct {
	Name    string
	Scope   Scope
	Order   int
	Applies func(Spec) bool // 仅 Route 作用域；nil = 全量生效
	Build   func(Spec) gin.HandlerFunc
}

var (
	registerMu sync.RWMutex
	registered []Def
)

// Register 在 init() 期登记一条中间件。重名或同作用域内 Order 冲突直接 log.Fatalf：
// 链顺序是安全属性（鉴权早于计费、监控早于拦截），带病装配不如启动失败。
func Register(d Def) {
	if d.Name == "" || d.Build == nil {
		log.Fatalf("middleware: 注册项非法（Name 与 Build 必填）")
	}
	registerMu.Lock()
	defer registerMu.Unlock()
	for _, e := range registered {
		if e.Name == d.Name {
			log.Fatalf("middleware: 重复注册 %q", d.Name)
		}
		if e.Scope == d.Scope && e.Order == d.Order {
			log.Fatalf("middleware: %q 与 %q 在同作用域争用 Order %d", e.Name, d.Name, d.Order)
		}
	}
	registered = append(registered, d)
}

// sortedBy 返回某作用域内按 Order 升序的注册项（stable：不依赖 init 顺序，
// 同 Order 已在 Register 期拒绝，此处不存在歧义）。
func sortedBy(scope Scope) []Def {
	registerMu.RLock()
	defer registerMu.RUnlock()
	out := make([]Def, 0, len(registered))
	for _, d := range registered {
		if d.Scope == scope {
			out = append(out, d)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Order < out[j].Order })
	return out
}

func applies(d Def, spec Spec) bool {
	return d.Applies == nil || d.Applies(spec)
}

// Globals 引擎级中间件链，按 Order 升序。
func Globals() []gin.HandlerFunc {
	defs := sortedBy(Global)
	out := make([]gin.HandlerFunc, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Build(Spec{}))
	}
	return out
}

// GlobalNames 全局链的名字序列，供守护用例断言顺序与排障日志。
func GlobalNames() []string {
	defs := sortedBy(Global)
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Name)
	}
	return out
}

// RouteChain 单路由中间件链：先按 Applies 过滤，再按 Order 升序。
func RouteChain(spec Spec) []gin.HandlerFunc {
	defs := sortedBy(Route)
	out := make([]gin.HandlerFunc, 0, len(defs))
	for _, d := range defs {
		if applies(d, spec) {
			out = append(out, d.Build(spec))
		}
	}
	return out
}

// RouteNames 同 RouteChain，但只出名字序列——供守护用例断言顺序、以及排障日志。
func RouteNames(spec Spec) []string {
	defs := sortedBy(Route)
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		if applies(d, spec) {
			out = append(out, d.Name)
		}
	}
	return out
}

// Build 按名字取单条中间件，供 /endpoints 这类只挂鉴权、不走完整链的路由。
func Build(name string, spec Spec) (gin.HandlerFunc, bool) {
	registerMu.RLock()
	defer registerMu.RUnlock()
	for _, d := range registered {
		if d.Name == name {
			return d.Build(spec), true
		}
	}
	return nil, false
}

// Describe 启动时一行 INFO：本部署装载了哪些中间件、什么顺序。
// 路由链这里是「注册全集」；某条具体路由的实际链由 Applies 决定，用 RouteNames 查。
func Describe() string {
	globals := sortedBy(Global)
	gnames := make([]string, 0, len(globals))
	for _, d := range globals {
		gnames = append(gnames, d.Name)
	}
	routes := sortedBy(Route)
	rnames := make([]string, 0, len(routes))
	for _, d := range routes {
		rnames = append(rnames, fmt.Sprintf("%s(%d)", d.Name, d.Order))
	}
	return "全局 " + strings.Join(gnames, "→") + " | 路由 " + strings.Join(rnames, "→")
}
