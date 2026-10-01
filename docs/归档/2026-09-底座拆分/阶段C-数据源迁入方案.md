# 阶段 C：数据源迁入骨架（在 loomproxy-base 开新分支）

> 状态：**已执行**（分支 `sources/private`，2026-09-30 完成私有版迁入）。锚点 = `loomproxy-base@main` 的 `31f0c91`（中间件按职责拆包自注册 + gate 按轴拆 5 文件）。
> 供体 = `/home/muyang/work/modelscope/loomproxy-go@dev`（`ff53974`）。
> 执行本方案前先读同目录 `记忆-底座拆分接续.md`（§3 已定决策、§4 SourceMeta 契约、§4b 装配层）与 `loomproxy-base-拆分方案.md`（边界与纪律）。

## 0. 目标与硬约束

把带数据源的 loomproxy 重建为「骨架 + 6 个源包」，且**骨架保持不变**：

- **宿主是当前仓库** `/home/muyang/work/loomproxy-base`——从 `main` 开新分支承载源，`main` 永远是零数据源骨架。
- **只提交，不推送**。`donor` remote 与 `skeleton-v1` tag 只存在于本地；骨架仓库本就无远端（AGENTS.md §12），这天然保证误推不了。
- **`loomproxy-go` 仓库零改动**：不加 remote、不打 tag、不切分支、不 push。`dev` 原地冻结只读，`public`/`feat/fq_hg` 不动不删。
- 本方案只做结构迁移与形态补齐，**不改任何上游协议实现**（签名、归一化、抓取逻辑原样搬）。

## 1. 两侧形态差异（开工前必读，否则会按 dev 的形状找文件）

dev 的骨架比想象中旧——自骨架分叉以来 base 连做了 6 次结构改动。对应关系：

| dev 现状 | 骨架现状 | 迁移动作 |
|---|---|---|
| 模块名 `loomproxy-go` | 模块名 `loomproxy`（`ec007d9`） | 迁入文件逐行改 import 前缀；**产物名仍是 `loomproxy-go`**，不要"顺手统一" |
| `handlers/<源>` | `sources/<源>`（`6cc34e3`） | `git mv` + 改包名/import |
| `handlers/all/all.go`（package `all`） | `sources/all.go`（package `sources`） | 重写为 6 行空白导入 |
| `handlers/common/` | `handlers/catalog/` | 不迁移（骨架已有，逻辑已声明化） |
| `handlers/quota/middleware.go`（849 行合一） | `gate/{access,billing,ratelimit,plan,usage,doc}.go` + Def 自注册（`31f0c91`） | 不迁移 |
| `app/middleware.go`、`app/autoblock.go` | `middleware/{transport,ipblock,apiauth,source}` 四包 + Def 自注册 | 不迁移；dev 里这两文件的源硬编码随之消失 |
| `models.XmlyDevice` + 自研池 | `models.PoolDevice` + `base/pool` 框架 | 见 §5 |
| `conf` 里 `XMLY_*`/`FQHG_*` 键 | `conf` 无源知识 + 通用 `POOL_*` | 见 §6 |
| 仍跟踪 `web/app.js`、`web/styles.css` | 已删（`c01ac75`） | **不要把这两个 vanilla 文件带回分支**（`git checkout donor/dev -- web/...` 是禁手） |

一句话执行纪律：**只有源侧路径从 dev 取，平台文件一律用骨架的**。这既是零冲突的来源，也是审计判据成立的前提。

## 2. Git 操作序列

### 2.1 为什么不在 loomproxy-go 侧 merge 骨架

两仓**没有任何共享对象**（`git rev-list` 交叉 `git cat-file` 零命中）。把骨架 merge 进 dev 即全树 add/add 冲突（平台两侧同名文件几乎都不同），若为省事加 `-X theirs`，会**静默删掉** dev 独有的接缝内容且不留痕迹：`conf/conf.go:255-267`（XMLY/FQHG 全部读取点）、`db/seed.go:124-147`（per-plan 限额）、`:459` 起（海阔视界→uxx 一次性更名）、`app/app.go:640-645`（xmly 号池启动）。反过来把骨架当宿主、dev 当只读供体，`checkout` 定向取内容 → 零冲突，且骨架成为分支的祖先，以后 `git merge main` 是普通合并。

