package app

// 聚合搜索：一条 /{source}/search 请求带 sources 参数时，把同样的查询打到多个源上，
// 结果合并返回，每条结果自带「它来自哪个源」，下游据此把详情/目录/正文路由回对应源。
//
// 为什么这道逻辑在装配层而不是在某个源里：源只该知道自己的上游协议，而「跨源」这件事
// 需要的是访问控制、额度、baseUrl 回落——全在平台这一侧。
//
// 为什么扇出必须自己把闸门再走一遍：handler 内部调别的源不经过中间件链，
// access/billing/ratelimit 对它是盲的。少补一刀，聚合就是套餐与额度的绕过通道。

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"loomproxy/base"
	"loomproxy/base/legado"
	"loomproxy/conf"
	"loomproxy/gate"
	"loomproxy/middleware"
	mws "loomproxy/middleware/source"
	"loomproxy/models"
	"loomproxy/utils"
)

const (
	// aggregateTargetTimeout 单个目标源的墙钟上限。取这么宽是给真实上游留余量，
	// 但它必须存在：没有上限时一个挂死的源会把整条聚合拖到下游自己超时，
	// 而那时调用方看到的是一句「搜索失败」，不是「某个源没回」。
	aggregateTargetTimeout = 20 * time.Second
	// aggregateConcurrency 是**每个源**同时在飞的上限（默认值；可用
	// `AGGREGATE_PER_SOURCE_MAX` 覆盖）。跨请求共享，见下面 aggregateAcquire 那段。
	aggregateConcurrency = 4
)

// aggregateStatus 是下发给下游的成因枚举（不含任何上游文案，见 legado.SearchResponse 的注释）
const (
	aggOK              = "ok"
	aggUngranted       = "ungranted"
	aggNeedLogin       = "need_login"
	aggNoPlan          = "no_plan"
	aggDisabled        = "disabled"
	aggNotFound        = "not_found"
	aggLimitExceeded   = "limit_exceeded"
	aggMissingParams   = "missing_params"
	aggHandlerMissing  = "handler_missing"
	aggUnsafeBaseURL   = "unsafe_base_url"
	aggUpstreamFailed  = "upstream_failed"
	aggCanceled        = "canceled"         // 客户端在排队阶段就走了：这一源根本没碰上游，不能算 upstream_failed
	aggInternalFailure = "internal_failure" // 源自己 panic：详情只进服务端日志
)

// errAggregatePanic 目标源 panic 的内部标记：只用于走 upstream_failed 那条成因，
// 它本身绝不出现在响应里
var errAggregatePanic = errors.New("聚合目标源 panic")

// errAggregateAllFailed 所有目标源都没回东西、且入口源的错误又不在手上时的兜底。
// 文案必须脱敏口径内：不列源名以外的细节，成因在 sources_status 与服务端日志里。
var errAggregateAllFailed = errors.New("聚合搜索的全部目标源均未返回结果")

// aggregateAccessReason 把 gate 的访问判定翻成下发枚举（一一对应，不做二次判断）
func aggregateAccessReason(r gate.AccessReason) string {
	switch r {
	case gate.AccessNotFound:
		return aggNotFound
	case gate.AccessDisabled:
		return aggDisabled
	case gate.AccessNeedLogin:
		return aggNeedLogin
	case gate.AccessNoPlan:
		return aggNoPlan
	case gate.AccessUngranted:
		return aggUngranted
	}
	return ""
}

