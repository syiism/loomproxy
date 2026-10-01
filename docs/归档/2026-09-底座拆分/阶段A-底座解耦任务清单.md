# 阶段 A：底座解耦任务清单

## 执行状态（2026-09-30，落点改为 loomproxy-base）

清单原写的是「先在 loomproxy-go 做 A，再做 B」；base 侧已先完成底座化（提交 `31a6ca0`），
因此 A 直接在 **loomproxy-base** 执行。状态：

| 任务 | 状态 | 说明 |
|---|---|---|
| A2 baseUrl 声明化 | ✅ base 已完成 | `SourceMeta.FixedBaseURL`，中间件读声明 |
| A3 分类展示策略 | ✅ base 已完成 | `handlers/common` 的 category switch 已删，tab/files 统一渲染 |
| A4 平台源清单 | ✅ base 已完成 | `ListPlatformSourceConfigs` 改为查 `data_sources` 表 |
| A5 面板分类枚举 | ✅ base 已完成 | `web/src` 全域无源名与分类枚举 |
| A9 源侧 utils 搬迁 | ✅ base 已完成 | `utils/norm.go`（通用），源专有归一化随源移出 |
| A6 seed 去源名 | ✅ 本次完成 `4cca538` | 新增 `RETIRED_SOURCES`；`LegacyGroups` 声明驱动组行展开 |
| A1 声明位补齐 | ✅ 本次完成 `4cca538` | `SearchTabs` / `LegacyGroups` / `DataFiles` + 三项夹具用例 |
| A7 中间件出 handlers | ✅ 本次完成 `b526a85` | 新包 `gate/`（访问控制/计费/限流 + 限额解析） |
| A11 testkit 与假源 | ✅ 本次完成 `16cd26b` | `testkit/fakesource` + `cmd/fakegateway`；python 用例改打假源 |
| A8 sources 目录分层 | ✅ 本次完成 `6cc34e3` | 定为顶层 `sources/`：源包 `sources/<源>`、清单 `sources/all.go`；`handlers/common` → `handlers/catalog`；`handlers/` 专指控制面 |
| A10 注释与遗留文件 | ✅ 本次完成 `c01ac75` | 死文件 `web/app.js` + `web/styles.css` 整删（不在 embed、全仓无引用），AGENTS.md §10 的「不要动」同步移除 |
| 弹性 knobs（A1 未尽） | ⏳ 未做 | 超时/缓存 TTL/并发/风控冷却仍由 handler 字段自设，底座不感知——**目前无消费方**，等 fork 接上再说 |

判据 grep 现状：底座全域（Go/conf/env/面板/scripts/deploy）**源名 0 命中**（跑 grep 时记得排掉 `.qoder`，方案文档本身含源名）。

---

执行落点：**loomproxy-base 仓库**（单主线，无远端）。每任务一个提交，提交前 `make build` 全绿（AGENTS.md §10 硬要求）。
loomproxy-go 侧的旧分支模型（dev→public 同步）在阶段 C 起改为 fork 关系，见记忆文档 T2。

最终判据（A12 的验收，也是整个阶段的验收）：

```bash
# 底座候选路径内不得出现具体源名（注释/示例也要清）
grep -rnE '"(fq_tutu|fq_mufan|fq_xinghai|fq_luomu|fq_jingluo|qq_luomu|qm_luomu|sq_luomu|uxx)"' \
  app base conf db models utils handlers/admin handlers/catalog handlers/auth handlers/quota \
  handlers/userconfig handlers/apikey handlers/verify web/src scripts tools
# 期望：0 命中（源包目录与 fork 侧测试除外）
```

---

## A1 SourceMeta 扩展为唯一契约入口