### 2.2 命令

```bash
cd /home/muyang/work/loomproxy-base
git status -sb                                  # 必须干净
git tag skeleton-v1 31f0c91                     # 仅本地
git remote add donor /home/muyang/work/modelscope/loomproxy-go
git fetch --no-tags donor dev                    # → donor/dev = ff53974

# 建议在 worktree 里干活，main 保持可随时对照与构建
git worktree add ../loomproxy-with-sources -b sources/private skeleton-v1
cd ../loomproxy-with-sources

# 定向取供体内容（逐组提交，见 §2.4）
git checkout donor/dev -- \
    handlers/fq_hg handlers/xmly handlers/qm_luomu handlers/qq_luomu handlers/sq_luomu handlers/uxx \
    internal/fq_hg internal/xmly \
    data/uxx data/xmly \
    utils/uxx_utils.go \
    test/fq_hg_integration_test.go test/xmly_pool_test.go test/bench
```

取完立刻核对「一支没漏」：

```bash
git ls-tree -r --name-only donor/dev | grep -E '^(handlers/(fq_hg|xmly|qm_luomu|qq_luomu|sq_luomu|uxx)|internal/(fq_hg|xmly))/[a-z]' | wc -l   # dev 侧源文件数
find sources internal* -name '*.go' | wc -l                                                                                                     # 分支侧对应数
```

### 2.3 接缝清单与审计判据

分支侧**唯一允许改动**的路径：`sources/**`、`data/**`、`.env.example`、`AGENTS.md`/`README.md`（追加源章节）、`deploy/` 的环境注入、分支自有的源测试。

`middleware/all/all.go` 在记忆里被列进接缝，但**本次不许动**：源专有逻辑先用 `SourceMeta` 声明表达（AGENTS.md §5 的纪律），确实需要新中间件才是「提回骨架」的另一件事。

审计（每次阶段收尾跑一次，输出必须为空）：

```bash
git diff skeleton-v1...HEAD -- . \
  ':(exclude)sources/**' ':(exclude)data/**' ':(exclude)test/**' \
  ':(exclude).env.example' ':(exclude)AGENTS.md' ':(exclude)README.md'
```

命中即「分支改了骨架」，出路只有两条：撤销，或把该通用需求提回 `main` 补声明位后再 merge。这就是 T2「受保护路径校验」的本地可执行版本（无 CI，靠这条命令 + 人工审查）。

### 2.4 分期提交（每步都 `make build` 全绿才提交）

1. `chore(分支): 从骨架起，登记 donor 只读供体` — 只有 §2.2 的取内容，未改名（此步编译不过，故与第 2 步合并提交，或允许这一步用 `--no-verify`？不允许：AGENTS.md §10 无豁免。**改为**：第 1 笔提交同时完成 `git mv` + import 前缀改写，保证可编译）。
2. `refactor(数据面): 源包落 sources/，改用声明位`（§3、§4）
3. `refactor(号池): xmly 池迁 base/pool + 存量表迁移`（§5）
4. `chore(配置): 源专属 env 归位于源包，xmly 协议常量归位 sources/xmly/client`（§6）
5. `refactor(面板): 换用通用号池页，删源专属页`（§7）
6. `test(数据源): 迁入源用例 + 骨架护栏复核`（§9）
7. `docs: AGENTS.md 源章节与部署说明`（§8 的行为变化写进发布说明）

`make build` 会先跑 `make web`（pnpm，需 `web/pnpm-workspace.yaml` 的 `allowBuilds.esbuild`），再 vet + gofmt + `go test ./test/ -race`（≈210s）+ 双二进制。

