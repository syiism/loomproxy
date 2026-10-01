package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"loomproxy/base"
	"loomproxy/base/legado"
	"loomproxy/base/pool"
	"loomproxy/conf"
	"loomproxy/db"
	"loomproxy/gate"
	"loomproxy/handlers/admin"
	"loomproxy/handlers/apikey"
	"loomproxy/handlers/auth"
	_ "loomproxy/handlers/catalog"
	"loomproxy/handlers/quota"
	"loomproxy/handlers/rank"
	"loomproxy/handlers/userconfig"
	"loomproxy/handlers/verify"
	"loomproxy/lifecycle"
	"loomproxy/middleware"
	_ "loomproxy/middleware/all" // 本部署装载哪些中间件 = middleware/all/all.go
	"loomproxy/models"
	_ "loomproxy/sources"
	"loomproxy/utils"
	"loomproxy/web"
)

type RouteInfo struct {
	Path        string   `json:"path"`
	Description string   `json:"description"`
	Methods     []string `json:"methods"`
	Params      []string `json:"params"`
	Handler     string   `json:"handler"`
}

type RouteEntry struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Endpoints []EndpointInfo `json:"endpoints"`
}

type EndpointInfo struct {
	Path        string   `json:"path"`
	Method      string   `json:"method"`
	Description string   `json:"description"`
	Params      []string `json:"params"`
	Auth        bool     `json:"auth_required"`
}

// validateRoutes 路由对账：以各源包 base.RegisterSource 声明的动作集为期望，
// 校验挂载路由齐全（app 不再硬编码按源清单；缺失仅告警不阻断，与原语义一致）。
func validateRoutes(registered []RouteInfo) error {
	registeredPaths := make(map[string]bool, len(registered))
	for _, r := range registered {
		registeredPaths[r.Path] = true
	}
	for _, src := range base.DeclaredSources() {
		for _, action := range src.Actions {
			expected := fmt.Sprintf("/%s/%s", src.Code, action)
			if !registeredPaths[expected] {
				log.Printf("WARNING: 声明路由 %s 未挂载（数据源 %s 的 %s 接口缺失）", expected, src.Code, action)
			}
		}
	}
	return nil
}

// spaFileSystem 支持 SPA 路由的 http.FileSystem：静态文件优先，找不到回退 index.html
type spaFileSystem struct {
	http.FileSystem
}

func (fs spaFileSystem) Open(name string) (http.File, error) {
	f, err := fs.FileSystem.Open(name)
	if err == nil {
		return f, nil
	}
	// 文件不存在时返回 index.html（SPA 回退）
	return fs.FileSystem.Open("index.html")
}

func makeEndpoint(handler base.Handler) RouteInfo {
	methods := handler.GetMethods()
	if len(methods) == 0 {
		methods = []string{"GET"}
	}
	params := handler.GetQueryParams()

	return RouteInfo{
		Path:        handler.GetPath(),
		Description: handler.GetDescription(),
		Methods:     methods,
		Params:      params,
		Handler:     handler.GetName(),
	}
}

// paramsPool 请求参数 map 池，减少每请求的 map 分配。\n// 注意：handler 不得在 Handle 返回后继续持有 params 引用（现有实现均满足）。
var paramsPool = sync.Pool{
	New: func() interface{} {
		return make(map[string]interface{}, 8)
	},
}

func acquireParams() map[string]interface{} {
	return paramsPool.Get().(map[string]interface{})
}

func releaseParams(params map[string]interface{}) {
	for k := range params {
		delete(params, k)
	}
	paramsPool.Put(params)
}

func buildParams(c *gin.Context, paramNames []string) map[string]interface{} {
	params := acquireParams()
	for _, p := range paramNames {
		if val := c.Query(p); val != "" {
			params[p] = val
		} else if p == "baseUrl" {
			// 从中间件注入的用户配置中读取
			if v, ok := c.Get(middleware.CtxResolvedBaseURL); ok {
				if s, ok := v.(string); ok && s != "" {
					params[p] = s
				}
			}
		}
	}
	return params
}

