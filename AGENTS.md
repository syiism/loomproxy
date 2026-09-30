# AGENTS.md — loomproxy-base 项目指南

> 本文件面向 AI 编码代理，介绍项目的背景、架构、构建方式与开发约定。阅读本文件前假定你对项目一无所知。
> **以代码为准**，本文件只记录现状性事实与简要规则；详细的 Bug 排查过程与方案权衡不入本文件。

## 1. 项目概述

**loomproxy-base** 是 LoomProxy（网络小说书源 API 代理/聚合服务，Go + Gin + GORM + Vue 3）的**底座项目**：只保留平台能力，**不携带任何数据源实现**。

对下（上游）它提供一套写书源的完整基础设施——处理器自注册、弹性获取栈（缓存 / 请求合并 / 熔断 / 代理池 / UA 轮换）、SSRF 防护、号池框架；对上（下游）输出统一的 Legado（阅读 App）风格数据，并内置用户系统、额度计费与限流、管理面板。

底座包含的能力：

- 用户系统：注册 / 登录（JWT + 会话设备管理）/ 找回密码 / 角色（admin·vip·user）/ 用户自助 API 密钥
- 额度与管控：额度计划、卡密兑换、按接口计费、访问控制、速率限制（固定间隔 + 滑动窗口双口径）、用量流水
- 数据源管控：数据库驱动的动态数据源 CRUD、套餐-数据源关联、平台/用户两级 baseUrl 配置
- 稳定性：LRU+TTL 与 Redis 缓存、上游响应短 TTL 缓存、singleflight、按 host 熔断与过期缓存降级、优雅停机
- 安全：SSRF 防护、IP 黑名单与滑动窗口自动拉黑、登录防爆破、场景化验证码（默认全关）、pprof 管理端点
- 内嵌 Vue 3 管理面板（`/panel`），随二进制 `go:embed` 发布

**接入一个书源 = 新建 `sources/<源>` 包（自注册）+ `sources/all.go` 加一行空白导入 + `make build`**；`app.go` 与 `db/seed.go` 不需要改动（路由对账与 seed 播种都以源包声明为准）。`handlers/` 此后专指控制面端点，数据面一律落 `sources/`。

## 2. 技术栈

| 层 | 技术 |
|---|---|
| 语言 | Go 1.26（模块名 `loomproxy`，CGO 必需——SQLite 驱动为 `mattn/go-sqlite3`。**模块名与产物名是两件事**：二进制、镜像、systemd 单元、部署路径仍一律叫 `loomproxy-go`，不随模块名变动） |
| Web | Gin |
| 存储 | GORM + SQLite（默认）/ MySQL / PostgreSQL，由 `DB_TYPE` 切换；Redis 作缓存 |
| 鉴权 | golang-jwt/v5（HS256）+ bcrypt + 静态与用户级 API Key |
| 前端 | Vue 3 + vue-router + Vite 5 + Tailwind CSS 3（纯 JS，pnpm），产物经 `web/embed.go` 嵌入 |

## 3. 构建与运行

```bash
make build   # 前端 pnpm build → go vet + gofmt → go test -race → 编译
             # 产出调试版 ./loomproxy-go（~51MB）与正式版 ./loomproxy-go-release（strip，~35MB，部署用）
make web     # 仅构建前端（必须先于 go build：embed.go 在 dist 缺失时 log.Fatal）
make test    # go test ./test/ -race -count=1
make vet     # go vet ./... + gofmt 检查（豁免存量文件，见 Makefile 注释）
make run     # build 后启动，默认监听 0.0.0.0:8081

# pnpm 11 需要构建脚本白名单：web/pnpm-workspace.yaml 的 allowBuilds.esbuild 勿删，否则 make web 失败
```

**部署**：

```bash
sudo scripts/deploy.sh            # systemd 模式：构建→备份→切换→冒烟→失败回滚（/opt/loomproxy）
scripts/deploy.sh --local         # 原地模式（免 systemd）；--skip-tests 紧急发版
docker build -t loomproxy-go .    # 多阶段：node 建前端 → golang CGO 编译 → bookworm-slim 运行
docker compose up -d --build      # .env 注入 + ./data 绑定挂载
```

