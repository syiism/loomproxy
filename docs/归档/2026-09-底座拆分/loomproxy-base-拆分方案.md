# loomproxy-base 拆分方案

状态：**阶段 A 已在 base 完成（含 A8/A10 收尾），B 的 base 侧改动已落地并实跑验收**（见任务清单 A12）。唯一剩余主线是 C：loomproxy-go 改为 base 的 fork。本文只定边界与纪律，不含代码。
决策已定：**fork 模板** + **单一 sources 仓库** + **源包落顶层 `sources/`**。

## 0. base 现状核对（2026-09-30）

`loomproxy-base` 已完成底座化（`31a6ca0` 起），与本方案有四处出入，前三条现已收口，记录在此以免后判错：

1. ✅ **模块名已改**：`go.mod` 现为 `loomproxy`（`ec007d9`，72 个 `.go` 的 import 前缀同步，`.go` 内无非 import 形式的模块路径字面量）。**产物名与部署链路仍保持 `loomproxy-go`**（Makefile `BINARY`、Dockerfile、`deploy/loomproxy.service`、`scripts/deploy.sh`、镜像名、`.gitignore`）——那些是线上部署路径，不随模块名动。
2. **通用号池进了底座**（`base/pool` + `models.PoolDevice` + `pool_devices` 表 + `/admin/pools` + 面板「号池」页），以 `Provider` 接口让源登记自己的池。本方案原写「源私有状态归 fork」——base 的做法更好：冷热/续领/错误驱动扩容是与协议无关的能力，配置也统一成了 `POOL_*`。保留此形态。
3. ✅ **源包目录已按本方案落地**（`6cc34e3`）：顶层 `sources/<源>`，导入清单 `sources/all.go`（package `sources`），
   `handlers/common` 更名 `handlers/catalog`，`handlers/` 此后专指控制面。base 侧当时已无源包，
   真实源目录的迁移落在 fork 侧（loomproxy-go 的源仍在 `handlers/<源>`，merge 时一并搬）。
4. **测试夹具已就位**（`test/fakesource_test.go` 三假源），但跨进程用例曾整批失效——本次 A11 已提为 `testkit/fakesource` + `cmd/fakegateway`。

## 1. 目标与判据

把 loomproxy 拆成「可复用网关骨架」与「数据源集合」两层，让公开版不再靠人工剔除私有源。

唯一硬判据：**底座（base 侧）代码里不得出现任何具体数据源名字**。
出现即说明契约缺位，fork 被迫改底座——那是这套方案最先崩的地方。

反例（本次 dev→public 同步时逐个撞到的真实形态）：

| 位置 | 形态 | 性质 |
|---|---|---|
| `db/seed.go` | `removedSources`、`legacyGroupSources["fq"]`、三档套餐逐源额度行 | 逻辑 |
| `handlers/common/datasource.go` | `switch ds.Category { case "fq" / "uxx" }` 选 tab 与数据文件 | 逻辑 |
| `app/middleware.go:123` | `skipBaseURLCheck := sourceName == "uxx"` | 逻辑 |
| `handlers/admin/admin.go` | `ListPlatformSourceConfigs` 内一份硬编码源清单（源名+显示名） | 逻辑 |
| `web/src/pages/admin/Datasources.vue` | 分类下拉 `fq/qq/qm/sq/uxx` 枚举（两处） | 逻辑 |
| `web/src/pages/Datasources.vue:118` | 缺省路径写死 `/qq_luomu/search` | 逻辑 |
| `utils/fq_utils.go` / `utils/uxx_utils.go` | 源侧归一化实现住在底座 utils | 归属 |
| `base/proxy.go`、`models/*.go`、`handlers/admin/quotas.go:338`、`UiTrendChart.vue:70` | 注释/示例里举源名 | 低危，会误导 |

## 2. 仓库角色

```
loomproxy-base            公开骨架，一条主线，无 dev/public 之分
  ├─ fork: loomproxy           公开版 = base + 番茄五源 / 落幕系三源 / uxx
  └─ fork: loomproxy-private   私有版 = 公开版全部源 + xmly / fq_hg
```

- **base 必须零源也能跑**：服务起得来、面板可用、鉴权/计费/限流/监控/卡密全通，`/datasources` 返回空列表，并内置一个**假源**作为契约测试与 CI 夹具。做不到这点，fork 无法把"底座改动"与"源改动"分开验证。
- **fork 的 `sources/` 是唯一的源容器**，加一个源 = 加一个包 + 一行导入清单。
- **公开/私有的源重复**：`sources/` 作为可搬运单元，用 `git subtree` 从公开 fork 单向拉进私有 fork。不在此阶段做成独立 Go module（避免同时背 fork 与依赖两套版本对齐）。等搬运频率到两周一次以上，再考虑升级成 module。
- **收益**：dev→public 的人工剔除（本次 101 文件的活）不再是常规操作；能力改进的正确方向是提回 base，两个 fork 各自升级。

## 3. 归属切分