## 3. 六源迁入与声明补全

dev 的 `SourceMeta`（`base/registry.go:11-19`）**没有** 骨架的四个声明位（`FixedBaseURL/SearchTabs/LegacyGroups/DataFiles`，`base/registry.go:21-31`），这些知识在 dev 里硬编码在平台文件中。迁移 = 把它们改写成声明。下表是逐源字面量（`Code/Display/Category/Description/SortOrder/Status/Actions` 全部照 dev 原值，出处标注）：

| 源 | dev 出处 | 声明位（新增） |
|---|---|---|
| `fq_hg`（package `hg`） | `handlers/fq_hg/actions.go:283-297`：番茄红果 / `fq` / sort 11 / actions `search,detail,chapter,content,explore` | **`FixedBaseURL: true`**（修 dev 漏判，见下）、`SearchTabs` 五形态 |
| `xmly` | `handlers/xmly/xmly.go:101-105` + `explore.go:88`：喜马拉雅 / `xmly` / sort 10 / actions `search,detail,chapter,content,explore` | `FixedBaseURL: true`、`DataFiles{xmly_groups, xmly_metadataValues}` |
| `uxx` | `handlers/uxx/search.go:62-66`：uxx / `uxx` / sort 9 / actions `search,detail,chapter,content,recommend` | `FixedBaseURL: true`、`DataFiles{sytjs → "推荐首页 tab_type 列表"}` |
| `qq_luomu` | `handlers/qq_luomu/search.go:103-113`：QQ阅读（落幕）/ `qq` / sort 6 / 四动作 | `LegacyGroups: []string{"qq"}`；**不**声明 FixedBaseURL（取 `baseUrl` 参数） |
| `qm_luomu` | `handlers/qm_luomu/search.go:109-113`：七猫（落幕）/ `qm` / sort 7 / 四动作 | `LegacyGroups: []string{"qm"}` |
| `sq_luomu` | `handlers/sq_luomu/…`：书旗（落幕）/ `sq` / sort 8 / 四动作 | `LegacyGroups: []string{"sq"}` |

要点：

1. **`fq_hg` 的 `FixedBaseURL` 是一处缺陷修复**。dev 把判定硬编码成 `app/middleware.go:122`：`skipBaseURLCheck := sourceName == "xmly" || sourceName == "uxx"`——漏了同样写死上游的 `fq_hg`（其上游地址在 `handlers/fq_hg/handlers.go:2`）。骨架侧该判定已是 `middleware/source/baseurl.go` 读 `meta.FixedBaseURL`，声明到位即自动修好；**不要去骨架里补硬编码名单**。
2. **`fq_hg` 不要声明 `LegacyGroups: ["fq"]`**。dev 的 `db/seed.go:311-318` 里 `"fq": {}` 是**空展开**（成员即被下线的五源，声明为空只为清掉存量组行）。若 fq_hg 声明 legacy `fq`，`migrateGroupCostRows` 会把历史属于五源的组行展开成 fq_hg 的行——资费与限额被凭空继承。五源的存量清理由 `RETIRED_SOURCES`（§8）承担。
3. **`SearchTabs` 逐字搬** dev `handlers/common/datasource.go:59-66`（骨架渲染逻辑在 `handlers/catalog/datasource.go:168-174`，声明存在则用声明，否则用通用 tab `{3,"2","小说"}`）：
   ```go
   SearchTabs: []base.SearchTab{
       {TabType: 2, BdID: "-", Name: "听书"},
       {TabType: 3, BdID: "-", Name: "小说"},
       {TabType: 8, BdID: "-", Name: "漫画"},
       {TabType: 10, BdID: "-", Name: "短剧"},
       {TabType: 13, BdID: "-", Name: "漫剧"},
   },
   ```
   注意 `base.SearchTab` 的字段名是 `BdID`（dev 侧是 `BdId`），且骨架的 `/data` 列表按 **category 目录**取文件（`getFiles(ds.Category)`），所以 xmly/uxx 的字典会自动列出，`DataFiles` 只补「说明」列文案——xmly 两个字典在 dev 侧没有文案，迁移时新写（`xmly_groups` → 分类组、`xmly_metadataValues` → 元数据取值表）。