运行所需最小部署单元：**二进制 + 同目录 `.env` + `data/` 目录**。配置加载顺序：进程环境变量 > 工作目录 `.env` > 可执行文件目录 `.env`（`.env` 不覆盖已存在的环境变量）。

入口与生命周期：`main.go`（监听 SIGINT/SIGTERM）→ `app.Run(ctx)`（`conf.Load()` → `db.Init()` → 构建 gin 引擎 → HTTP 服务，5 秒优雅停机 → `lifecycle.RunCleanups()`）。

## 4. 目录结构

```
main.go             入口：信号处理，调用 app.Run
app/                装配：CreateApp 构建 gin 引擎、全局与单路由中间件链、路由对账；
                    autoblock.go（IP 自动拉黑）、monitor.go（调用明细落库与归档）、pprof.go
conf/               环境变量配置加载（envStr/envInt/envFloat/envBool/envList）
base/               核心：Handler 接口与泛型注册表、BaseHandler（共享 HTTP 客户端 +
                    Fetch/FetchJSON/FetchText = 缓存 → singleflight → 熔断 → 过期缓存降级）、
                    upstream_cache.go（上游短 TTL 缓存，自包含实现——不能用 utils.Cache，
                    会形成 base→utils→base 环）、singleflight.go（导出的 Flight 类型，
                    超时兜底 + 失败结果不共享 + leader panic 防护）、circuit_breaker.go（按 host 熔断）、
                    metrics.go（调用监控内存计数 + 明细环形缓冲）、proxy.go / proxy_api.go（IP 代理池）
base/pool/          通用号池框架（见 §6）
base/legado/        Legado 响应 DTO 与五个基础处理器（Search/Detail/Chapter/Content/Explore）
handlers/           控制面 HTTP 端点：auth/ admin/ quota/（额度面板与用量流水）userconfig/
                    apikey/ verify/ catalog/（/datasources 与 /data，能力发现端点）
sources/            数据面：各源包目录 + all.go（数据源导入集合，即「本部署携带哪些源」；底座为空）
gate/               数据源请求链路上的管控闸门：访问控制 / 额度计费 / 速率限制 + 限额解析
                    （是被穿过的一环，不是被调用的端点，故不放在 handlers/）
models/             GORM 模型（User/Role/SystemSetting/Quota*/DataSource/PoolDevice/…）
db/                 三方言初始化、AutoMigrate、seed.go（角色/设置/额度计划/套餐关联/管理员引导）
utils/              auth.go jwt.go cache.go（LRU+TTL + Redis）network.go（SSRF）norm.go（字段兼容
                    与格式化通用工具）apikey.go device.go（UA 解析）
web/                Vue 3 前端（src/）+ 构建产物（dist/，被 embed）
test/               黑盒测试（包名 test，只测公开 API）
testkit/fakesource/   假数据源夹具（fake_a/fake_b/fake_c + 各自的声明位），Go 集成测试
                      与跨进程用例共用；不属于产品功能
cmd/fakegateway/      测试服务入口：登记假源后走与产品入口相同的 app.Run
docs/               中文设计文档（历史迁移记录，与现状可能有出入，以代码为准）
data/               运行时数据：loomproxy.db（或配置的 DB_NAME）、各数据源的 JSON 字典目录
scripts/ deploy/    部署脚本与 systemd 单元
```

## 5. 核心架构：处理器自注册

**新增数据源必须遵循：**