- 目标：把「源需要底座知道的一切」收进 `base.SourceMeta`，后续任务只加声明、不加底座分支
- 新增声明项：baseUrl 需求（三态）、搜索 tab 集合、附属数据文件来源、上游弹性 knobs（超时/上游缓存 TTL/目录缓存/并发上限/风控冷却）、私有配置 key 清单、私有模型注册口、是否匿名可见
- 涉及：`base/registry.go`，各源包 `init()` 的声明块
- 要点：缺省值必须兼容现有行为（未声明 = 现状），否则 A2–A6 会连锁改测试
- 验收：`go build ./... && make vet`；`/endpoints` 与 `/datasources` 响应逐字段与改动前 diff 为空

## A2 baseUrl 判定声明化

- 目标：去掉 `app/middleware.go:123` 的 `skipBaseURLCheck := sourceName == "uxx"`
- 做法：中间件按 `GetSourceMeta(source).NeedsBaseURL` 决策
- 验收：grep `"uxx"` 在 `app/` 内 0 命中；`test/auth_integration_test.go` + 计费主链路用例全绿

## A3 分类展示策略声明化

- 目标：拆掉 `handlers/common/datasource.go` 的 `switch ds.Category { case "fq"/"uxx" }`
- 做法：tab 与 files 一律取自源声明；`listDataFiles` 的数据目录由声明指定，缺省空
- 连带：`web/src/pages/Datasources.vue:118` 缺省路径 `/qq_luomu/search` 改为取列表首项
- 验收：`curl /datasources` 输出与改动前 diff 为空（本次同步已把 fq 统一为默认 tab，作为基线）

## A4 平台源清单去硬编码

- 目标：`handlers/admin/admin.go` 的 `ListPlatformSourceConfigs` 里那份源名+显示名硬编码清单删掉
- 做法：由 `base.DeclaredSources()` 派生，只保留声明了「平台默认 baseUrl 可配」的源
- 验收：面板「平台配置」页显示的源集合与改动前一致；admin 集成用例绿

## A5 面板分类枚举去硬编码

- 目标：`web/src/pages/admin/Datasources.vue`（两处分类下拉）、`admin/Quotas.vue`、`pages/Datasources.vue` 不再枚举 `fq/qq/qm/sq/uxx`
- 做法：分类候选由目录接口下发（`/datasources` 或新增 admin catalog 响应字段）；前端只做透传渲染
- 验收：`grep -n "番茄系\|七猫\|书旗" web/src/pages/admin/*.vue` 0 命中；`cd web && pnpm build` 通过

## A6 seed 与历史迁移去源名

- 目标：`db/seed.go` 不再出现源名清单
- 三处分别处理：
  1. 三档套餐逐源额度行 → 由 provider 按声明源集合生成（额度数值走源声明或 base 默认档）
  2. `removedSources` → 改为注入机制（底座只提供清理能力，清单归部署侧）
  3. `legacyGroupSources`（历史组码迁移，本质是"本部署的源变迁史"）→ 随 fork 走，底座只保留 `migrateGroupCostRows` 机制
- 验收：`grep -nE 'fq_|_luomu|"uxx"|xmly' db/seed.go` 0 命中；新库从零 seed 后 `/datasources`、`/quota/*` 行为与基线一致

## A7 计费/限流中间件出 handlers

- 目标：`handlers/quota/middleware.go` 的三轴闸门（访问控制/计费/限流）移出 handlers 目录，与 `handlers/*` 端点包分离
- 建议落点：顶层 `middleware/`（或 `app/middleware/`，与 autoblock 同处）
- 要点：`/quota/*` 端点（额度面板、用量流水）仍是控制面 handler，**只搬中间件，不搬端点**
- 验收：`app/app.go` 中间件链组装路径可读；`go test ./test/ -race` 全绿

## A8 目录分层：sources 与 catalog

- 目标：数据面与控制面在目录上表达
  - `handlers/{fq_*,qq_luomu,qm_luomu,sq_luomu,uxx}` → `sources/…`
  - `handlers/all` → `sources/all`（导入清单即源集合）
  - `handlers/common` → `handlers/catalog`（`/datasources`、`/data` 是能力发现端点，不是数据源）