4. **`LegacyGroups` 之外不要再碰 `db/seed.go`**：骨架的 seed 已完全声明驱动（`db/seed.go:141-149`、`:432-456`、`:251` 起 `retiredSources()`），源包声明到位即播种正确。
5. `base.RegisterSource` 返回 `error`（骨架 `base/registry.go:67`）。源包 `init()` 里按 `testkit/fakesource` 的写法处理，不要裸调丢弃错误。

## 4. `internal/` 协议包改名（会踩的硬钉子）

`internal/fq_hg`（3641 行：`sign.go` 签名、`cenc.go`/`content_key.go` 加解密、`session.go` 会话池、`registry.go` 端点覆写、`normalize.go`、`modes.go` + `//go:embed data/modes.json`）与 `internal/xmly`（591 行：`client.go`/`crypto.go` HMAC 签名、`api.go`、`explore_api.go`）**只被各自源包 + `test/` 导入**，可以整体随源走。

但不能沿用 `internal` 这个目录名：Go 的 internal 规则下，`sources/fq_hg/internal/...` 只允许 `sources/fq_hg/...` 子树导入，而 `test/`（package `test`，仓库根级另一棵树）会**直接编译失败**。

改名方案：`internal/fq_hg` → `sources/fq_hg/protocol`，`internal/xmly` → `sources/xmly/api`（`go:embed` 的 `data/modes.json` 相对路径同步）。同时 `utils/uxx_utils.go` → `sources/uxx/`（只有 uxx 用）。

反向的一条：`utils/fq_utils.go` **名字骗人**，里面全是通用件（`ToString/FirstNonEmpty/TZShanghai/NormalizeAPIBase/FormatTime/FormatWordCount/ToInt64/FormatDate`，`:15-98`），骨架已以 `utils/norm.go` 提供 → **不迁移该文件**，源包 import 改指 `loomproxy/utils`。

## 5. xmly 号池 → `base/pool`（最大单块工作）

dev 的 `handlers/xmly/pool.go`（576 行）+ `pool_util.go`（自研 flight 组与错误窗口）是骨架通用池框架的一个私有实现，两者语义高度重合。**逐函数处置**：

| dev（`handlers/xmly/`） | 骨架 | 处置 |
|---|---|---|
| `pool.go:80 initPool`（含 `:90-92` 非 UUID → dead） | `state.go:159 initLedger` | Provider 侧：`Refresh` 对非法号返错 → 框架 `markDead` 自然达成；不新增骨架钩子 |
| `pool.go:123 refreshDevice`（`AdFreeStatus`） | `Provider.Refresh(ctx, dev) (Quota, error)` | 实现：`Total`/`Used`/`ExpiresAt` 三值映射 |
| `pool.go:142 claim`（`AddDuration`） | `Provider.Claim(ctx, dev) error` | 实现，天然满足「叠加无损」契约 |
| `pool.go:190 createColdLocked`（`GenerateDeviceID/Sn`） | `Provider.Create(ctx) (*Device, error)` | 实现：`Ident=device_id`，`Attrs{sn, duration_min}` |
| `pool.go:261 PrioritizeClaimedCold` | `state.go:487 PrioritizeClaimed` | **删除 dev 版**，框架已内建同语义（已领取且仍在有效期优先、越早到期越优先） |
| `pool.go:278/205/222/410 promoteLocked/topUpCold/reviveOneSpent/maintain` | `state.go:312-460` | **删除 dev 版** |
| `pool.go:335 Reauthorize` + `pool_util.go:21-75 flightGroup` | `state.go:250-299` + `base.Flight` | **删除 dev 版**（框架用 `base.Flight` 按号合并） |
| `pool.go:372 ReportPlayResult` 的 501 分类 | `pool.ResourceExpiredClassifier` | 实现可选接口 `IsResourceExpired(err) bool` |
| `pool.go:385 startMaintainer`（每分钟 ticker） | `registry.go:56 StartAll` + `POOL_MAINTAIN_SEC` | 删除；骨架 `app.Run` 已 `go pool.StartAll()`，关停经 `lifecycle` |
| `pool.go:473-540 PoolConfigInfo/PoolDeviceInfo/PoolStatus/maskDeviceIdent` | `status.go:11-135` + `GET /admin/pools` | 删除（含 dev 的 `handlers/admin/xmly_pool.go`、`admin.go:52` 路由） |
| `pool.go:58 GetDevicePool()` 由 `app/app.go:640-645` 显式启动 | 源包 `init()` 里 `pool.Register(pool.New(provider, cfg))` | 改接线：骨架 `app.Run` 统一启动，`conf.Config.PoolEnabled=false` 整体跳过 |
| `XMLY_POOL_ENABLED` 每池开关 | 源包自行决定是否 `Register`（env 判空即不注册） | **不要给骨架 `pool.Config` 加 `Enabled`** —— 不注册就是不启用 |

