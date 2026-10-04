# loomproxy-base

LoomProxy 的 **Go 底座项目**——一个**上游接口的代理与治理平台**：代理或内联第三方接口、把异构响应归一成统一形状，并对接口做鉴权、计费、限流、监控与生命周期管理。本分支**不携带任何上游接口实现**，只保留平台能力；按约定写一个接口包、加一行导入，即可拿到完整的取数、管控与面板能力。

内置的下游形态之一是 **Legado（阅读 App）书源协议**——标准动作 search / detail / chapter / content / explore / recommend 与小说书源的字段归一化都在 `base/legado` 与 `sources/` 约定里，但它是**一种输出契约，不是项目的边界**：同样可以代理音视频、漫画、元数据或任何 HTTP 接口。

## 功能特性

- **接口框架**：处理器自注册（`base.Register` + `base.RegisterSource`），路由为根级 `/{source}/{action}`；动作集与输出契约由接口包自己声明（Legado 五动作只是内置的书源形态），字段归一化在包内做
- **用户系统**：注册、登录（JWT + bcrypt + 会话/设备管理）、找回密码、角色（admin / vip / user）、用户自助 API 密钥、额度计划与卡密兑换升级
- **数据源管理**：数据库驱动的动态数据源 CRUD、套餐授权（限额即授权）、平台/用户两级 baseUrl 配置与按套餐访问控制
- **额度计费与限流**：按接口扣减当日额度（北京时间零点重置）、套餐级速率限制（固定间隔 / 滑动窗口两种口径）、用量流水与管理端审计
- **内嵌管理面板**：`/panel` 下的 Vue 3 单页应用（用户 / 角色 / 系统设置 / 额度套餐 / 接口管理 / 卡密 / IP 拉黑 / 调用监控 / 号池），随二进制一起发布
- **稳定性与安全**：LRU+TTL 内存缓存与 Redis 缓存、上游响应短 TTL 缓存、singleflight 请求合并、按上游 host 熔断与过期缓存降级、IP 代理池（静态列表 / 文件 / 动态 API 三来源）、UA 轮换、SSRF 防护、IP 黑名单与自动拉黑、登录防爆破
- **通用号池**（`base/pool`）：凭证/额度型上游资源（设备号、账号、临时 token）的冷热分离框架——活跃号最小化、临期自动续领、耗尽自动换号、错误驱动扩容、状态面板；数据源实现 `Provider` 接口即可接入

## 接入一个数据源

```go
// sources/mynovel/mynovel.go
func init() {
    base.RegisterSource(base.SourceMeta{
        Code: "mynovel", Display: "我的小说源", Category: "novel",
        Actions: []string{"search", "detail", "chapter", "content"},
    })
    base.Register("mynovel_search", newSearchHandler, 10, nil)
    // ...
}
```

1. 新建源包，内嵌 `base/legado` 的基础处理器（或 `base.BaseHandler`）实现 `base.Handler`；
2. `sources/all.go` 加一行空白导入；
3. `make build`。

路由对账与 seed 播种（`data_sources` / `quota_costs` / `quota_limits` / 套餐关联）全部以源包声明为准，`app.go` 与 `db/seed.go` 免改。需要凭证池的源用 `pool.Register(pool.New(provider, cfg))` 登记自己的号池。详见 [AGENTS.md](AGENTS.md) §5、§6。

## 技术栈

- **后端**：Go 1.26 + Gin + GORM（SQLite / MySQL / PostgreSQL，由 `DB_TYPE` 切换；SQLite 启用 WAL + 单连接规避并发写锁）+ JWT + Redis（可选，连接失败自动回退纯内存）
- **前端**：Vue 3 + vue-router + Vite + Tailwind CSS 3（pnpm），经 `go:embed` 打进二进制
- **部署**：Makefile 一键构建 / 裸机升级脚本 / 多阶段 Dockerfile / docker compose

## 快速开始

环境要求：Go 1.26+（CGO 工具链，因 SQLite 依赖 gcc）、Node.js 18+ 与 pnpm。

```bash
cp .env.example .env      # 按需修改配置，见下方「配置」
make build                # 前端构建 → go vet + gofmt → go test -race → 编译
./loomproxy-go            # 默认监听 0.0.0.0:8081
```

启动后验证：

