# AGENTS.md — loomproxy-base 项目指南

> 面向 AI 编码代理的**索引页**：本文件只存条目与一句话总结，细节一律在 `docs/` 下按主题成页。
> **以代码为准**；文档与代码冲突时改文档。改完代码要同步的是 `docs/` 对应页，不是把细节写回本文件。
> 文档地图见 [`docs/README.md`](docs/README.md)。

## 1. 项目定位

- 一句话：上游接口的**代理与治理平台**——代理/内联第三方接口、归一异构响应，并做鉴权、计费、限流、监控与生命周期管理；底座不携带任何上游接口实现。
- Legado 只是内置的一种**下游输出契约**（`base/legado` 五动作），动作集与输出形状由接口包自己声明，底座不做书源特例分支。
- 接入一个源 = 新建 `sources/<源>` 包自注册 + `sources/all.go` 加一行空白导入 + `make build`；`app.go` 与 `db/seed.go` 免改。
- 搜索响应**自带来源**：每条书目项的 `source` 与 `kind` 首项由平台在出口统一打（`StampSearchSource`），
  源不必自己填、填了也会被覆盖——下游（含聚合书源）拿它把详情/目录/正文路由回正确的源。
- 详情：[`docs/架构/项目定位与能力清单.md`](docs/架构/项目定位与能力清单.md)

## 2. 技术栈

- Go 1.26（模块名 `loomproxy`，CGO 必需）+ Gin + GORM（SQLite/MySQL/PostgreSQL 由 `DB_TYPE` 切换）+ Redis；Vue 3 + Vite + Tailwind 面板经 `go:embed` 内置。
- 模块名与产物名是两件事：二进制、镜像、systemd 单元、部署路径恒为 `loomproxy-go`。
- 详情：[`docs/架构/技术栈.md`](docs/架构/技术栈.md)

## 3. 构建、运行与部署

- 唯一门禁是 `make build`（pnpm 前端 → `go vet` + `gofmt` + 手写 SQL 的保留字/反引号扫描 + 前端未定义类扫描 + 管理端导航完整性 + 集成用例装配顺序 + 设置键必须有后端读取方 + 判据页的分布数字必须等于当场数出来的（且全页只留一句） → `go test -race` → 调试版与 strip 版两个产物）；前端必须先于 Go 构建。
  **测试段跑的是 `./...` 而不是只跑 `./test/`**：包内单测不被门禁跑到就等于没写——
  携带形态在源包里的向量对拍与解析层用例，过去只有人手动跑 `go test ./sources/...` 才会红（分支侧 S30 的门禁盲区，本轮回骨架收口）。
- 部署走 `scripts/deploy.sh`（systemd 与 `--local` 两种模式）或 Docker；最小运行单元是「二进制 + 同目录 `.env` + `data/`」。
- 配置优先级：进程环境变量 > 工作目录 `.env` > 可执行文件目录 `.env`。
- 手工换装（不走 `scripts/deploy.sh`）有三查：**备份先验非空**、**核 md5 与 install 在同一条远端命令里、装完复算目标文件**（后台传输没收到完成通知就不算传完）、**重启后按启动契约核对日志关键行**而非只看 `is-active`（`Restart=always` 会把崩溃循环伪装成运行中）。
  下发类产物（书源 JSON、静态托管文件）从磁盘读、不随二进制走，要单独一步装到生产目录并复算；**先装产物、后改 `.env`**。
- 详情：[`docs/运维/构建与部署.md`](docs/运维/构建与部署.md)

## 4. 目录结构与模块边界

- 分层事实：`app/` 装配、`base/` 核心与获取栈、`base/legado/` DTO 与五基础处理器、`handlers/` 控制面、`sources/` 数据面、`middleware/`+`gate/` 链与闸门、`db/`+`models/`、`utils/`、`web/`、`test/`+`testkit/`。
- 两条硬边界：`middleware` 根包是叶子包（不得导入子包或 `gate`，否则成环）；`base` 不得反向导入 `base/legado` 与 `utils`。
- 详情：[`docs/架构/目录结构与模块边界.md`](docs/架构/目录结构与模块边界.md)

## 5. 处理器自注册与数据源声明位