**额度量纲**（唯一需要留意的语义偏移）：`models.XmlyDevice` 记的是**分钟**（`TotalMinutes/ReceivedMinutes`），骨架 `pool.Quota{Total, Used int}` 是**次数**。处置：把分钟数直接当计数装进 `Total/Used`（池内单位由 Provider 自解释），墙钟剩余靠 `ExpireAt`；面板 `DeviceInfo` 只出次数与到期时间。若运维确实需要「单位」展示，那是骨架声明位缺位（`Provider` 加 `Unit() string` 或 `DeviceInfo` 附单位），**届时提回 `main`**，本次不提。

**数据迁移 `xmly_devices` → `pool_devices`**（一次性，随 `sources/xmly` 走，不进骨架 seed）：

```
pool            = "xmly"
ident           = device_id
attrs           = {"sn": device_sn, "duration_min": "..."}
total_quota     = total_minutes      // 分钟当计数
used_quota      = received_minutes
expire_at       = expire_at
status          = hot/cold/spent/dead 原值携带（dev 列默认 "active"，需归一为 hot/cold）
```
注意 `XmlyDevice.DeviceSn` 在 dev 有独立 `uniqueIndex`（`models/xmly_device.go`），而骨架唯一键是 `(pool, ident)`——**sn 的唯一性失去数据库约束**，Provider 建号时自己保证（或接受重复，sn 由上游签发本就唯一）。不迁移的后果：面板 0 台、池重新铸号 = 白烧上游免费时长，所以迁移脚本要在上线同一窗口执行。旧表迁移后由源包负责删除或改名留档（骨架 AutoMigrate 不含它，留着也不会被清理）。

## 6. 源专属配置与凭证处置（安全项，单列）

骨架 `conf/` 已无源知识（`conf/conf.go` 现只有通用 `POOL_*`、`RETIRED_SOURCES` 等）。迁移采用**源包内 `os.Getenv`**（`sources/xmly/conf.go`、`sources/fq_hg/conf.go`），否决两项：不给 `SourceMeta` 加 env-key 声明位（过度设计，且声明位是给底座消费的、不是给源自己读配置的）；签名凭证不进 `SystemSetting`/面板（DB 不是放上游签名密钥的地方，且 `/admin/settings` 会把它写进用量与备份链路）。

需要归位的键（dev 出处 `conf/conf.go:77-98,255-267`）：`XMLY_APP_KEY`、`XMLY_APP_SECRET`、`XMLY_PLAYURL_CACHE_TTL`、`XMLY_CHAPTER_CACHE_TTL`、`FQHG_SESSIONS_PATH`、`FQHG_ENDPOINTS_OVERRIDE`；池参数改读通用 `POOL_*`（可按池在源包 `Config` 里覆写）。