1. 源包落在 `sources/<源>`，在 `init()` 中：
   - `base.RegisterSource(base.SourceMeta{Code, Display, Category, Description, SortOrder, Status, Actions, FixedBaseURL, SearchTabs, LegacyGroups, DataFiles})` 声明数据源身份——这是路由对账、seed 播种（`data_sources` / `quota_costs` / `quota_limits` / 套餐关联）的**唯一事实来源**；
   - 每个动作一次 `base.Register(name, factory, priority, metadata)`；
   - `FixedBaseURL: true` 表示上游地址写死在源实现内：该源路由不接收 `baseUrl` 参数、不做用户/平台配置回落，也跳过 SSRF 校验。
   - `SearchTabs` 声明自有搜索分类（多媒介/多站点形态）；不声明则 `/datasources` 下发底座通用 tab。**底座不按分类名做分支**，需要新形态请补声明而不是改 `handlers/catalog`。
   - `LegacyGroups` 声明该源历史所属的平台组码（组概念 2026-08-09 已移除）；seed 据此把存量按组码配置的行展开为本源码并清理组行。
   - `DataFiles` 声明该源附属的静态数据字典（文件名 + 说明）；`/data` 列表的说明列取自声明，底座不内置任何具体文件名。
2. 处理器实现 `base.Handler`：`Handle(ctx, params)`、`GetPath/GetMethods/GetName/GetDescription/GetQueryParams/AuthRequired`；通常内嵌 `base/legado` 的基础处理器（复用 DTO 与上游缓存 TTL），或内嵌 `base.BaseHandler`（`NewBaseHandler()` 默认 GET、Auth 开、QueryParams 含 `api_key`）。
3. `sources/all.go` 加一行空白导入——这份清单即「本部署携带哪些源」，`app.go` 只空白导入 `sources` 包本身，不因新源而改。
4. 路由全部是根级 `/{source}/{action}`，无版本前缀。单路由中间件链：
   `authMiddleware（若 AuthRequired）→ monitorMiddleware（置于最前以覆盖 403/429）→ baseURLCheckMiddleware（resolve baseUrl：请求参数 → 用户配置 → 平台默认）→ gate.DataSourceAccessMiddleware → gate.BillingMiddleware → gate.RateLimitMiddleware → handler`。
5. 需要凭证池的源在 §6 登记自己的号池，不要另写一套生命周期管理。

**测试夹具**：假源本体在 `testkit/fakesource`（三个假源 `fake_a`/`fake_b`/`fake_c`，动作集 search/detail/chapter/content/explore），处理器只做「按解析出的 baseUrl 取上游并原样返回」；`test/fakesource_test.go` 只在 test 包 `init()` 里调 `fakesource.Register()`，跨进程用例经 `cmd/fakegateway` 起同一套声明。集成测试覆盖路由管线与 seed 播种全靠这三个假源——**它们只在测试二进制里存在，不属于产品功能**。新增管线类用例请继续用假源，不要引入真实书源依赖。

## 6. 通用号池（base/pool）

凭证/额度型上游资源（设备号、账号、临时 token）的复用框架，与具体上游协议无关：

- **Provider 接口**（源包实现）：`Name()` / `Create(ctx) (*Device, error)`（只建号不领取）/ `Refresh(ctx, dev) (Quota, error)`（拉真实额度，不产生领取）/ `Claim(ctx, dev) error`（领取一次，**必须叠加无损**——多次领取延长有效期而非重置）。可选实现 `ResourceExpiredClassifier`，把「资源到期」类错误从限流信号里剔除。
- **状态机**（`models.PoolDevice`，表 `pool_devices`，按 `pool + ident` 唯一）：`hot`（活跃，已领取且在有效期内）/ `cold`（冷备，未领取不过期）/ `spent`（周期次数用尽，周期重置后复活）/ `dead`（上游持续失败，超 `MaxDead` 后物理清理）。
- **策略**：资源按墙钟燃烧，故平时只保持 1 个活跃号（多号并行同步燃烧）+ `ColdSpares` 个冷备；`Acquire` 轮询活跃号（一个活跃号可服务无限并发）；`Maintain`（默认每分钟）负责临期原地续领、耗尽退役并转正替补、冷备补齐（优先逐个复活 spent，再新建）、dead 清理、错误驱动扩容（窗口错误率 >30% 且样本 ≥20 时活跃号 +1，上限 `MaxHot`，恢复后靠到期自然缩容）；`Reauthorize` 用 `base.Flight` 按号合并并发自愈（可补领则补领，否则换号）；`Report(err)` 供业务侧上报调用结果。
- **转正候选排序** `PrioritizeClaimed`：已领取且仍在有效期的冷备优先（时间在白烧），越早到期越优先，其余保持原序——重启成本低。
- **接线**：源包 `pool.Register(pool.New(provider, cfg))` → `app.Run` 里 `go pool.StartAll()`（`POOL_ENABLED=false` 整体跳过）→ 关停经 `lifecycle` 自动 `StopAll` → 管理面板「号池」页读 `GET /admin/pools`（`pool.StatusAll()`，标识与凭证值一律脱敏，只列凭证键名）。
- 底座项目不携带任何 Provider：号池列表为空是正常状态，框架由 `test/pool_test.go` 的假 Provider 覆盖。