```bash
curl http://localhost:8081/              # JSON 状态页
curl 'http://localhost:8081/datasources' # 数据源列表（底座为空列表，接入书源后自动出现）
curl 'http://localhost:8081/data'        # 数据源静态 JSON 字典
```

管理面板：浏览器访问 `http://localhost:8081/panel`。首个管理员由 `ADMIN_USERNAME` / `ADMIN_PASSWORD` 引导创建（未设置密码时会生成随机密码并打印日志）。

## 部署

**裸机一键升级**（构建 → 备份 → 切换 → 冒烟 → 失败自动回滚）：

```bash
sudo scripts/deploy.sh        # systemd 模式：安装到 /opt/loomproxy 并重启 loomproxy.service
scripts/deploy.sh --local     # 原地模式：仓库目录就地切换（pkill + nohup，免 systemd）
scripts/deploy.sh --skip-tests # 跳过 vet+test（紧急发版）
```

**Docker**（多阶段构建，node 构建前端 → golang CGO 编译 → bookworm-slim 运行时）：

```bash
docker build -t loomproxy-go .
docker run -d --name loomproxy --env-file .env -p 8081:8081 -v loomproxy-data:/app/data loomproxy-go
# 或使用 compose（.env 注入 + ./data 绑定挂载持久化）
docker compose up -d --build
```

最小部署单元：**二进制 + 同目录 `.env` + `data/` 目录**。

## 配置

全部通过环境变量配置（12-factor 风格）：进程环境变量 > 当前工作目录 `.env` > 可执行文件目录 `.env`。完整键列表与默认值见 [`.env.example`](.env.example)，代码定义见 `conf/conf.go`。常用项：

| 变量 | 默认 | 说明 |
|---|---|---|
| `SERVER_PORT` | 8081 | 监听端口 |
| `DB_TYPE` | sqlite | sqlite / mysql / postgres（注意：`conf.go` 代码默认值为 mysql，`.env.example` 与镜像默认 sqlite） |
| `AUTH_ENABLED` | false | 开放模式（免登录不计费）或鉴权模式（JWT + API Key，启用计费） |
| `API_KEYS` | 空 | 静态 API Key（逗号分隔），用于服务间调用 |
| `JWT_SECRET` | 见 example | **生产必须修改** |
| `JWT_EXPIRE_HOURS` | 168 | Token 有效期；系统设置/用户个人设置可覆盖，-1 表示永不过期 |
| `REDIS_ENABLED` | true | Redis 缓存，连接失败自动回退纯内存 |
| `UPSTREAM_CACHE_TTL` | 10 | 上游响应短 TTL 缓存（秒），<=0 禁用 |
| `UPSTREAM_PROXIES` / `UPSTREAM_PROXY_FILE` / `UPSTREAM_PROXY_API` | 空 | IP 代理池三个来源，可叠加；`proxy_enabled_sources` 系统设置控制哪些接口走代理 |
| `POOL_ENABLED` / `POOL_COLD_SPARES` / `POOL_MAX_HOT` / `POOL_RENEW_BEFORE_SEC` | true / 2 / 3 / 300 | 通用号池运行参数（`base/pool`，详见 AGENTS.md §6） |
| `ADMIN_USERNAME` / `ADMIN_PASSWORD` | 空 | 首个管理员引导 |
| `DATA_DIR` | data | 运行时数据目录（SQLite、JSON 配置文件） |

## API 概览

统一响应格式为 `{"code": 0, "msg": "...", "data": ...}`，`code === 0` 表示成功。

| 路由 | 说明 |
|---|---|
| `GET /` | 服务状态页 |
| `GET /datasources` | 数据源列表（按用户套餐过滤，Redis 缓存，管理端变更写时失效；`AUTH_ENABLED=true` 时需带凭证——它不在默认 `AUTH_WHITELIST` 里） |
| `GET /data`、`GET /data/:source/:name` | 数据源静态 JSON 文件 |
| `/{source}/{action}` | 标准动作：search / detail / chapter / content / explore 等 |
| `/auth/*` | 注册、登录（用户名或邮箱）、找回密码、个人信息、登录设备会话管理 |
| `/user/*` | 个人 baseUrl 配置、导入书源、卡密兑换（需 JWT） |
| `/quota/*` | 额度面板、用量流水（需 JWT） |
| `/announcement` | 站内公告（管理员在系统设置中维护） |
| `/verify/*` | 场景化验证码（默认全部关闭，接入发码平台零代码） |
| `/admin/*` | 管理接口：用户 / 角色 / 设置 / 套餐 / 卡密 / 数据源 / IP 黑名单 / 调用监控 / 号池 |
| `/debug/pprof/*` | Go 运行时 profiling 端点（heap/goroutine/trace 等，按管理接口保护） |
| `/panel` | 内嵌管理面板（Vue 3 SPA） |

