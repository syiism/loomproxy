# AGENTS.md — loomproxy-base 项目指南

> 面向 AI 编码代理的**索引页**：本文件只存条目与一句话总结，细节一律在 `docs/` 下按主题成页。
> **以代码为准**；文档与代码冲突时改文档。改完代码要同步的是 `docs/` 对应页，不是把细节写回本文件。
> 文档地图见 [`docs/README.md`](docs/README.md)。

## 1. 项目定位

- 一句话：上游接口的**代理与治理平台**——代理/内联第三方接口、归一异构响应，并做鉴权、计费、限流、监控与生命周期管理；底座不携带任何上游接口实现。
- Legado 只是内置的一种**下游输出契约**（`base/legado` 五动作），动作集与输出形状由接口包自己声明，底座不做书源特例分支。
- 接入一个源 = 新建 `sources/<源>` 包自注册 + `sources/all.go` 加一行空白导入 + `make build`；`app.go` 与 `db/seed.go` 免改。
- 详情：[`docs/架构/项目定位与能力清单.md`](docs/架构/项目定位与能力清单.md)

## 2. 技术栈

- Go 1.26（模块名 `loomproxy`，CGO 必需）+ Gin + GORM（SQLite/MySQL/PostgreSQL 由 `DB_TYPE` 切换）+ Redis；Vue 3 + Vite + Tailwind 面板经 `go:embed` 内置。
- 模块名与产物名是两件事：二进制、镜像、systemd 单元、部署路径恒为 `loomproxy-go`。
- 详情：[`docs/架构/技术栈.md`](docs/架构/技术栈.md)

## 3. 构建、运行与部署

- 唯一门禁是 `make build`（pnpm 前端 → `go vet` + `gofmt` → `go test -race` → 调试版与 strip 版两个产物）；前端必须先于 Go 构建。
- 部署走 `scripts/deploy.sh`（systemd 与 `--local` 两种模式）或 Docker；最小运行单元是「二进制 + 同目录 `.env` + `data/`」。
- 配置优先级：进程环境变量 > 工作目录 `.env` > 可执行文件目录 `.env`。
- 详情：[`docs/运维/构建与部署.md`](docs/运维/构建与部署.md)

## 4. 目录结构与模块边界

- 分层事实：`app/` 装配、`base/` 核心与获取栈、`base/legado/` DTO 与五基础处理器、`handlers/` 控制面、`sources/` 数据面、`middleware/`+`gate/` 链与闸门、`db/`+`models/`、`utils/`、`web/`、`test/`+`testkit/`。
- 两条硬边界：`middleware` 根包是叶子包（不得导入子包或 `gate`，否则成环）；`base` 不得反向导入 `base/legado` 与 `utils`。
- 详情：[`docs/架构/目录结构与模块边界.md`](docs/架构/目录结构与模块边界.md)

## 5. 处理器自注册与数据源声明位

- `base.RegisterSource(SourceMeta{...})` 是路由对账与 seed 播种的**唯一事实来源**；声明位含 `Actions`/`FixedBaseURL`/`SearchTabs`/`LegacyGroups`/`DataFiles`/`MediaType`。
- 需要新行为优先**补声明位**而不是改骨架；声明值非法在 `RegisterSource` 期 `log.Fatalf`。
- 路由全是根级 `/{source}/{action}`；链由中间件包的 `Def{Scope,Order,Applies,Build}` 装配，禁止在 `app.go` 手工拼链。
- **数据源分组**（`source_groups` + `data_sources.group_id`）只是归类与筛选视图：限额/计费/限速/套餐关联的键
  **一律仍是数据源码**，`POST /admin/source-groups/:id/apply-limits` 也只是批量写入口（落库每源一行）。
  一源至多一组，删组只把成员回落 NULL 不删源，组改动一律失效 `/datasources` 缓存。
- 集成测试只用 `testkit/fakesource` 的三个假源，不引入真实上游。
- 详情：[`docs/架构/处理器自注册与数据源声明位.md`](docs/架构/处理器自注册与数据源声明位.md)、
  [`docs/方案/数据源分组与首页额度看板.md`](docs/方案/数据源分组与首页额度看板.md)

## 6. 通用号池（base/pool）

- Provider 三钩子 `Create`/`Refresh`/`Claim`（**Claim 必须叠加无损**）+ 可选 `ResourceExpiredClassifier`；状态机 `hot`/`cold`/`spent`/`dead`，表 `pool_devices`。
- 现有形态是**墙钟燃烧型**：只保持 1 个活跃号 + N 个冷备，临期续领、用尽换号、错误驱动扩容。
- 底座不携带任何 Provider，号池列表为空是正常状态；面板读 `GET /admin/pools`（凭证只列键名）。
- 详情：[`docs/架构/号池框架.md`](docs/架构/号池框架.md)

## 7. 鉴权模型