## 7. 鉴权模型（两层，易混淆）

1. **API 网关层**：`app.authMiddleware` → `utils.VerifyAuth`。仅 `AUTH_ENABLED=true` 时生效；白名单路径跳过；接受 JWT（`Authorization: Bearer` / `?token=` / `loomproxy_token` Cookie，统一走 `utils.TokenFromRequest`）或 API Key（`X-API-Key` / `?api_key=`）。
2. **用户层**：`handlers/auth` 的注册/登录等；`AuthRequired()` 与 `AdminRequired()`（查库校验 admin 角色）保护 `/auth/me`、`/user/*`、`/quota/*`、`/admin/*`、`/debug/pprof/*`。
3. **用户自助 API 密钥**（`/apikey`，JWT 会话保护）：创建（`lp_` 前缀，明文可随时查回，每人上限 10 个）/ 列表 / 撤销。网关匹配到用户密钥时**注入归属身份**，计费/配额/监控/套餐门控按归属用户生效；静态 env 键保持匿名语义。密钥不适用于面板会话接口。
4. **会话**：JWT 的 `jti` 对应 `auth_sessions` 行（设备名/IP/最后活跃）；两层鉴权都校验会话未被吊销且未过期，**无 jti 的旧 token 一律 401**。删除/禁用用户、找回密码都会吊销相应会话。

关键路由分组：`/auth/*`（register·login（用户名或邮箱）·forgot-password·me·password·logout）、`/verify/*`（场景化验证码，`GET /verify/config` 供前端决定是否渲染输入框；发码通道 `mock`/`http` 模板适配器由系统设置切换，默认全部场景关闭）、`/quota/dashboard` 与 `/quota/usage-logs`、`/user/*`（baseUrl 配置·导入书源·卡密兑换）、`/admin/*`（详见 §9）、`/datasources`·`/data`（免鉴权，Redis 缓存）、`/announcement`、`/panel`。

## 8. 弹性获取栈与配置

上游请求全部经 `base.BaseHandler`，因此自动获得：短 TTL 响应缓存 → singleflight 合并（键 = URL+请求头）→ 按 host 熔断（冷却期快速失败 503）→ 故障时降级返回过期缓存；代理池与 UA 轮换在 `base.Fetch` 层。

数据源相关处理器**不要自建 http.Client**；自定义超时/请求头通过 `BaseHandler` 字段设置。需要包内自持的获取器（如后台协程）用 `base.NewBaseHandler()` 并设 `Path`，使其同样命中代理门控与熔断口径。

全部配置为环境变量（`conf/conf.go` 为权威定义，含默认值）：