## 目录结构

```
main.go            入口：信号处理，优雅停机
app/               gin 引擎装配、路由注册、IP 自动拉黑、监控中间件
base/              核心：Handler 接口与注册表、HTTP 客户端、缓存/熔断/singleflight、代理池
base/legado/       Legado 响应 DTO 与五个基础处理器
handlers/          控制面 HTTP 端点（auth / admin / catalog / quota / userconfig / apikey / verify）
sources/           数据面：各源包目录 + all.go（数据源导入集合，底座为空）
base/pool/         通用号池框架（冷热分离状态机 + Provider 接口，见 AGENTS.md §6）
models/            GORM 模型
db/                数据库初始化与 seed（角色/设置/套餐/管理员引导）
conf/              环境变量配置加载
utils/             鉴权、JWT、缓存、SSRF 防护等工具
web/               Vue 3 前端（src/）与构建产物（dist/，被 embed）
test/              黑盒测试（单元 + HTTP 集成）
docs/              中文设计文档（迁移方案与实现过程记录）
scripts/deploy.sh  裸机一键升级脚本
deploy/            systemd 单元文件
```

## 开发

```bash
make web      # 仅构建前端（必须先于 go build，dist 缺失时 embed 会 Fatal）
make test     # go test ./... -race -count=1 -timeout 20m（跑全部包：包内单测不被门禁跑到就等于没写）
make vet      # go vet + gofmt 检查
make clean    # 清理二进制与 web/dist
```

- 测试集中在 `test/` 目录（黑盒测试，只测公开 API），含 `httptest` 单元测试与完整 gin 引擎的 HTTP 集成测试；假数据源夹具在 `testkit/fakesource`（Go 用例经其 init 登记，`test/python/` 的 pytest 移植版经 `cmd/fakegateway` 起子进程）
- 日志与界面文案使用中文，代码标识符用英文
- 提交前至少运行 `go vet ./...` 与 `gofmt` 检查

## 安全注意事项

- **生产环境必须修改默认 `JWT_SECRET`**；`.env` 中的 `API_KEYS`、`ADMIN_PASSWORD` 属敏感信息，不要写入仓库或文档
- 平台级默认 baseUrl 视为管理员可信来源（允许指向内网地址用于同机部署数据源）；用户自配 baseUrl 严格 SSRF 校验（拒绝回环/私有/保留 IP）
- CORS 当前为 `*`（面向阅读 App 的开放 API 场景），收紧前需评估对书源客户端的影响

## 许可与免责声明

- 代码以 **AGPL-3.0** 发布，全文见 [LICENSE](LICENSE)；Copyright © 2026 syiism。
  选 AGPL 而非宽松协议是有意的：本骨架的价值在于「拿它对外提供服务就得回馈改动」，
  同时版权持有人仍可另行授权（包括对携带数据源的私有分支另作安排）。
- 使用前请先读 [DISCLAIMER.md](DISCLAIMER.md)：本骨架**不携带任何上游接口实现，也不托管或分发内容**——
  代理哪些接口、携带哪些数据源由部署者自行决定并自负合规责任；上游接口随时可能变更或封禁，不作可用性担保；
  若用于代理阅读类接口，`api_call_logs` 会记录用户的阅读行为数据，对外部署前必须设定保留天数与访问控制。
- 名称与标识「LoomProxy / loomproxy-base」不随本许可授予。

## 相关文档

- [AGENTS.md](AGENTS.md) — 面向 AI 编码代理的项目指南（架构细节、开发约定、已知缺口，内容最全）
- [docs/](docs/) — 中文设计文档：迁移方案、实现过程记录、数据源性能测试报告；`架构分析.md` 从分层、请求管线、弹性获取栈、管控三轴等视角对现状架构做整体分析