- `base.RegisterSource(SourceMeta{...})` 是路由对账与 seed 播种的**唯一事实来源**；声明位含 `Actions`/`FixedBaseURL`/`SearchTabs`/`LegacyGroups`/`DataFiles`/`MediaType`/**`OnBoot`**/**`RequiredParams`**。
- **源的启动动作放 `OnBoot`，不要放包 `init()`**：`conf.Load()` 在 `app.Run` 里才读 `.env`，init 阶段 `os.Getenv` 只能看到真实环境变量（症状是「.env 改了不生效」）；`OnBoot` 跑在 `db.Init` 之后、`pool.StartAll()` 之前，登记号池与初始导入都在这里。
- **必填参数用 `RequiredParams` 声明，不要在源里自己判**：`map[动作][]参数名`，由 `middleware/source` 的 `reqparams`（链上 350，晚于 monitor/baseurl、早于 access/billing）在进 handler 之前统一校验，缺任何一个直接 400。源里自己判的旧写法是返回 `ContentType=="error"` 的正文——HTTP 仍是 200，下游会把「缺少参数」那句话**当正文渲染进阅读器**（P22、分支侧 S15）。`baseUrl` 由 baseurl 中间件解析后放 context，所以这条校验必须排在它之后；未声明的动作一律直通（`search` 的空搜是合法形态，别乱填）。
- 需要新行为优先**补声明位**而不是改骨架；声明值非法在 `RegisterSource` 期 `log.Fatalf`（`RequiredParams` 声明了未声明的动作就是炸，不是静默不校验）。
- 路由全是根级 `/{source}/{action}`；链由中间件包的 `Def{Scope,Order,Applies,Build}` 装配，禁止在 `app.go` 手工拼链。
- **数据源分组**（`source_groups` + `data_sources.group_id`）只是归类与筛选视图：限额/计费/限速/授权的键
  **一律仍是数据源码**，`POST /admin/source-groups/:id/apply-limits` 也只是批量写入口（落库每源一行）。
  一源至多一组，删组只把成员回落 NULL 不删源，组改动一律失效 `/datasources` 缓存。
- 集成测试只用 `testkit/fakesource` 的三个假源，不引入真实上游。
- 详情：[`docs/架构/处理器自注册与数据源声明位.md`](docs/架构/处理器自注册与数据源声明位.md)、
  [`docs/方案/数据源分组与首页额度看板.md`](docs/方案/数据源分组与首页额度看板.md)


## 6. 通用号池（base/pool）

- Provider 三钩子 `Create`/`Refresh`/`Claim`（**Claim 必须叠加无损**）+ 可选 `ResourceExpiredClassifier`；状态机 `hot`/`cold`/`cooldown`/`spent`/`dead`，表 `pool_devices`。
- 水位参数是 `ColdSpares`/`MaxHot`/`MaxDead`，**总号数上限是另一个声明位 `Config.MaxDevices`**（0=不限，
  判在框架的建号入口，报 `ErrCapacityReached`）——建号会对上游产生不可逆增长的源用它，别在源内自己数行。
  **死号保留上限是算出来的**：`min(MaxDead, MaxDevices - 可用目标)`——`MaxDevices < MaxDead` 时旧代码里死号
  既清不掉又占满名额（生产 uxx 是 5<10），现在先留得下可用号再谈留档，建号被判住前还会先清一次腾名额（P35）。
  **水位也被名额压**（同一条规则的另一半）：`MaxDevices>0` 时 `ColdSpares`（spread 是 `TargetDevices`）在
  `withDefaults` 里夹到名额之内、**下限 1**（建号只走补齐这条路，夹到 0 池就一个号都建不出来），
  而 `MaxDevices` 本身不动——它是闸门，按水位收紧会把错误驱动扩容的余量一起削掉。
  单设备池（`MaxDevices=1`）因此长期补不齐冷备，这是预期：`ErrCapacityReached` **只在状态变化时出声**，
  不再每轮巡检刷一条（生产 qm_device 曾每 60 秒一条，把「这个池有问题」的读数泡坏）。
  快照另有一格 `soft_deleted`（按池数软删行）：**框架从不软删自己的行**（清理走 `Unscoped()` 硬删），
  所以它 >0 只可能是"有人在库外面动过号池"；这些行在所有状态计数里隐形——**已拍：不自动清、维持手工清**
  （P35②，2026-10-06）：`pool_devices` 的行是上游凭证的载体，自动物理清理会拿走运维的手工 undo。