- 服务：`SERVER_HOST`·`SERVER_PORT`·`SERVER_LOG_LEVEL`
- 网络与缓存：`TIMEOUT_CONNECT`·`TIMEOUT_POOL`·`CACHE_TTL`·`CACHE_MAXSIZE`·`UPSTREAM_CACHE_TTL`·`UPSTREAM_CACHE_MAXSIZE`；`REDIS_*`
- 熔断：`CIRCUIT_BREAKER_ENABLED`·`_FAILURES`·`_COOLDOWN`
- 代理：`UPSTREAM_PROXIES`·`UPSTREAM_PROXY_FILE`（5s 热加载）·`UPSTREAM_PROXY_API`·`_API_SCHEME`·`_API_INTERVAL`·`UPSTREAM_PROXY_CHECK_URL`·`UPSTREAM_UA_ROTATE`；动态池自维护水位（低于 5 自动补充，复验存量 + 逐轮拉新）；**哪些接口走代理由系统设置 `proxy_enabled_sources` 决定**（逗号分隔，支持整源与单接口两种粒度，留空=不限制）
- 号池：`POOL_ENABLED`·`POOL_COLD_SPARES`·`POOL_MAX_HOT`·`POOL_MAX_DEAD`·`POOL_RENEW_BEFORE_SEC`·`POOL_MAINTAIN_SEC`
- 数据：`DATA_DIR`·`DATA_FILE_GLOB`·`TZ_OFFSET_HOURS`·`ERROR_CODE`·`RETIRED_SOURCES`（本部署已下线的历史数据源码，逗号分隔，默认空；启动时清理其配置表存量行——底座不携带源，清单归部署侧）
- 鉴权：`AUTH_ENABLED`·`API_KEYS`·`AUTH_WHITELIST`·`JWT_SECRET`（**生产必须改**，启用鉴权时用默认值直接拒绝启动）·`JWT_EXPIRE_HOURS`·`ADMIN_USERNAME`·`ADMIN_PASSWORD`
- 数据库：`DB_TYPE`（代码默认 **mysql**，`.env.example` 与镜像默认 sqlite）·`DB_HOST/PORT/USER/PASSWORD/NAME/SSLMODE`；SQLite 路径为 `DATA_DIR/DB_NAME.db`，经 DSN 启用 WAL + `busy_timeout` + `SetMaxOpenConns(1)`（读事务升级写会触发不被 busy_timeout 重试的 `SQLITE_BUSY_SNAPSHOT`，单连接彻底规避）

登录 token 时长解析优先级：用户个人设置 > 系统设置 `jwt_expire_hours` > `JWT_EXPIRE_HOURS` > 168h；任一级取 -1 即永不过期（JWT 不写 exp、会话表写 100 年）。

## 9. 管控：额度、限流、访问控制、监控

- **计费**（`gate.BillingMiddleware`）：`quota_costs.status=0` 的接口对全员 403；未配置或 cost=0 不计费；匿名不计费；admin 不限不记；上游 200 后按 `quota_costs` 扣减并写 `quota_usage_logs`（口径为北京时间每日零点重置，并发下允许少量超扣）。`AUTH_ENABLED=false` 时计费关闭（接口禁用仍生效）。
- **访问控制**（`gate.DataSourceAccessMiddleware`）：先查数据源 `status`，再按用户生效套餐查 `QuotaPlanDataSource` 关联，不满足 403。
- **限额解析优先级**：用户数据源级覆盖（`user_quota_overrides`，**追加语义**：生效额度 = 套餐限额 + 覆盖值）> 套餐限额（`quota_limits`）> 不限。
- **速率限制**（`gate.RateLimitMiddleware`）两种口径并存，优先级：套餐级 > 全局，同级内 窗口计数（`limit_count` + `window_sec`，允许突发）> 固定间隔（`interval`，令牌桶容量 1，不可突发）；套餐级配了任一种即不回退全局。同时作用于 IP 维度与用户维度（key 含 planId），两者都放行才放行；管理员豁免全部；配置直查库，保存即生效。
- **监控**（`base/metrics.go` + `app/monitor.go`）：内存聚合 + 最近明细环形缓冲，缓冲满 250 条批量落 `api_call_logs`；清理前按 数据源/接口 聚合累加进 `api_call_stats` 永久归档；`GET /admin/monitor`、`/monitor/trend`（近 7 天按天×源，合并内存中未落库明细）、`/monitor/history`、`POST /monitor/reset`。
- **自动拉黑**（`app/autoblock.go`）：滑动窗口统计数据源路由的 403/429，达阈值写黑名单（`source=auto`）；回环地址永不自动拉黑；开关与阈值走系统设置（`auto_block_enabled`·`auto_block_threshold`·`auto_block_window_sec`）。
- 历史字段名注意：`quota_costs` / `quota_cost_plans` / `user_quota_overrides` 的 `group_code` 列**实际存的是数据源码**（组概念已移除，启动时按各源 `LegacyGroups` 声明把存量组码行展开为每源一行并删除组行；无声明则组行留在库中不生效）。同理，本部署下线的历史源由环境变量 `RETIRED_SOURCES` 声明，seed 据此清理其在各配置表的存量行（`db.cleanupRemovedSources`，历史用量流水保留）。