func registerHandlers(r *gin.Engine) []RouteInfo {
	var registered []RouteInfo
	allHandlers := base.All()

	for _, handler := range allHandlers {
		info := makeEndpoint(handler)
		h := handler

		// 源名与动作：路径形如 /{source}/{action}
		source, action := "", ""
		if parts := strings.SplitN(strings.TrimPrefix(info.Path, "/"), "/", 2); len(parts) == 2 {
			source, action = parts[0], parts[1]
		}

		handlerFunc := func(c *gin.Context) {
			// 内容维度载体：进 handler 前挂上（monitor 中间件在 c.Next() 之后才读它），
			// handler 返回后由 legado.ObserveCall 按标准信封回填
			subject := &base.CallSubject{}
			c.Set(middleware.CtxCallSubject, subject)

			params := buildParams(c, h.GetQueryParams())
			if uid, exists := c.Get("user_id"); exists {
				if id, ok := uid.(uint); ok {
					params["__uid"] = id
				}
			}
			defer releaseParams(params)

			// data_files handler 从路径提取 source/name 参数
			if h.GetName() == "data_files" {
				dataSource := c.Param("source")
				name := c.Param("name")
				if dataSource != "" {
					parts := []string{dataSource}
					if name != "" {
						parts = append(parts, name)
					}
					params["_datafile_parts"] = parts
				}
			}

			ctx := c.Request.Context()
			result, err := h.Handle(ctx, params)
			if err != nil {
				handleError(c, err)
				return
			}
			// data_files handler 直接返回文件内容时跳过 JSON 封装
			if handler.GetName() == "data_files" {
				if data, ok := result.([]byte); ok {
					c.Header("Content-Type", "application/json; charset=utf-8")
					c.Writer.Write(data)
					c.Abort()
					return
				}
			}
			// 控制面端点（source 为空）没有内容维度可抽
			if source != "" {
				legado.ObserveCall(source, params, result, subject)
			}
			c.JSON(http.StatusOK, result)
		}

		// 单路由链：成员与顺序全部来自各中间件包的 Def（Applies 过滤 + Order 排序），
		// 这里不再逐行 append——加中间件不碰本文件，见 middleware/all/all.go
		spec := middleware.Spec{
			Source:       source,
			Action:       action,
			HandlerName:  info.Handler,
			AuthRequired: h.AuthRequired(),
		}
		handlers := append(middleware.RouteChain(spec), handlerFunc)

		for _, method := range info.Methods {
			switch strings.ToUpper(method) {
			case "GET":
				// data_files 路由特殊处理：先注册精确匹配 /data，再注册参数路由
				if info.Handler == "data_files" {
					r.GET("/data", handlers...)
					r.GET("/data/:source", handlers...)
					r.GET("/data/:source/:name", handlers...)
				} else {
					r.GET(info.Path, handlers...)
				}
			case "POST":
				r.POST(info.Path, handlers...)
			case "PUT":
				r.PUT(info.Path, handlers...)
			case "DELETE":
				r.DELETE(info.Path, handlers...)
			case "PATCH":
				r.PATCH(info.Path, handlers...)
			default:
				r.GET(info.Path, handlers...)
			}
		}
		registered = append(registered, info)
	}
	return registered
}

func handleError(c *gin.Context, err error) {
	// 客户端主动取消连接属正常现象，不记错误日志
	if errors.Is(err, context.Canceled) {
		return
	}

	log.Printf("ERROR: %v", err)

	if ue, ok := base.IsUpstreamError(err); ok {
		switch ue.StatusCode {
		case http.StatusGatewayTimeout, 0:
			if ue.StatusCode == 0 && errors.Is(err, context.DeadlineExceeded) {
				c.JSON(http.StatusGatewayTimeout, gin.H{
					"code": conf.Config.ErrorCode,
					"msg":  "上游请求超时",
				})
				return
			}
			if ue.StatusCode == 0 {
				// 上游响应读取/解析失败等未知上游异常，属服务端网关问题而非客户端参数错误
				c.JSON(http.StatusBadGateway, gin.H{
					"code": conf.Config.ErrorCode,
					"msg":  sanitizeUpstreamMsg(ue.Message),
				})
				return
			}
			if ue.StatusCode != 0 {
				c.JSON(ue.StatusCode, gin.H{
					"code": conf.Config.ErrorCode,
					"msg":  sanitizeUpstreamMsg(ue.Message),
				})
				return
			}
		default:
			c.JSON(ue.StatusCode, gin.H{
				"code": conf.Config.ErrorCode,
				"msg":  sanitizeUpstreamMsg(ue.Message),
			})
			return
		}
	}

	if errors.Is(err, context.DeadlineExceeded) {
		c.JSON(http.StatusGatewayTimeout, gin.H{
			"code": conf.Config.ErrorCode,
			"msg":  "上游请求超时",
		})
		return
	}

	if errors.Is(err, base.ErrUnsafeBaseURL) {
		c.JSON(http.StatusBadRequest, gin.H{
			"code": conf.Config.ErrorCode,
			"msg":  err.Error(),
		})
		return
	}

	if dnsErr, ok := isDNSError(err); ok {
		c.JSON(http.StatusBadGateway, gin.H{
			"code": conf.Config.ErrorCode,
			"msg":  dnsErr,
		})
		return
	}

	// 传输层失败（连接被重置、TLS 握手失败、读超时等）：err.Error() 必带完整请求 URL，
	// 且属网关侧问题——不能落到下面「400 + 原文」的兜底，那是给参数缺失类错误用的
	if isTransportError(err) {
		c.JSON(http.StatusBadGateway, gin.H{
			"code": conf.Config.ErrorCode,
			"msg":  "上游请求失败，请稍后重试",
		})
		return
	}

	c.JSON(http.StatusBadRequest, gin.H{
		"code": conf.Config.ErrorCode,
		"msg":  sanitizeUpstreamMsg(err.Error()),
	})
}