- **持锁调源钩子必须带截止**（P81）：`Provider.Create`/`Refresh`/`Claim` 三处出口走 `hookCtx()`
  （`POOL_HOOK_TIMEOUT_SEC`，默认 30 秒）——维护与装填是**持着 `p.mu`** 调它们的，一次挂住的上游
  同时扣住维护协程与全部 `Acquire`。框架只保证"传下去的 ctx 带截止"，**不读 ctx 的 Provider 拦不住**。
- **`Config.Kind` 是调度分叉位，不只是标签**：`burn_wall_clock`（默认）= 墙钟燃烧型，只保持 1 个活跃号
  + N 个冷备，临期续领、用尽换号、错误驱动扩容；`spread` = 用量摊薄型，框架**只调 `Create`**（不 Claim
  不探活），把请求轮询到全部可用号上，失效走 `Pool.Cooldown` 的临时冷却而不是判死（P5）。
  给「不因墙钟过期、燃烧看请求量」的号套默认形态，等于把所有请求打到同一台设备上。
- **两条后台路径的 panic 兜底 + 临界区一律 defer 解锁**（P77/P78）：`Start`/`Maintain` 各
  `defer p.guardPanic(...)`（它们直调源写的 `Provider.Create/Refresh/Claim`，而这里没有 gin Recovery），
  被它们走到的临界区一律 `p.locked(fn)`——**「临界区一律 defer 解锁」是全仓规则，不只是号池**（P93：
  尾解锁被 panic 跳过 = 锁被永久持有，症状从"少一行读数"变成"下一个请求卡死"；**已由 `make vet` 的
  `lock-defer-check` 扫**，多段临界区与循环体各包闭包，就地加 defer 会把锁扩到网络请求上）；启动装填 `initLedger` **全程持锁**——`go pool.StartAll()` 排在
  `http.Server` 之前，端口开着而燃烧型还在逐行探活，不持锁等于让并发 `Acquire` 读半装填的切片。
- 嵌套凭证（会话 cookie 一类）走 `Device.Payload` / `pool_devices.payload`，框架不解析、只搬运
  （回写用 `Pool.UpdatePayload`）；`Attrs` 是扁平 `map[string]string`——**塞嵌套在 Go 里就编译不过**，
  真实的坏形态是这一列被库外面改坏（手填 SQL、截断文本），那会表现为"号在库里但凭证为空"；
  现在 `deviceOf` 对解不开的 `attrs` 出一条脱敏 ERROR，同一行只喊一次（P59）。
- 底座不携带任何 Provider，号池列表为空是正常状态；面板读 `GET /admin/pools`（凭证只列出键名）。
- 详情：[`docs/架构/号池框架.md`](docs/架构/号池框架.md)


## 7. 鉴权模型

- 两层：API 网关层（`middleware/apiauth` → `utils.VerifyAuth`，JWT 或 API Key）与用户层（`AuthRequired()`/`AdminRequired()` 查库）。
- **三形态（token / cookie / apiKey）在用户面端点统一可用**，但**凭证引导类（`/apikey`、`/auth/me·password·privacy·sessions·logout`）
  与管理面（`/admin/*`）只认会话**：长期密钥不该能铸造别的密钥、改密码或绕过「一次登出全部失效」。
  两层共用同一个凭证解析器，别在第二处再写一遍解析顺序（P26）。
- **登录/找回密码的防爆破是进程内存状态，不落库**：按**客户端 IP** 限频（登录 10 次/分、找回 5 次/分），
  连击失败 10 次或超窗口即锁 1 小时。它**不是 IP 黑名单**（`blocked_ips` 在库里、拦所有请求）。
  管理面可读可清：`GET /admin/security/attempts`（快照，锁定项排前）与 `POST /admin/security/attempts/reset`
  （按 IP 清锁与连击）——重启进程也会全清，所以这条端点的价值是把「为救一个 NAT 出口而重启」换成定向操作（P40）。
  已知边界：不按账号、且自动拉黑只看数据面 403/429，**登录失败不喂给它**。
  **账号侧的形状已经可观察、但仍然不管事**（P40① 的准备）：`handlers/auth/account_watch.go` 按登录标识
  记「尝试 / 失败 / 不同 IP 数」（内存、一小时、不入库、**不参与 `locked()` 判定**），经
  `GET /admin/security/attempts` 的 `accounts` 出，面板「IP 拉黑」页有只读一节，标黄阈值由响应的
  `accounts_meta.multi_ip_yellow` 下发（复用 `suspect_distinct_ips` 那一条定义）；那一节还渲染
  `first_seen`/`last_seen` 两个时刻（**跨度**才是"慢速试"唯一看得出的形状，P66）。**已拍：不据此锁人**
  （P40① 选 A，2026-10-06）——用户名可被任意填，按标识锁人等于让攻击者点名把某个账号锁在门外；
  那一列**永远只给人看**，"观察不许变成判定"那条反向断言从此是定案的守卫。