## 10. 开发约定

- **统一响应格式** `{"code","msg","data"}`，`code === 0` 为成功，错误时 `code` 取 `ERROR_CODE`（默认 -1）。
- **错误处理**：上游非 2xx 抛 `base.UpstreamError`（透传状态码，Message 附响应体前 256 字节摘要）；`app.handleError` 统一映射（超时→504；读/解析失败等 `StatusCode=0`→502；不安全 base_url 与参数缺失→400；DNS/连接拒绝→502）。**对下游的错误文案必须脱敏**：网络错误的 `err.Error()` 可能含完整签名 URL，详细信息只进服务端日志。
- **上游字段兼容**：解析上游响应禁止写死单一字段名，必须多字段兼容读取（`utils.FirstNonEmpty` / `utils.ToString`，数值 ID 一律用 `utils.ToString` 避免科学计数法），拼进 URL 的 ID 必须 `url.QueryEscape`。
- 日志与界面文案用中文，代码标识符用英文。
- **可信代理**：`CreateApp` 中 `SetTrustedProxies(["127.0.0.1","::1"])`——只有本机 nginx 的 `X-Forwarded-For` 参与真实 IP 还原。套 CDN 部署时需同步配置 nginx real_ip（`set_real_ip_from` + `real_ip_header`），否则记录与限流/黑名单口径都会是边缘节点 IP。
- **前端**（规范见 `.trae/skills/minimalist-ui`）：暖单色配色、衬线标题（Newsreader）+ 几何无衬线（Geist）、1px `#EAEAEA` 边框、Bento 网格、淡彩标签；**禁止** emoji、渐变、重阴影、Inter/Roboto/Lucide。分层：`api/client.js`（fetch 封装：token 注入、401 跳登录、`code!==0` 抛 ApiError）+ `api/index.js`（按域端点方法，页面不拼路径）+ `store.js`（reactive 会话态，路由守卫用 `meta.public`/`meta.admin`）+ `components/`（UiTag/UiModal/UiPagination/UiSpinner/UiEmpty/UiField/UiSwitch/UiTrendChart/VerifyCodeField/PageHeader，标签一律 UiTag）+ `styles.css` 的 `@layer components` 基样类（btn/input/card/tag/table）。新页面复用这些组件，不要再写一次性样式。
- **系统设置页**（`web/src/pages/admin/Settings.vue`）是卡片聚合而非平铺列表：受管 key 按功能分 5 张卡，卡级 diff 保存（逐 key `PUT /admin/settings/:key`）；不在清单内的 key 落入「自定义配置」兜底卡。**seed 新增设置 key 时要同步把字段加进前端 GROUPS 的对应分组**（或确认走兜底卡）。
- `web/vite.config.js` 已配 dev 代理（`/auth`·`/admin`·`/quota`·`/datasources`·`/data`·`/verify` → `localhost:8081`）。
- **测试**：黑盒测试一律放 `test/`（包名 test，只测公开 API，不与源码混放）。单元级用 `httptest` + 直接构造 `conf.Config` 全局指针；HTTP 集成用 `testserver_test.go` 的 `newTestServer(t)`（临时 SQLite + `app.CreateApp()` + `httptest.NewServer`，自动 AutoMigrate + seed，admin/admin1234）。全局态（`conf.Config`·`db.DB`·`utils.DefaultCache`·限流器·号池注册表）跨用例共享，**集成用例禁止 `t.Parallel()`**；`newTestServer` 已清缓存，直接改库后按需 `delCostCache`。
- **质量门禁**：每次修改完成后 `make build`（含 vet + gofmt + `go test -race`），并**必须 `git commit`**（含 AGENTS.md 同步更新），不留未提交的工作区改动。
- **AGENTS.md 同步规则**：改动涉及数据源、接口、中间件、配置项、架构模式时，同步更新本文件对应章节并一起提交。不确定时优先更新，避免文档与代码脱节。