// runAggregateSearch 执行一次聚合搜索。返回的是已按源打标并合并好的响应体。
//
// 返回 error 只留给「这件事本身没做成」的情形；单个目标源失败不算 error——
// 一个源坏了不该让其余源的结果一起丢掉，那正是聚合存在的理由。
func runAggregateSearch(c *gin.Context, routeSource, action string, routeParams map[string]interface{}) (interface{}, error) {
	targets := legado.AggregateTargets(routeSource, c.Query(legado.AggregateSourcesParam))
	if len(targets) == 0 {
		targets = []string{routeSource}
	}

	var caller *models.User
	if raw, ok := c.Get(middleware.CtxCurrentUser); ok {
		if u, isUser := raw.(*models.User); isUser {
			caller = u
		}
	}
	var uid uint
	if caller != nil {
		uid = caller.ID
	}
	query := c.Request.URL.Query()

	statuses := make([]string, len(targets))
	results := make([]interface{}, len(targets))
	targetErrs := make([]error, len(targets))
	var wg sync.WaitGroup

	for i, name := range targets {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			// 闸是**按源、跨请求**的（待办清单 P110 的①）：原来这道 `sem` 建在每个请求里，
			// 挡得住"一个请求同时打 8 个源"，挡不住"N 个用户各发一个聚合请求打同一个源"——
			// 而旁边那句注释写的威胁本来就是跨请求的，注释与实现差了整整一层。
			// 排队而非拒绝：聚合的语义就是"能回多少回多少"；但客户端已经走了就别替它等。
			release, aerr := aggregateAcquire(c.Request.Context(), name)
			if aerr != nil {
				statuses[i] = aggCanceled
				targetErrs[i] = aerr
				return
			}
			defer release()

			res, status, callErr := runAggregateTarget(c, name, action, uid, caller, query, routeParams, routeSource == name)
			statuses[i] = status
			targetErrs[i] = callErr
			if res != nil {
				results[i] = res
			}
		}(i, name)
	}
	wg.Wait()

	merged := make([]interface{}, 0, len(targets))
	statusMap := make(map[string]string, len(targets))
	for i, name := range targets {
		statusMap[name] = statuses[i]
		if statuses[i] == aggOK && results[i] != nil {
			merged = append(merged, results[i])
		}
	}
	if len(merged) == len(targets) || len(merged) > 0 {
		if len(merged) < len(targets) {
			log.Printf("聚合搜索部分目标未取到结果: route=%s action=%s 明细=%v", routeSource, action, statusMap)
		}
		return legado.MergeSearchResults(merged, statusMap), nil
	}

	// 一条结果都没有：不能返回「200 + 空列表」。空列表在下游与监控里的读数是「这次没搜到」，
	// 而真实情况是「上游都坏了」——把失败说成没结果，成功率与覆盖率两头一起说谎，
	// 单源聚合（等价于直连）还会因此与直连的状态码不一致。
	// 优先把**入口源**的错误原样抛出去：它失败时聚合必须与直连同形。
	log.Printf("聚合搜索全部目标未返回结果: route=%s action=%s 明细=%v", routeSource, action, statusMap)
	for i, name := range targets {
		if targetErrs[i] != nil && name == routeSource {
			return nil, targetErrs[i]
		}
	}
	for i := range targets {
		if targetErrs[i] != nil {
			return nil, errAggregateAllFailed
		}
	}
	return legado.MergeSearchResults(merged, statusMap), nil
}