- 用户自助密钥（`lp_` 前缀）匹配时**注入归属身份**，计费/配额/监控/套餐门控随该用户生效；静态 env 键保持匿名语义。
- 会话由 JWT 的 `jti` 对应 `auth_sessions`，无 `jti` 的旧 token 一律 401；**会话行就是鉴权的执行点**
  （每请求 `db.ValidateSession` 查 `revoked_at`），所以"吊销没做成"等于那个人还能用。
  禁用/删除用户、改密、找回密码、改名都吊销会话，且**与那次凭证写入同一个事务**（P56）：
  分开提交会造出"密码改了、旧设备还在、响应说成功"。改密与登出保留当前设备（`keepSessionID`），
  只有"改名/禁用/删除/找回"按语义把当前会话也算进去；`Logout` 在吊销失败时仍返回 200 但带 `notice`。
  **密钥是同一次处置的另一条通路，不会跟着会话走**：归属由 `utils.LookupApiKeyIdentity` 解析、
  命中缓存时不复查 `users.status`，所以禁用/删除用户必须显式 `utils.InvalidateUserApiKeys(userID)`
  清掉他名下密钥的归属缓存，否则「禁用了但他还在跑」最长持续一个 `CACHE_TTL`（P47）。
  两个状态码分得开这两条通路：403「无效的 API Key」= 查库查不到人（失效做成了）；
  401「用户不存在」= 身份来自缓存、往下才发现库里没人（缓存还热着）。
- 详情：[`docs/架构/鉴权模型与会话.md`](docs/架构/鉴权模型与会话.md)


## 8. 弹性获取栈与环境变量

- 上游请求一律经 `base.BaseHandler`：短 TTL 缓存 → singleflight → 按 host 熔断 → 过期缓存降级，代理池与 UA 轮换在 `base.Fetch` 层；不要自建 http.Client。
- 全部配置是环境变量，权威定义在 `conf/conf.go`（含默认值）；`REDIS_*` 同时承载接口缓存与「标识→名称」命名缓存的跨重启持久化。
- `TZ_OFFSET_HOURS` 是平台时区的**唯一值**，而「今天零点」的**唯一算法**是 `utils.DayStart(days)`——额度日界、
  榜单窗口、趋势图日标、验证码日限、统计的今日新增全走它（`make vet` 的 `tz-check` 拦住回退，P71）。
- 几个容易踩的默认：`REDIS_PASSWORD` 默认空（写死默认密码会让无密码 Redis 连不上）、`MONITOR_RETENTION_DAYS` 默认 0=永久保留、`DB_TYPE` 代码默认 mysql 而 `.env.example` 默认 sqlite。
- 详情：[`docs/架构/弹性获取栈与环境变量.md`](docs/架构/弹性获取栈与环境变量.md)

## 9. 管控：额度、限流、访问控制、监控

- 计费/访问控制/限流三轴在 `gate/`，顺序 access(400) → billing(500) → ratelimit(600)，`monitor(200)` 置于其前以覆盖 403/429。
  **还有一个更外层的闸门：`maintenance_mode`**（待办清单 P53②，面板那个开关从此说话算）——开着时数据面
  `/{source}/{action}` 在进 handler 之前一律 503，**管理员放行**、查不到角色按拦处理（失败关闭），
  控制面与登录注册不受影响；被它拦下的请求没进过 handler，`CallSubject` 是 nil、维度全空，但观测仍要走完。**面板顶部有一条横幅说明这件事**（P95：信号是 `/announcement` 的 `maintenance`，按 `isAdmin()` 分两个变体、不可关闭；定稿里给登录页的那条被撤回——维护不动登录注册，写上去就是替系统说假话）。