## 11. 安全注意事项

- 不要把真实密钥写入仓库或文档：`.env` 的 `API_KEYS`·`ADMIN_PASSWORD`·`JWT_SECRET` 均属敏感信息；生产必须改默认 `JWT_SECRET`。
- CORS 当前为 `*`（面向阅读 App 的开放 API），收紧前需评估对书源客户端的影响。
- `baseURLCheckMiddleware` 对所有注册路由校验 `base_url`（`utils.IsSafeURL`：仅 http/https，DNS 解析后拒绝回环/私有/保留 IP，含 IPv6）。**按来源区别对待**：请求参数与用户个人配置严格校验；**平台默认配置视为管理员可信来源**，允许指向本机/内网（用于同机部署数据源项目）。声明 `FixedBaseURL` 的源整体跳过。
- 密码 bcrypt 哈希；用户软删除，其用户名/邮箱进入黑名单（注册与建用户查重走 `Unscoped()`，冲突返回 409 提示「已被注销账号占用」）；邮箱可空但唯一。
- 号池与设备凭证（`pool_devices.attrs`）属上游签名凭证：接口响应与日志只出脱敏值（保留前 8 后 4），管理面板不展示原值。
- 面板路由守卫在前端，真正可信的权限校验是后端 `AdminRequired()`——后端是唯一信任边界。

## 12. 已知取舍

- 处理器间传参用 `map[string]interface{}`（换取统一的 Legado 形状与低接入成本，牺牲部分类型安全与性能）。
- SSRF 的 DNS 结果永久缓存在内存中（无过期）。
- 缓存预热：启动约 2 秒后预填充 `/datasources`（匿名视图）与 `/data` 的缓存（30 秒超时；`db.Init` 失败时跳过）。
- `/datasources` 与 `/data` 走 Redis 缓存，管理端相关写操作（数据源 CRUD、套餐关联、用户套餐变更、兑换）调用 `InvalidateDatasourcesCache()` 写时失效。
- 配额计费按**请求**计数，含上游缓存命中；如需按上游真实调用计费需另改。
- `docs/` 里的激进方案（unsafe 字段映射、工作窃取调度等）不落地。
- 仓库无远端、无 CI；构建纪律靠 Makefile，部署靠 `scripts/deploy.sh` 与 Dockerfile。
- 历史 Python 版设计文档在 `docs/`；`test/python/` 是 auth/quota 用例的 pytest 黑盒移植版（uv 管理）。
  移植版起的是真实子进程，而底座产品二进制不带数据源，故用例打的是 `cmd/fakegateway`（假源 `fake_a`/`fake_b`/`fake_c`）；harness 每次会话重建该二进制，`LOOMPROXY_BIN` 可指向自备的产物。

## 13. 项目知识库（第三大脑）

配套 Obsidian 知识库：`/home/muyang/Documents/obsidian/loomproxy/`（库结构与其维护规则见该库自带的 AGENTS.md，写库前必读）。

**分工边界**：本文件只记权威的现状性事实（架构、接口、配置、约定是什么）；详细的 Bug 排查过程、根因分析、方案权衡一律入知识库，不在本文件展开。

- **动手前先查库**：报错或行为不符预期时，先查 `04_检索索引/Bug问题关键词索引.md` 与 `01_知识库加工区/07_排错日志Bug记录/`；方案设计/重构前查 `02_碎片对话整合库/` 了解历史权衡；触碰易复发区域（embed 前端缓存、旧进程占端口、GORM 方言差异、号池/限流状态机）前查对应条目。`00_原始归档_raw_export/` 只读且单文件可达 1.3MB，禁止整读。
- **修复/新增后录库**：Bug 修复按库《Bug修复记录模板》写入 07 分区并更新关键词索引；新功能写入 01 分区并更新《LoomProxy项目功能索引》。知识库改动**不自动 git commit**（由用户决定提交时机），源码仓库改动照常按 §10 提交。