- 连带：AGENTS.md §4/§5 目录图与注册纪律段、`docs/架构分析.md` 分层图同步改写
- 要点：**纯目录移动 + import 改路径**，不改任何注册名与路由；`handlers` 一词此后专指控制面
- 验收：`git diff --stat` 中源包文件为 rename；`/endpoints` diff 为空
- **落地结果（`6cc34e3`）**：base 侧无源包可搬，实际改动是 `handlers/all/all.go` → `sources/all.go`
  （package `sources`，`app.go` 改为空白导入 `loomproxy/sources`）+ `handlers/common` → `handlers/catalog`
  （含 4 个引用文件的 `common.` → `catalog.`）。真实源目录的迁移落在 fork 侧（记忆文档 T1 的接缝清单）。

## A9 源侧 utils 搬迁

- 目标：`utils/fq_utils.go`、`utils/uxx_utils.go` 离底座
- 依据（已核实）：控制面（app/auth/apikey/quota/admin/userconfig/verify/common）只用到 `utils` 的鉴权、JWT、缓存、SSRF、device、apikey；不触碰归一化函数
- 做法：归一化公共部分 → `sources/shared`，源专有部分 → 源包内；底座 `utils` 留通用能力
- 验收：`grep -rl "fq_utils\|uxx_utils\|BuildBookItem\|ParseUxx" base app conf db models utils` 0 命中

## A10 注释、示例与文档措辞去源名

- 目标：清低危泄漏，避免 fork 后误导——`base/proxy.go`、`base/base.go`、`models/api_call_log.go`、`models/quota.go`、`handlers/admin/quotas.go:338`、`web/src/components/UiTrendChart.vue:70`
- 做法：示例改用抽象名（`<source>/<action>`）或骨架自带的假源
- 验收：开头那条 grep 判据在全部底座路径 0 命中

## A11 testkit 抽出与源清单测试归 fork

- 目标：`test/testserver_test.go` 的临时 SQLite + 完整 gin 引擎 + 假上游能力抽成可复用包（`testkit`），供 fork 侧源测试 import
- 归类：逐源动作清单（`test/python/test_quota.py` 的 9 源表、`db/seed.go` 相关断言）属 fork 侧；base 侧只保留「假源跑通全链路」的契约用例
- 验收：base 侧测试不含任何真源名；fork 侧源测试用同一 testkit

## A12 阶段验收

1. ✅ grep 判据（本文开头那条）0 命中
2. ✅ `make build`（前端 + vet + `go test ./test/ -race` + 双二进制）全通过
3. ✅ 起服务实跑（2026-09-30，用**产品二进制** `./loomproxy-go` 起在 127.0.0.1:18081 + 临时 `DATA_DIR`）：
   全新库从零 seed 无报错，`/datasources` → `{"code":0,"data":[]}`（底座不带源，空列表是正确状态），
   `/data` → `{"count":0,"sources":{}}`，`/panel/` → 200，`/endpoints` → 401（AdminRequired，符合预期），
   启动日志 0 条 WARNING/路由对账告警，缓存预热完成。
4. ⚠️ **归 fork 侧**：逐源冒烟（`/fq_tutu/search` … search→detail→chapter→content）在 base 上不可能跑——
   底座零源。由 `cmd/fakegateway` 的假源链路 + `test/python` 18 例覆盖管线，真源冒烟随记忆文档 T1 在 loomproxy-go 侧做。
5. ✅ 提交切分：A1–A11 每任务独立提交（A8 `6cc34e3`、A10 `c01ac75`、模块改名 `ec007d9`），便于后续往 base 提契约变更

## 完成后才进入 B

阶段 A 的产出（扩好的 SourceMeta + testkit + 零源名底座）就是交给 `loomproxy-base` 的内容。A 未过验收不要开始拆仓——否则硬编码会被复制成双份。
