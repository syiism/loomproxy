package source

// reqparams 注册 Def：Name="reqparams"，Scope=Route，Order=350。
//
// 位置是这条校验成立的前提，三个方向都有理由：
//   - **晚于 monitor(200)**：被它挡下的 400 要进调用明细与覆盖率表，否则「客户端在拿空参数打我」
//     这件事又变成看不见的（P22 的起因就是这类失败在明细里长得像成功）；
//   - **晚于 baseurl(300)**：`baseUrl` 不是请求带来的，而是 baseurl 中间件解析后放在 context 上的，
//     校验必须在它之后，否则声明了 `baseUrl` 必填的源会被判成永远缺参；
//   - **早于 access(400)/billing(500)**：形态错误的请求连 handler 都不该进，更不该被套餐访问判定
//     与计费走一遍——`gate/billing.go` 的扣减判据是 `status == 200`，挡在这里等于顺手免掉了这次扣额。

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"loomproxy/base"
	"loomproxy/conf"
	"loomproxy/middleware"
)

func init() {
	middleware.Register(middleware.Def{
		Name:  "reqparams",
		Scope: middleware.Route,
		Order: middleware.OrderRequiredParams,
		// 只管声明了必填参数的 源×动作：没声明的一律直通，行为与加这条之前完全一致
		Applies: func(s middleware.Spec) bool {
			return s.Source != "" && len(base.RequiredParamsFor(s.Source, s.Action)) > 0
		},
		Build: func(s middleware.Spec) gin.HandlerFunc {
			return requiredParamsMiddleware(s.Source, s.Action)
		},
	})
}

// requiredParamsMiddleware 按源的 RequiredParams 声明校验必填请求参数，缺任何一个直接 400。
//
// 为什么不留在各源自己判：源的写法是返回 `ContentResponse{ContentType:"error", Data:{message:...}}`
// 且 error 为 nil（Legado 书源传统），于是下游阅读器把「缺少参数」那句话**当正文渲染**，
// 而监控看到的是一条 200、内容维度全空的「成功」（待办清单 P22 / 分支侧 S15）。
// 这里的判据与 handler 读参数同源（`buildParams` 同样取 query 参数），所以挡下的请求，
// 源里那一支必然进不去；留下的那支也就不必再伪造成正文。
func requiredParamsMiddleware(source, action string) gin.HandlerFunc {
	required := base.RequiredParamsFor(source, action)
	return func(c *gin.Context) {
		for _, name := range required {
			if paramPresent(c, name) {
				continue
			}
			// 只说缺了什么，不说上游与内部形态（对下游错误文案要脱敏，见开发约定）
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"code": conf.Config.ErrorCode,
				"msg":  "缺少必填参数: " + name,
			})
			return
		}
		c.Next()
	}
}

// paramPresent 参数是否带上了：普通参数看 query（**纯空白算没带**——源侧普遍是
// `strings.TrimSpace(GetParam(...))` 后判空，这里不跟着 trim 的话 `" "` 会溜过校验、
// 而源里那一支已被删掉，结果是拿一个空白 id 去拼上游 URL）；
// `baseUrl` 由 baseurl 中间件解析后放 context。
func paramPresent(c *gin.Context, name string) bool {
	if strings.TrimSpace(c.Query(name)) != "" {
		return true
	}
	if name == "baseUrl" {
		v, ok := c.Get(middleware.CtxResolvedBaseURL)
		s, _ := v.(string)
		return ok && strings.TrimSpace(s) != ""
	}
	return false
}