// runAggregateTarget 判定 + 调用 + 打标一个目标源。返回 (结果, 成因)。
func runAggregateTarget(c *gin.Context, name, action string, uid uint, caller *models.User,
	query map[string][]string, routeParams map[string]interface{}, isRouteSource bool) (interface{}, string, error) {

	// 路由源自己**不重判也不重扣**：链上的 access/billing 已经为它做过一遍，
	// 再来一次就是同一件事扣两次额度——聚合的账要算到「多出来的那些源」上，
	// 不是把入口这个源算两遍
	cost := int64(0)
	if !isRouteSource {
		// 1) 访问判定：与中间件同一个函数（扇出不经过链，不在这里判就是绕过套餐）
		if reason := aggregateAccessReason(gate.SourceAccessByName(name, caller)); reason != "" {
			return nil, reason, nil
		}
		// 2) 额度判定：与计费中间件同一套原语，见 gate.AggregateTargetVerdict
		//    （第二个返回值原来写成 `allowed, c, reason :=`，于是这个块里 `c` 从 *gin.Context
		//     悄悄变成 int64；下面那句 `DeductAggregateTarget(c, ...)` 之所以还对，靠的是"它在块外"
		//     这个作用域细节而不是名字。待办清单 P89）
		allowed, targetCost, reason := gate.AggregateTargetVerdict(caller, name, action)
		if !allowed {
			return nil, reason, nil
		}
		cost = targetCost
	}

	// 3) 该源的处理器与它的必填参数（声明位来自注册表，不靠约定）
	handler, err := base.Get(name + "_" + action)
	if err != nil {
		return nil, aggHandlerMissing, nil
	}
	params := make(map[string]interface{}, len(query)+2)
	for _, p := range handler.GetQueryParams() {
		if vals, ok := query[p]; ok && len(vals) > 0 && vals[0] != "" {
			params[p] = vals[0]
			continue
		}
		if p != "baseUrl" {
			continue
		}
		// baseUrl 的三种来路：路由源沿用 baseurl 中间件已经解析好的那一份
		// （含请求显式带的那个），其余源各解析各的用户/平台配置——
		// 用户给某个源配了独立站点地址时，扇出不能装看不见
		if isRouteSource {
			if v, has := routeParams["baseUrl"]; has {
				params["baseUrl"] = v
			}
			continue
		}
		if _, has := params["baseUrl"]; has {
			continue
		}
		resolved, fromPlatform := mws.ResolveBaseURL(name, uid)
		if resolved == "" {
			continue
		}
		// 平台默认配置由管理员设、视为可信；用户配置仍要过 SSRF（与中间件同一口径）
		if !fromPlatform && !utils.IsSafeURL(resolved) {
			return nil, aggUnsafeBaseURL, nil
		}
		params["baseUrl"] = resolved
	}
	for _, p := range base.RequiredParamsFor(name, action) {
		if v, ok := params[p]; !ok || v == "" {
			return nil, aggMissingParams, nil
		}
	}
	if uid > 0 {
		params["__uid"] = uid
	}

	// 4) 调用：独立超时 + 兜住 panic。一个源挂死或崩掉不该把整条聚合带走
	ctx, cancel := context.WithTimeout(c.Request.Context(), aggregateTargetTimeout)
	defer cancel()
	result, callErr := callSourceHandler(ctx, handler, params)
	if callErr != nil {
		log.Printf("聚合搜索目标源失败: source=%s action=%s err=%s", name, action, scrubQueryForLog(callErr.Error()))
		if errors.Is(callErr, errAggregatePanic) {
			return nil, aggInternalFailure, callErr // 源自己崩了：与「上游没回」分开报，两者排查方向不同
		}
		return nil, aggUpstreamFailed, callErr
	}

	// 5) 扇出目标各扣一次（与中间件同形状的流水行），再按源打标。
	// 不扣这一笔，「一次请求换 N 个上游」就是额度上的空头支票
	if !isRouteSource {
		gate.DeductAggregateTarget(c, caller, name, action, cost)
	}
	return legado.StampSearchSource(result, name), aggOK, nil
}

// callSourceHandler 包住一次源调用：源实现 panic 时只废掉这一个目标，
// 而不是让整条聚合请求以 500 收场（那会让「六个源里有一个坏了」显示成「网关坏了」）
func callSourceHandler(ctx context.Context, handler base.Handler, params map[string]interface{}) (result interface{}, err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("聚合搜索目标源 panic: %s", scrubQueryForLog(fmt.Sprint(r)))
			result, err = nil, errAggregatePanic
		}
	}()
	return handler.Handle(ctx, params)
}

// ---------------------------------------------------------------------------
// 按源的在飞闸门（跨请求共享）
// ---------------------------------------------------------------------------

var (
	aggregateGateMu    sync.Mutex
	aggregateGateCache = map[string]chan struct{}{}
)

// aggregateConcurrencyNow 每次取闸门时读一遍上限，改配置后新出现的源就用新值；
// 已经建好的闸门不重建（重建等于把在飞的那几发放回自由）。
func aggregateConcurrencyNow() int {
	if conf.Config != nil && conf.Config.AggregatePerSourceMax > 0 {
		return conf.Config.AggregatePerSourceMax
	}
	return aggregateConcurrency
}

func aggregateGate(source string) chan struct{} {
	aggregateGateMu.Lock()
	defer aggregateGateMu.Unlock()
	if g, ok := aggregateGateCache[source]; ok {
		return g
	}
	g := make(chan struct{}, aggregateConcurrencyNow())
	aggregateGateCache[source] = g
	return g
}

// aggregateAcquire 取一枚该源的在飞许可；ctx 取消立刻回错误，不替已走的客户端排队。
func aggregateAcquire(ctx context.Context, source string) (func(), error) {
	g := aggregateGate(source)
	select {
	case g <- struct{}{}:
		return func() { <-g }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