**凭证的定性（2026-09-30 与用户确认后更正）**：`XMLY_APP_KEY`/`XMLY_APP_SECRET` 不是部署密钥，而是
小雅开放接口的**固定客户端标识**——所有客户端二进制里都是同一对，泄露面等同公开。
因此迁入后的处置是：作为协议常量内置在 `sources/xmly/client/conf.go`（与 `apiBase` 同类），
env 仅保留为「上游轮换该对时」的覆盖入口，`.env.example` 明确写了不必设置；
不做「无凭证即 fail-closed」的门槛（那会让源开箱不可用），也不需要轮换。
dev 侧的问题只是**位置错了**（写在 `conf/` 里让骨架看似感知数据源），不是泄密。

## 7. 控制面与面板换装

- `handlers/admin/xmly_pool.go` + `admin.go` 的 `GET /admin/xmly-pool` → 删除，改用骨架 `handlers/admin/pools.go` 的 `GET /admin/pools`（`pool.StatusAll()`，标识与凭证一律脱敏、只列凭证键名）。
- `web/src/pages/admin/XmlyPool.vue` 及 `main.js:18,44`、`App.vue:148,173`、`api/index.js:78` 的挂点 → 删除，改用骨架的 `web/src/pages/admin/Pools.vue` + `api.listPools`。
- `web/src/pages/Datasources.vue:118` 的默认 `/qq_luomu/search`、`admin/Datasources.vue` 的硬编码 category `<option>`：骨架已去源化（改为自由输入 + 库内数据），**取骨架版本**，不要搬 dev 文案。
- `handlers/admin/admin.go:592-596` 的源展示列表：dev-only，取骨架版本（查 `data_sources` 表）。
- 前端规范照旧（AGENTS.md §10）：标签一律 `UiTag`、禁 emoji/渐变/重阴影。

## 8. 播种、存量库影响与行为变化

- `.env.example` 追加 **`RETIRED_SOURCES=fq_tutu,fq_mufan,fq_xinghai,fq_luomu,fq_jingluo`**（dev 在 `796db3d`/2026-09-29 下线番茄五源，`db/seed.go:247` 硬删存量；骨架侧同一件事由 env 驱动，`db/seed.go:251` `retiredSources()` + `:260` `cleanupRemovedSources`）。公开版分支在此基础上再追加 `xmly,fq_hg`。历史用量流水保留（骨架与原语义一致）。
- **记忆文档 §6 那份 9 源声明清单是旧状态**（抄自 `public@fd167c5`），`data/fq/` 三字典文案也已随五源删除——以本文件 §3 为准。
- **一处真实行为变化（发布说明必写）**：dev 的 `quota_limits` 没有 `fq_hg` 行（= 不限），骨架的声明式播种会给每个源建 free 100/日、vip 1000/日、admin 不限（`db/seed.go:128-149`，`count==0` 才插 → **存量库下次启动也会新增 fq_hg 限额行**）。其余 5 源的 dev 硬编码值与骨架默认值逐行相同（`db/seed.go:124-147` ↔ 骨架 `:128-149`），无变化。已定：**接受统一口径**，不给骨架加跳过声明位。
- 号池台账从 `xmly_devices` 换到 `pool_devices`：`POOL_ENABLED=false` 时 `StartAll` 整体跳过，面板仍能从库读到记录（`status.go:84 fillCountsFromDB`）。

## 9. 测试迁入与骨架护栏

- 主体留在 `test/`（AGENTS.md §10「黑盒测试一律放 test/」）：`test/fq_hg_integration_test.go`（改 `loomproxy-go/internal/fq_hg` → `loomproxy/sources/fq_hg/protocol`、`handlers/fq_hg` → `sources/fq_hg`）、`test/xmly_pool_test.go`（改为针对 `base/pool` + xmly Provider）、`test/bench/`。只有必须触包内未导出类型的用例才随源包（预期为零）。
- **骨架自带用例一支不改**地通过，是「分支没弄坏骨架」的证据：`test/` 全量（含 `test/middleware_chain_test.go` 的顺序断言、`test/pool_test.go` 的假 Provider、`testkit/fakesource` + `cmd/fakegateway` 跨进程链路）。
- `test/python/` 里引用真实源名的用例同步改指假源或源分支自有用例（骨架版默认跑 `cmd/fakegateway`）。
- 集成用例禁止 `t.Parallel()`（`conf.Config`/`db.DB`/`utils.DefaultCache`/限流器/号池注册表都是进程级共享）。