- **多设备登录监控（P44）**：登录时按 `max_active_sessions` 把超出的旧会话移出（保留最近的），开关 `device_watch_enabled` **默认关**；
  处置**只按会话数，IP 数再多也只标红不踢人**。读数在 `/admin/devices`；密钥侧只统计数量与来源 IP 分布，**不做冻结**。
  **活跃会话只有一份定义**（`db.ActiveSessionCond`：窗口内建立 + 未吊销 + 未过期），页面的 `active_sessions`
  与用户管理的 `over_cap=1` 筛选共用它；而会话**明细**那一条按窗口全集取（`db.SessionWindowCond`，含已吊销），
  否则「近期移出 / 登录设备 / 登录 IP」会跟着静默归零（P45）。这页可按七个聚合列排序：`sort`/`dir` 走白名单映射到
  **内存比较函数**，**永不进 `ORDER BY`**，非法值回默认而不是 400。
- **用户管理有九个筛选条件（P46）**：状态三态（含「已删除」）、套餐（含「绑着的套餐已下架」）、角色、到期四档、
  活跃度（**从未登录单独一档**）、留存同意位（**NULL ≠ false**）、被手工刷过额度、密钥数分档、活跃会话超上限。
  非法枚举值一律当「没这个筛选」；档位数字由响应 `filters_meta` 下发，面板不自己抄。
- **当日用量的起算点只有一处定义**（`gate.UsageSince`，P41）：取「平台时区零点」与「该用户的管理员刷新时刻」
  （`users.quota_reset_at`，NULL=从未刷新）里较晚的那个，判定与面板读数共用它。管理员刷额度走
  `POST /admin/users/:id/refresh-quota`（只认会话）：**不删流水、不改限额**，放开的是「再一天的量」。
- **套餐名/角色名可被本人设显示别名（P43）**：`user_display_aliases` 按 `(user_id, kind, target_id)` 存，
  默认名那两张全局表一概不动；显示规则只有 `models.DisplayAlias` 一处——本人界面看 `display_name`，
  管理员与运营看 `name` 并另给一栏 `alias`。写入口 `PUT /auth/display-alias` 只认会话，资格 = 绑定了非免费套餐。
- **扣减按内容去重**：同接口同内容在 `BILLING_DEDUPE_SEC`（默认 300 秒，0=关）窗口内只扣一次（P25）。
  标识取不到时照扣，不去重。
- **授权与限额是同一行（P34）**：`quota_limits(scope=source, target=数据源码)` 存在即该套餐可用该源，
  `-1` = 不限额，**删行 = 回收**；判定口只有 `gate/grant.go`，旧关联表已停写、已摘出 `AutoMigrate`、生产已 `DROP`。
  播种只在套餐新建那一次铺默认档，新源由 `attachSourceToBuiltinPlans` 补授权——按行无限回填会复活管理员删掉的授权。
  限额优先级：用户数据源级覆盖（**追加语义**）> 套餐限额 > 不限；限流为套餐级 > 全局。
  **额度只有一个窗口：当日**——`quota_limits.period` 没有任何判定读它，而面板把它当单位显示也让人改（P70）；
  **②已落 B 收掉**（`3672d22`）：模型不再映射这列、写口不再收 `period`、面板只显示「/ 日」，
  库里那一列仍在——**已拍定不追 DROP**（2026-10-06）：B 摘的是口径（谁读它、谁显示它、谁收它），不是清库，
  没有任何路径读它之后 DROP 只是整洁性，不值得为它单独开一次生产写授权。
- **同意位关闭时内容维度不捕获（P37）**：`users.content_consent`（NULL=默认同意），个人中心 `POST /auth/privacy` 只认会话；
  关闭后 `monitor` 在写明细前抹掉七个内容字段（**含标识**，否则名称回填会补回来）并置 `content_withheld`。
- **聚合搜索（P38）**：`GET /{source}/search?sources=a,b` 在 handler 内扇出到多个源，那条路不经过中间件链，
  所以逐源重走访问与额度判定（用抽出来的 `gate.SourceAccessVerdict` / `gate.AggregateTargetVerdict`，不是复制逻辑）、
  各扣一次；入口源不重判不重扣。结果里每个源只给成因枚举 `sources_status`，全失败照实报错而不是「200 + 空列表」。