// sanitizeUpstreamMsg 对外文案脱敏：上游错误信息常内嵌完整请求 URL（签名站会带上
// app_key/device_sn/sig 一类参数，也暴露自家上游域名），出现 URL 一律换成通用提示。
// 完整错误已由 handleError 记入服务端日志，这里只影响下游看到什么。
func sanitizeUpstreamMsg(msg string) string {
	if strings.Contains(msg, "://") {
		return "上游请求失败，请稍后重试"
	}
	return msg
}

// isTransportError 判定是否为上游传输层失败。*url.Error 是 http.Client 所有请求失败的
// 包装形态，net.Error 兜住未经 client 的场景（如自持连接）。DNS 类已在上一步给出
// 更具体的友好文案，故此处不再区分。
func isTransportError(err error) bool {
	var ue *url.Error
	if errors.As(err, &ue) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne)
}

func isDNSError(err error) (string, bool) {
	errStr := err.Error()
	if strings.Contains(errStr, "no such host") {
		return "上游地址不可达", true
	}
	if strings.Contains(errStr, "connection refused") {
		return "上游连接被拒绝", true
	}
	return "", false
}

func CreateApp() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	if conf.Config.ServerLogLevel == "debug" {
		gin.SetMode(gin.DebugMode)
	}

	if err := db.Init(); err != nil {
		log.Printf("WARNING: Database init failed: %v", err)
	}

	r := gin.New()

	// 只信任本机反向代理（nginx）：X-Forwarded-For 仅在直连方是可信代理时才用于还原客户端
	// IP。gin 默认信任所有代理，会取 XFF 链最左段——流量未套 CDN 时该段可被客户端任意伪造
	// （实测伪造 XFF 后日志/监控/会话记录的全是假 IP），黑名单、限流、自动拉黑随之失效。
	if err := r.SetTrustedProxies([]string{"127.0.0.1", "::1"}); err != nil {
		log.Printf("WARNING: 设置可信代理失败: %v", err)
	}

	for _, m := range middleware.Globals() {
		r.Use(m)
	}
	log.Println("中间件链:", middleware.Describe())

	auth.RegisterRoutes(r)
	verify.RegisterRoutes(r)
	admin.RegisterRoutes(r)
	quota.RegisterRoutes(r)
	userconfig.RegisterRoutes(r)
	apikey.RegisterRoutes(r)
	rank.RegisterRoutes(r)
	registerPprofRoutes(r)

	registeredRoutes := registerHandlers(r)
	validateRoutes(registeredRoutes)

	// Ensure visiting /panel or /panel/ serves the index.html (instead of directory listing).
	r.GET("/panel", func(c *gin.Context) {
		f, err := web.FS.Open("index.html")
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		defer f.Close()
		data, err := io.ReadAll(f)
		if err != nil {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", data)
	})
	// 前端中控面板（嵌入静态资源）
	// SPA 回退文件系统：找不到文件时返回 index.html
	r.StaticFS("/panel", spaFileSystem{http.FS(web.FS)})

	// 未匹配的 API 路由返回 JSON
	r.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, gin.H{"code": 404, "msg": "not found"})
	})

	// 端点列表（需认证，返回用户有权限访问的端点）
	apiAuth, _ := middleware.Build("apiauth", middleware.Spec{})
	r.GET("/endpoints", apiAuth, func(c *gin.Context) {
		uid, exists := c.Get("user_id")
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{"code": 401, "msg": "unauthorized"})
			return
		}
		userID, ok := uid.(uint)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"code": 401, "msg": "unauthorized"})
			return
		}

		// 查询所有启用的数据源
		var dataSources []models.DataSource
		db.DB.Where("status = 1").Order("sort_order ASC, id ASC").Find(&dataSources)

		// 按用户套餐过滤
		if userID > 0 {
			var user models.User
			if err := db.DB.Preload("Plan").First(&user, userID).Error; err == nil {
				plan := gate.ResolvePlanForUser(&user)
				if plan.ID > 0 {
					var planDS []models.QuotaPlanDataSource
					db.DB.Where("plan_id = ?", plan.ID).Find(&planDS)
					allowedMap := make(map[uint]bool)
					for _, p := range planDS {
						allowedMap[p.DataSourceID] = true
					}
					if len(allowedMap) > 0 {
						filtered := make([]models.DataSource, 0, len(dataSources))
						for _, ds := range dataSources {
							if allowedMap[ds.ID] {
								filtered = append(filtered, ds)
							}
						}
						dataSources = filtered
					}
				}
			}
		}

		// 构建端点路由映射
		routeMap := make(map[string]*RouteEntry)
		allHandlers := base.All()
		for _, handler := range allHandlers {
			info := makeEndpoint(handler)
			source := ""
			if parts := strings.SplitN(strings.TrimPrefix(info.Path, "/"), "/", 2); len(parts) == 2 {
				source = parts[0]
			}

			allowed := false
			for _, ds := range dataSources {
				if ds.Name == source {
					allowed = true
					break
				}
			}
			if !allowed {
				continue
			}

			if _, ok := routeMap[source]; !ok {
				routeMap[source] = &RouteEntry{
					ID:        source,
					Name:      source,
					Endpoints: []EndpointInfo{},
				}
			}
			routeMap[source].Endpoints = append(routeMap[source].Endpoints, EndpointInfo{
				Path:        info.Path,
				Method:      info.Methods[0],
				Description: info.Description,
				Params:      info.Params,
				Auth:        handler.AuthRequired(),
			})
		}

		result := []RouteEntry{}
		for _, entry := range routeMap {
			result = append(result, *entry)
		}
		sort.Slice(result, func(i, j int) bool {
			return result[i].ID < result[j].ID
		})

		c.JSON(http.StatusOK, gin.H{
			"code":  0,
			"msg":   "ok",
			"count": len(result),
			"data":  result,
		})
	})

	r.GET("/announcement", func(c *gin.Context) {
		// 站内公告：读取系统设置 announcement（db.GetSetting 带 10s 缓存），空=无公告
		c.JSON(http.StatusOK, gin.H{
			"code": 0,
			"msg":  "ok",
			"data": gin.H{"content": db.GetSetting("announcement")},
		})
	})

	r.GET("/", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"app":    "LoomProxy",
			"status": "ok",
			"panel":  "/panel",
		})
	})

	// 提供同目录下的网站 icon（favicon.ico / icon.png / icon.svg）
	registerIconRoute(r)

	return r
}