## 10. 验收协议

1. `make build` 全绿（前端 → vet + gofmt → `go test ./test/ -race` → 双二进制），`cd test/python && uv run pytest -q` 全绿。
2. §2.3 审计命令输出为空。
3. 不变量自查：源名只出现在 `sources/**`、`data/**`、`.env.example`、`AGENTS.md`/`README.md`、分支自有测试里；`app/ middleware/ gate/ base/ db/ conf/ handlers/ utils/ web/` 内除上述接缝外零命中（骨架侧那条「全域 0 命中」grep 只适用于 `main`，源分支上必然命中，别当失败信号）。
4. 真源冒烟（`make run`，`POOL_ENABLED=true`）：
   - `GET /datasources` 出 6 个源，`fq_hg` 带五形态 tab（2/3/8/10/13），`qq/qm/sq_luomu` 仍可配 `baseUrl`；
   - `GET /fq_hg/search?tabType=8&query=…` 命中上游（验证 `FixedBaseURL` 声明生效：不带 `baseUrl` 参数不再 400）；
   - `GET /xmly/content…` 经号池取号，`GET /admin/pools` 列出 xmly 池且凭证脱敏（只出键名）；
   - `GET /data/uxx/sytjs.json` 直出、`/data` 列表的说明列取自 `DataFiles` 声明；`data/fq_hg/state.json` **不**出现在任何 `/data` 视图（号池运行时状态与静态字典共用 `DATA_DIR`，按 category 目录取文件恰好隔离，改目录结构后要复核）；
   - 禁用接口仍 403、限流仍 429，且监控计数与自动拉黑照常（`middleware/source` 的 monitor 在 `gate` 三轴之前）。
5. 提交纪律：每个阶段一笔提交，AGENTS.md 同步更新一起提交；全程 `git push` 禁止。

## 11. 执行结果（2026-09-30）

- 提交：`813f371`（5 源迁入 + 声明位）→ `cc5946a`（xmly + 号池框架化 + 源 env 归位 + 迁移工具）→ 收尾一笔（凭证定性、AGENTS.md §0、号池断言通用化）。每笔均 `make build` 全绿。
- 审计：对 `skeleton-v1` 的骨架路径 diff 只剩 `.env.example`、`go.mod`/`go.sum`（源依赖 goquery 是新发现的接缝，已计入 §2.3 清单）。
- 冒烟（真上游，非离线）：`/datasources` 出 6 源且 fq_hg 五形态 tab 来自声明；`/data/xmly` 说明列来自 `DataFiles`；`/fq_hg/search` 不带 `baseUrl` 直接命中上游返回真实书单（`FixedBaseURL` 生效，同时修掉 dev 漏判）；`/qq_luomu/search?baseUrl=http://127.0.0.1:9/x` → 400 unsafe base_url（SSRF 口径不变）；`/uxx/search` 带外部 `baseUrl` 被忽略、仍打自家站点；xmly 取号在 `POOL_ENABLED=false` 下按需建号并返回真实搜索结果，`pool_devices` 台账写入 `hot/4/1`。
- 迁移工具：对含 4 行 `xmly_devices` 的库先 `-check` 后实搬，`active`→`cold` 归一、`Attrs={"device_sn":…}`、`expire_at` 携带，重复执行全部跳过（幂等）。
- 媒介声明的一处回退（同日 `a9f3eb4`）：**uxx 不声明 MediaType 源默认**。它的搜索不能按分类筛选、
  媒介只在详情页看得出，列表项 `kind` 是付费类型；填默认等于让小说结果也被判成影像。
  最终取值 = qq/qm/sq `novel`、xmly `audio`、fq_hg 逐 tab、uxx 不声明（详情/正文自带 + 命名缓存回补）。
  教训写进声明位注释：**「源默认」只适用于一源一媒介**，判据是「搜索阶段能否确定媒介」，
  不是「DetectBookType 的兜底分支返回什么」。