- 监控明细 `api_call_logs` 带内容维度与标识列，名称靠命名缓存反查、可事后幂等回填（**默认由 `MONITOR_BACKFILL_SEC`=900 秒的循环自己补**，
  单批 5000 行、按 id **倒序**捞）；维度榜的书名条目额外带 `book_id`（**面板不展示**，给拿榜单去检索的人）；
  书名榜与章节榜只统计 `action='content'`。**两张榜的数字不同义**：书名榜是「同数据源内的访问人数」（匿名退 IP，
  跨源相加不再去重），章节榜与搜索榜仍是次数；成功率与平均耗时一律按请求数算（P18）。同一接口还下发 `coverage`
  （按 源×接口 的内容维度覆盖率）：判据一律 `COALESCE(col,'') <> ''` 只数**有**的行、缺失由总数相减，
  `expect_book` 标明该动作是否理应带书名（search/explore 留空合法、面板不标红），少于 5 行的组合不出。
  分母**不含**用户关掉留存的那些行（`content_withheld`，P37）。**失败读数含「带内失败」**（P22①：HTTP 200 但正文是
  `ContentType=="error"` 的错误载荷，`ObserveCall` 识别后在明细标 `in_band_error` 并把成功率/覆盖率读数计成失败，
  覆盖率表单列 `in_band_failed`）——失败请求天然五维全空，是「缺格子的合理成因」；对下游的响应与计费口径不受它影响（P22② 已判定：**维持带内**，只改观测计数）。
- 公开榜单的放行名单只有一个真相：设置项 `rank_public_sources`。**编辑入口只有一处**——数据源列表每行的
  「榜单可见」开关（`PATCH /admin/data-sources/:id {rank_public}`）；设置页那份复选组已收掉（P17），只显示只读摘要 + 跳转。
  删源时后端顺带把该源码从名单摘掉；`PUT /admin/settings/rank_public_sources` 仍是全量入口，留给脚本与部署用。
- 详情：[`docs/架构/管控-额度限流与监控.md`](docs/架构/管控-额度限流与监控.md)


## 10. 开发约定

- 响应统一 `{code,msg,data}`；对下游错误文案必须脱敏。**上游文本的出口有两条，都要过 `sanitizeUpstreamMsg`**：
  `handleError`（handler 报错）与 `scrubInBandMsg`（带内错误正文，`ContentType=="error"` 的 `message`，P23①）。
  **还有第三条出口不归它管**（P90）：我们自己把 `err.Error()` 拼进响应——本地写库失败发的是驱动原文
  （`Duplicate entry …` 等于替人枚举账号，`Data too long for column …` 交出列名），而 `sanitizeUpstreamMsg` 只处理上游文案。
  判据：按**读者**（`AdminRequired()` 之后可信，SQL 文本是 §11 设计的一部分）与**来源**（预定义句安全 /
  绑定错误泄露结构体名 / 驱动与上游原文泄露）两轴审一遍。
  这条轴现在有两个落点：**用户面六处参数绑定错误已收成固定句「参数错误」**（P90②，真因进服务端 ERROR；
  `test/binding_msg_leak_test.go` 按路由表把六个端点各发一个非法体钉住），而**管理面那 33 处判为不改**。
- **数字 query 参数只走 `utils.Paginate`**（单一处夹，页长 100 / 页码 1e6）：`Atoi` 超范围时返回错误**但也返回 `MaxInt64`**，
  丢 err 就是收下它，负 offset 会被 GORM 整段丢掉（P86）。
- 手写 SQL 的别名/列名必须避开 MySQL 8.0 保留字（SQLite 容忍、只有生产暴露；**已由 `make vet` 扫**），
  条件里的列名一律用 GORM map 形式让它按方言加引号——**不要在 SQL 里写死反引号**（PostgreSQL 只认双引号）。
- 设置项的 `type` 是行为声明不是展示标签（`string`/`bool`/`number`/`json`/`json_object`）：**五种类型在写入端都校验值形态**
  （`json` 双侧校验、`json_object` 额外要求"字符串到字符串的对象"（判据要与消费方同，P94）、`bool` 只收 `true`/`false` 且归一存储、`number` 必须是整数；**0 与负数合法**，形态校验不越界去管语义——P60）；
  **读取口按类型各只有一处**——bool 走 `db.SettingBool`、整数走 `db.SettingInt`（空/非整数/非正数三种"填了没生效"都出声；
  **0 有业务含义的键走 `db.SettingIntNonNeg`**，它只差合法下界，共用同一份实现——P63）。