func prewarmCache(ctx context.Context, cancel context.CancelFunc) {
	// 数据库未就绪（db.Init 失败仅告警继续运行）时不预热：
	// 下游 handler 会对 nil 的 db.DB 执行查询直接 panic 拖垮进程
	if db.DB == nil {
		log.Println("缓存预热跳过：数据库未连接")
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	deadline := time.After(30 * time.Second)

	for {
		select {
		case <-deadline:
			return
		case <-ticker.C:
			select {
			case <-ctx.Done():
				return
			default:
			}

			if !prefillDatasourcesAnon(ctx) {
				continue
			}
			log.Println("缓存预热完成（只热 /datasources；/data 直读文件不缓存，见待办清单 P16）")
			cancel()
			return
		}
	}
}

func prefillDatasourcesAnon(ctx context.Context) bool {
	apiCfg := &base.APIConfig{
		BaseURL: "",
		Headers: nil,
	}
	h, err := base.GetOrCreate("datasources", apiCfg)
	if err != nil {
		return false
	}

	params := map[string]interface{}{"__uid": uint(0)}
	_, err = h.Handle(ctx, params)
	if err != nil {
		log.Printf("缓存预热 /datasources（匿名）失败: %v", err)
		return false
	}
	return true
}

func registerIconRoute(r *gin.Engine) {
	candidates := []string{"favicon.svg"}
	var iconPath string

	exe, err := os.Executable()
	if err == nil {
		exeDir := filepath.Dir(exe)
		for _, name := range candidates {
			path := filepath.Join(exeDir, name)
			if _, err := os.Stat(path); err == nil {
				iconPath = path
				break
			}
		}
	}

	if iconPath == "" {
		for _, name := range candidates {
			if _, err := os.Stat(name); err == nil {
				iconPath = name
				break
			}
		}
	}

	if iconPath == "" {
		return
	}

	contentType := "image/svg+xml"

	data, err := os.ReadFile(iconPath)
	if err != nil {
		log.Printf("WARNING: failed to read icon %s: %v", iconPath, err)
		return
	}

	r.GET("/favicon.svg", func(c *gin.Context) {
		c.Data(http.StatusOK, contentType, data)
	})
}

// init 数据源声明注入 seed 播种：app 是 base（注册表）与 db 的共同可见方，
// 避免 db→base 反向依赖（同 base.SetProxySourcesGetter 注入模式）。
// 源包的 base.RegisterSource 声明是数据源清单的唯一事实来源。
func init() {
	db.SetSourceSeedProvider(func() []db.SourceSeed {
		metas := base.DeclaredSources()
		seeds := make([]db.SourceSeed, 0, len(metas))
		for _, m := range metas {
			seeds = append(seeds, db.SourceSeed{
				Name: m.Code, DisplayName: m.Display, Category: m.Category,
				Description: m.Description, SortOrder: m.SortOrder, Status: m.Status,
				Actions: m.Actions, LegacyGroups: m.LegacyGroups,
			})
		}
		return seeds
	})
}

func Run(ctx context.Context) error {
	conf.Load()
	r := CreateApp()

	// 监控明细落库：缓冲淘汰批量落库 + 关停兜底落库
	initMonitorPersistence()

	// 「标识 → 名称」命名缓存挂 Redis（可用时）：重启不再清空，正文调用的名称维度不再留空
	initSubjectStore(ctx)

	// 缓存预热：在端口监听前预填充 /datasources 的缓存（/data 不缓存——它每次直读文件，见 P16）
	// 使用 context + cancel 控制预热生命周期，30 秒超时
	prewarmCtx, prewarmCancel := context.WithTimeout(ctx, 30*time.Second)
	go prewarmCache(prewarmCtx, prewarmCancel)

	// 号池启动即初始化（由各数据源经 pool.Register 登记）：提前完成存量号分类与
	// 首个活跃号转正，启动日志即可确认号池状态；POOL_ENABLED=false 时整体跳过
	go pool.StartAll()

	// 动态代理 API 拉取（配置 UPSTREAM_PROXY_API 时启用）
	base.StartProxyAPIRefresher(ctx)

	// 按数据源启用代理：读取系统设置 proxy_enabled_sources（逗号分隔，空=不限制）
	base.SetProxySourcesGetter(func() string { return db.GetSetting("proxy_enabled_sources") })

	addr := fmt.Sprintf("%s:%d", conf.Config.ServerHost, conf.Config.ServerPort)

	srv := &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadTimeout:       10 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1MB
	}
	shutdownDone := make(chan struct{})

	go func() {
		<-ctx.Done()
		log.Println("Shutting down server...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("Server shutdown error: %v", err)
			srv.Close()
		}
		lifecycle.RunCleanups()
		utils.CloseAll()
		close(shutdownDone)
	}()

	// 显式建监听再 Serve：日志才能报内核实际分配的地址。SERVER_PORT=0 时 addr 里的端口是 0，
	// 跨进程测试（test/python）从这行读回真实端口，不必再自己抢一个空闲端口——那种做法是 TOCTOU，
	// 抢到的端口在被服务 bind 之前可能被别人用掉，两个并发构建就会撞 bind: address already in use（P9）
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("监听 %s 失败: %w", addr, err)
	}
	log.Printf("LoomProxy starting on %s", lis.Addr().String())

	err = srv.Serve(lis)
	if errors.Is(err, http.ErrServerClosed) {
		// 等待优雅停机与清理（含监控明细兜底落库）完成，
		// 否则 main 的 log.Fatal 会在清理完成前 os.Exit
		<-shutdownDone
		return nil
	}
	return err
}