- 两层：API 网关层（`middleware/apiauth` → `utils.VerifyAuth`，JWT 或 API Key）与用户层（`AuthRequired()`/`AdminRequired()` 查库）。
- 用户自助密钥（`lp_` 前缀）匹配时**注入归属身份**，计费/配额/监控/套餐门控随该用户生效；静态 env 键保持匿名语义。
- 会话由 JWT 的 `jti` 对应 `auth_sessions`，无 `jti` 的旧 token 一律 401；禁用/删除用户与改密都吊销会话。
- 详情：[`docs/架构/鉴权模型与会话.md`](docs/架构/鉴权模型与会话.md)

## 8. 弹性获取栈与环境变量

- 上游请求一律经 `base.BaseHandler`：短 TTL 缓存 → singleflight → 按 host 熔断 → 过期缓存降级，代理池与 UA 轮换在 `base.Fetch` 层；不要自建 http.Client。
- 全部配置是环境变量，权威定义在 `conf/conf.go`（含默认值）；`REDIS_*` 同时承载接口缓存与「标识→名称」命名缓存的跨重启持久化。
- 几个容易踩的默认：`REDIS_PASSWORD` 默认空（写死默认密码会让无密码 Redis 连不上）、`MONITOR_RETENTION_DAYS` 默认 0=永久保留、`DB_TYPE` 代码默认 mysql 而 `.env.example` 默认 sqlite。
- 详情：[`docs/架构/弹性获取栈与环境变量.md`](docs/架构/弹性获取栈与环境变量.md)

## 9. 管控：额度、限流、访问控制、监控

- 计费/访问控制/限流三轴在 `gate/`，顺序 access(400) → billing(500) → ratelimit(600)，`monitor(200)` 置于其前以覆盖 403/429。
- 限额优先级：用户数据源级覆盖（**追加语义**）> 套餐限额 > 不限；限流为套餐级 > 全局，窗口计数与固定间隔双口径并存。
- 监控明细 `api_call_logs` 带内容维度（搜索词/书名/章节/媒介/结果数）与标识列，名称靠命名缓存反查、可事后幂等回填；书名榜与章节榜只统计 `action='content'`。
- 详情：[`docs/架构/管控-额度限流与监控.md`](docs/架构/管控-额度限流与监控.md)

## 10. 开发约定

- 响应统一 `{code,msg,data}`；对下游错误文案必须脱敏（收口在 `handleError` + `sanitizeUpstreamMsg`）。
- 手写 SQL 的别名/列名必须避开 MySQL 8.0 保留字（SQLite 容忍、只有生产暴露），条件里的列名优先用 GORM map 形式让它加引号。
- 设置项的 `type` 是行为声明不是展示标签（`string`/`bool`/`number`/`json`），`json` 型前后端双侧校验。
- 前端复用既有组件与 `@layer components` 基样类；`reveal` 动画依赖观察时机（先 `loading=false` 再 `nextTick(revealObserve)`）。
- 黑盒测试一律放 `test/`，集成用例禁止 `t.Parallel()`；改动完成后 `make build` 且必须 `git commit`。
- 详情：[`docs/规范/开发约定.md`](docs/规范/开发约定.md)

## 11. 安全与合规

- 凭证不进仓库/文档/测试夹具；`JWT_SECRET` 生产必须改（默认值 + 启用鉴权 = 拒绝启动）。
- SSRF 校验按来源区别对待：请求参数与用户配置严格校验，平台默认配置视为管理员可信来源，`FixedBaseURL` 源整体跳过。
- 阅读行为数据（搜索词/书名/章节/媒介）只经 `/admin/*` 出明细，公开端点只给聚合榜且窗口被钉死；号池凭证只出脱敏值。
- 代码按 AGPL-3.0 发布，使用条件见 `DISCLAIMER.md`；引入新依赖前确认许可证兼容。
- 详情：[`docs/规范/安全与合规.md`](docs/规范/安全与合规.md)

## 12. 已知取舍

- 处理器间传参用 `map[string]interface{}`；SSRF 的 DNS 结果永久缓存；配额计费按请求（含上游缓存命中）。
- `docs/归档/` 里的激进方案（unsafe 字段映射、工作窃取调度等）不落地。
- 无 CI：构建纪律靠 Makefile，部署靠脚本，分支纪律靠本地基线 tag 的三点 diff。`main` 可由维护者推送，携带数据源的 `sources/*` 分支不推远端。
- 详情：[`docs/规范/已知取舍.md`](docs/规范/已知取舍.md)

## 13. 文档与知识库的分工

- 三层：**本文件** = 索引与一句话结论；**`docs/`** = 现状性设计详解与待办清单（入库，随代码走）；**Obsidian 知识库** = Bug 排查过程、根因分析、方案权衡（不入库、不自动提交）。
- 动手前先查知识库的 Bug 关键词索引；`docs/归档/` 只读，是历史阶段的方案与执行记录。
- 待办与已知问题记在 [`docs/规范/待办清单.md`](docs/规范/待办清单.md)，不在本文件展开。
- 详情：[`docs/规范/知识库分工.md`](docs/规范/知识库分工.md)