- **配置填了却没生效要说一声，也要有人听**：上限类设置被兜底替换时经 `db.NoticeReplacedSetting` 出一条 ERROR，
  **同一种替换只喊一次**（读配置在热路径上，每请求一条会泡坏读数）；反向的那半句由构建兜：**seed 里每个键必须有后端读取方**
  （`make vet` 的 `settings-readers-check` 第一条），只给展示/脚本用的写进脚本 `EXEMPT` 并留理由（P53）。
  同一个脚本的**第二条**管读法：出现 `Atoi(db.GetSetting(` 或本地 `settingInt` 函数即红（P61）。
- **下发位置是部署事实，不做设置项**：书源 JSON 走静态托管 `GET /data/shuyuan/bookSource.json`（免鉴权、原样直出），
  `GET /user/import-config` 只回路径与 `ready`（判据是 `json.Valid`）；绝对地址由前端 `window.location.origin` 拼，
  不信 `X-Forwarded-Host`。旧的 `legado_import_url` 已进 seed 废弃键清单、启动硬删。
- **写入不许吞错，判据是「这张表是谁的读对象」**：不只留档表（`redemption_logs`）——**判定输入表**更严。
  `quota_usage_logs` 是 `UsedToday` 的读对象，写失败等于"这次没扣额度"，而请求 200、监控成功、面板偏小，
  没有任何调用方会报错（P88）。**已由 `make vet` 的 `ledger-write-check` 扫**：8 张判定/审计表的字面量写入口 +
  任何 `Updates/UpdateColumn(s)`，收尾行没有 `.Error` 即构建红。列宽按**用户输入的最坏形态**定（SQLite 不检查列宽，
  这类只在 MySQL 暴露）。**镜像的那一半**（P67）：面向用户的错误文案可以也应该是固定句，但**同一份原因必须落在
  服务端日志里**——"给用户的不泄"与"给运维的有声"是两份信息，只做其一仍是吞错。
- 前端复用既有组件与 `@layer components` 基样类；`reveal` 动画依赖观察时机（先 `loading=false` 再 `nextTick(revealObserve)`）。
- **共享件开的插槽要给自己宽度约束**（容器 `min-w-0 max-w-full`、内容自己 `flex-wrap`，**只改一头仍会溢出**）：
  这类窄屏错构建、用例、`css-check` 全都不报，只能按那个视口量一次（P65）。
- **自定义类写了就得有定义**（`btn-*`/`card`/`table-*`/`label`/`checkbox`…）：Tailwind 对不存在的类静默跳过，
  错类名不红构建、不红用例——**已由 `make vet` 扫**（`scripts/check-css-classes.sh`，P31）。
- 黑盒测试一律放 `test/`，集成用例禁止 `t.Parallel()`；改动完成后 `make build` 且必须 `git commit`。
- **断言写完做一次变异验证**：把被测改动反转跑一次，确认用例真的会红——绿灯只证明它没挡住现状，不证明它能挡住错误。
  **变异不红先怀疑断言空转**（用例里根本没有样本走那个分支），NULL/边界类断言让用例自己数样本。
- **机制性坑记在** [`docs/规范/踩坑判据.md`](docs/规范/踩坑判据.md)（症状 → 真因 → 一句可执行的判据 → 固化去处）；
  该页的核心不是收集而是**退出机制**：坑被**判据那一类形状**机器拦住之后删掉那一行——
  固化成"某一个实例的用例"不构成删行条件（档位的定义与那句问句见那一页的《去处》：
  **下一个还没写出来的违例会不会自己红**），今天那一页 55 条里只有 2 条到得了这一档。文档写过 ≠ 防住了。
- 详情：[`docs/规范/开发约定.md`](docs/规范/开发约定.md)


## 11. 安全与合规