- 追平骨架（同日）：`git merge main`（`6028c15`）→ `93a2b24`，冲突只落在 `test/monitor_integration_test.go`
  的注释措辞（同一处断言两边都改过，取 main 的部署中立表述）；随后 `4fe359e` 只改 `sources/**` 补媒介声明
  （qq/qm/sq=novel、xmly=audio、uxx=video；fq_hg 不填源默认、逐 tab 声明）。
- **骨架基线换 tag**：`skeleton-v2` = `6028c15`。审计判据、AGENTS.md §0 均已指向新基线；每 merge 一次 main
  就补一个新 tag（停在 skeleton-v1 会把 main 的新改动算成分支违规）。
- 公开版不建长期分支（已定）：发布时从 `sources/private` 拉临时分支，删 `sources/all.go` 两行 + `RETIRED_SOURCES` 追加。
- 骨架侧两处修复已在 `main` 落地（`6028c15`）：上游 URL 透穿（handleError + 回归用例）、号池断言的部署中立性。
- 开源与定位（同日 `2b5f68d`，已推 origin/main）：骨架按 **AGPL-3.0** 发布 + `DISCLAIMER.md`；项目定位纠正为
  「上游接口的代理与治理平台」，Legado 只是一种下游输出契约。分支侧 `afb5a7c` 合并该笔，README 顶部加了分支
  身份说明（携带 6 源、私有适配、不在公开许可覆盖范围内、不推送远端），基线随之换到 `skeleton-v3`。
- 顺带记录一处骨架既有行为（不属本次范围）：上游网络错误的 `msg` 会把完整上游 URL 透给下游（`/uxx/search` 实测），与 AGENTS.md §10 的脱敏要求有张力，值得单独处理。

## 12. 风险与待决

| 项 | 说明 | 处置 |
|---|---|---|
| xmly `app_key`/签名密钥的定性 | 用户确认：这是上游协议的固定客户端标识，不是部署密钥 | 作为协议常量内置 `sources/xmly/client`，env 仅覆盖；不设 fail-closed、不需轮换（§6） |
| `xmly_devices` 未迁移即上线 | 池重新铸号，白烧上游免费时长 | 迁移脚本与上线同窗口（§5） |
| fq_hg 新增默认限额 | 存量库下次启动生效，用户可见 | 发布说明 + 已定接受统一口径（§8） |
| `internal`→`protocol` 改名的漏改引用 | 编译期即暴露 | `go build ./...` 兜底，无静默风险 |
| 公开/私有分支形态 | 同仓双分支（`sources/private` / `sources/public`）把原「两个 fork + `git subtree` 单向搬运」简化为同仓 `git merge` | **用户已确认采用**；`sources/public` 尚未创建（= private 少 fq_hg/xmly 两行 + `RETIRED_SOURCES` 追加两源） |
| 骨架侧一处待提回改动 | `test/monitor_integration_test.go` 的号池断言原为「号池数==1」，携带数据源后必然为多 | 已在本分支改为按池名取条目（通用改进）；应提回 `main`，避免下次 merge 冲突 |
| 对下游错误文案含上游 URL | uxx 上游失败时 `msg` 带完整站点 URL（骨架 `handleError` 既有口径，与 §10 脱敏要求有张力） | 不属本次迁移范围，记为骨架侧议题 |
| 骨架后续演进 | `main` 上还会改中间件/gate | 源分支定期 `git merge main`，冲突只应落在接缝清单；落在别处先提回 `main` |