| 归 base（底座） | 归 fork（源侧） |
|---|---|
| `base/`（注册表、HTTP 客户端、缓存/熔断/singleflight、代理池、proxy 门控） | 各源包（`sources/fq_tutu` … `sources/uxx`） |
| `app/`（gin 装配、路由挂载、中间件链、监控、pprof、优雅停机、自动拉黑） | 源私有归一化管线（现 `utils/fq_utils.go`、`utils/uxx_utils.go`） |
| 控制面：admin / auth / apikey / userconfig / verify / catalog(原 common) | 源私有凭据与配置默认值、源私有数据文件 |
| 计费/限流/访问控制中间件（现 `handlers/quota/middleware.go`） | 源私有表与迁移（设备池、会话池一类） |
| `conf` / `db`+`models` / `lifecycle` / `utils`（鉴权、JWT、缓存、SSRF、device、apikey——已核实控制面不依赖源侧归一化函数） | 源的集成测试与契约回归（如逐源动作清单） |
| `web/` 面板（不含任何源专有页面） | 部署品牌：`deploy/`、`.env.example`、README |
| `base/legado` DTO | 源私有运维页（**明确不进公开面板**） |

## 4. SourceMeta 契约补齐（阶段 A 的核心产出）

fork 模型下加声明位需要"提回上游"的成本，所以一次想全。现字段 `code / display / category / actions / sort_order / status` 之外，源还需要：

1. **baseUrl 需求**（三态：不需要 / 用户可配 / 平台默认必填）——替代 `app/middleware.go` 的源名判断
2. **分类展示策略**：搜索 tab 集合 + 附属数据文件来源——替代 `case "fq"` 式 switch
3. **上游弹性 knobs**：超时、上游缓存 TTL、目录级缓存、并发上限、风控冷却——今天散在各 handler 私有字段
4. **私有配置 key 清单**：key 名 + 默认值由源侧提供，底座只透传 env，`conf` 结构体不得出现源字段（历史上 `XMLY_APP_KEY` / `XMLY_APP_SECRET` 因此进了公开仓库历史提交）
5. **私有模型/迁移注册口**：底座不承诺这些表的兼容性，并在文档里写明
6. **是否公开可见**（可选）：匿名 `/datasources` 白名单口径

面板扩展点**不做插件化**。注册式前端扩展要付出整套组件契约与版本协商，收益只是省一个私有运维页，而这类页面天然是私有物。

## 5. fork 纪律

- **受保护路径**：base 全部文件默认不可改。允许改动清单固定为：`sources/`、源导入清单、`data/`、`deploy/`、`.env.example`、品牌文案。
- **CI 校验**：对 upstream tag 做路径级 diff，受保护清单之外出现改动即失败。靠机器，不靠自觉。
- **冲突位置即诊断信号**：merge base tag 时冲突若落在受保护清单外，说明该 fork 违规改了底座——先把改动提回 base，再继续 merge。
- **契约向后兼容**：base 的 `SourceMeta` 与 `base/legado` DTO 是两侧唯一公共 API，变更须向后兼容一个 tag 周期，并打 tag 记录。
- **schema 版本化**：base 带 schema 版本号与向前迁移；fork 不得新增公共表。否则各 fork 的 SQLite 库会因升级节奏不同而不一致。
- **同步节奏**：base 打 tag → fork 定期 merge，不允许长期落后。

## 6. 阶段划分

| 阶段 | 内容 | 仓库 | 前置 |
|---|---|---|---|
| A | 底座解耦：消灭源名硬编码、SourceMeta 补齐、中间件出 handlers、抽 `testkit`、源侧 utils 搬迁 | 先在 loomproxy-go 做 | 无（唯一现在可动） |
| B | base 成型：删源目录、内置假源、零源可运行验收、受保护路径 CI | loomproxy-base | A 完成 |
| C | fork 关系建立：loomproxy-go 设 upstream、merge 骨架、源放回 `sources/` | loomproxy-go | B 完成 |
| D | 私有 fork 另立，公开源 subtree 单向搬运，定节奏 | loomproxy-private | C 完成 |

**顺序不可反**：先拆仓再做 A，会把 `case "fq"` 这类硬编码复制进两个仓库，形成比现在更难收拾的双份债。

## 7. 风险清单

- **底座 drift**：fork 落后导致 DTO/SourceMeta 版本错位。→ 兼容窗口 + tag 纪律
- **双份前端**：面板改动没提回 base 会迅速分叉。→ 面板不提源，私有页不进公开面板
- **凭据复发泄露**：→ 底座 conf 禁源字段；`.env.example` 源段落由源侧维护；加 pre-commit 密钥扫描
- **subtree 搬运冲突**：→ 搬运只单向（公开→私有），不做双向
- **拆分后 dev/public 双分支语义重叠**：fork 边界已表达公开/私有，旧分支模型应在 C 之后废弃，避免两套机制并存

## 8. 契约测试设计

- base 提供 `testkit`：临时目录 SQLite + `app.CreateApp()` 完整 gin 引擎 + `httptest` 假上游（即现 `test/testserver_test.go` 的思路抽出）
- base 自带**最小假源**，跑通鉴权 → baseUrl 解析 → 计费 → 限流 → 代理门控 → 监控落库全链路
- 每个真源在自己的 fork 里用同一 testkit 跑同一份契约清单；源的逐动作回归（如现 `test/python/test_quota.py` 里的 9 源动作表）归 fork，**不进 base**