- 凭证不进仓库/文档/测试夹具；`JWT_SECRET` 生产必须改（默认值 + 启用鉴权 = 拒绝启动）。
- SSRF 校验按来源区别对待：请求参数与用户配置严格校验，平台默认配置视为管理员可信来源，`FixedBaseURL` 源整体跳过。
- 阅读行为数据（搜索词/书名/章节/媒介）只经 `/admin/*` 出明细，公开端点只给聚合榜且窗口被钉死（维度白名单只有搜索词与书名）；号池凭证只出脱敏值；**设置项里的对外通道凭证同样只写不回显**（`db.SensitiveSettingKeys`，待办清单 P62：`GET /admin/settings` 命中名单出空串、写侧留空=保持原值，面板按随响应下发的 `sensitive_keys` 渲染密钥控件）。用户可以关掉留存（`users.content_consent`，默认同意），见 §9 与 P37。
- 代码按 AGPL-3.0 发布，使用条件见 `DISCLAIMER.md`；引入新依赖前确认许可证兼容。
- 详情：[`docs/规范/安全与合规.md`](docs/规范/安全与合规.md)

## 12. 已知取舍

- 处理器间传参用 `map[string]interface{}`；SSRF 的 DNS 结果永久缓存；配额计费按请求（含上游缓存命中）。
- `docs/归档/` 里的激进方案（unsafe 字段映射、工作窃取调度等）不落地。
- 无 CI：构建纪律靠 Makefile，部署靠脚本，分支纪律靠基线 tag 的三点 diff（tag 已推 gitee）。`main` 与骨架 tag 可由维护者推送；携带数据源的 `sources/*` 分支只进**私有远端**（git.syiism.cc.cd），不进公开远端。
- 详情：[`docs/规范/已知取舍.md`](docs/规范/已知取舍.md)

## 13. 文档与知识库的分工

- 三层：**本文件** = 索引与一句话结论；**`docs/`** = 现状性设计详解与待办清单（入库，随代码走）；**Obsidian 知识库** = Bug 排查过程、根因分析、方案权衡（不入库、不自动提交）。
- **遇到错误先查经验，再想方案**（默认动作，不是可选步骤）：报错、读数不对、行为反常时，
  先扫 [`docs/规范/踩坑判据.md`](docs/规范/踩坑判据.md)（**标题即结论**，按症状关键词搜一遍即可）
  与知识库的 Bug 关键词索引，命中就照它的判据改；两处都没有，才开始重新推导。
  理由不是「查资料比较礼貌」，是**重新推导会重复踩同一个坑，而且容易把症状当成真因**——
  这一页里「覆盖率一格只有 47%」那条，当初就是先查了采集、绕一整圈，真因却在另一个层。
  反过来，解决之后如果确认是新坑，按该页的格式补一条（症状/真因/判据/固化去处），
  别让它止于一次口头总结。
- **规范是产物，不是前提**：本页与 `docs/` 的约定都由经验长出来，也必须靠经验退役。五条经验源（踩坑 / 被纠正 /
  重复劳动 / 决策答复 / **主动巡检**）、升级阶梯（判据页 → 约定 → 机器拦截 → 删行）、收尾三件事与
  「巡检怎么算做过」见 [`docs/规范/踩坑判据.md`](docs/规范/踩坑判据.md) 的《经验的来路与去处》；
  硬约束是**先删后加**与**每次改规则都回链触发它的经验**——没有回链的规则半年后没人敢删。
  **第五源是唯一不会自己送上门的**：每轮清单做完必须抽一次源码找「不会自己报错的错」
  （静默跳过的类名与配置、两处写同一份值、`== nil` 判不出的半初始化），
  并写下查了什么、发现了什么、**没发现什么**——第三条决定下一轮是重查还是接力。
- 待办与已知问题记在 [`docs/规范/待办清单.md`](docs/规范/待办清单.md)，不在本文件展开；
  **该页只留未收口条目 + 一张索引表**，已收口的条目整条搬到
  `docs/归档/<治理轮>/`（一轮一个目录：`2026-10-治理轮/`＝P1~P21、`2026-10-维护轮/`＝P24~P27），逐字未改。
  引用某条时写「待办清单 P17」这类编号，**不要**复制链接或复述结论——索引负责「有没有这条、哪个提交收的口」，
  细节去归档页读，复述就会长出第二份事实来源。
- `docs/归档/` 只读，是历史阶段的方案与执行记录。
- 详情：[`docs/规范/知识库分工.md`](docs/规范/知识库分工.md)

