# loomproxy-go 构建纪律：make build 一键完成 前端构建 → 静态检查 → 测试 → 后端编译
#
# 常用目标：
#   make build    完整流水线（web + vet + test + go-build），产出调试版 ./loomproxy-go
#                 与正式版 ./loomproxy-go-release（-ldflags="-s -w" strip，部署用）
#   make web      仅构建前端（web/dist，go:embed 编译期必需）
#   make test     运行全部测试（-race）
#   make vet      go vet + gofmt 检查 + 手写 SQL 的保留字别名/反引号扫描 + 前端未定义类扫描
#                 + 管理后台导航完整性扫描
#   make run      build 后启动服务
#   make clean    清理二进制与 web/dist

BINARY := loomproxy-go
RELEASE_BINARY := loomproxy-go-release

# 存量未 gofmt 文件（历史遗留，勿动；见 AGENTS.md），gofmt 检查时豁免
GOFMT_EXEMPT_RE := handlers/admin/usage_logs\.go|handlers/common/datasource\.go|handlers/quota/middleware\.go|models/api_call_log\.go

.PHONY: build web vet fmt-check sql-check css-check nav-check test go-build run docker clean lock-defer-check

build: web vet test go-build

web:
	cd web && pnpm install && pnpm build

# 调试版保留调试符号（本地排障）；正式版 strip（部署用，与 deploy.sh/Dockerfile 一致）
go-build:
	CGO_ENABLED=1 go build -o $(BINARY) .
	CGO_ENABLED=1 go build -ldflags="-s -w" -o $(RELEASE_BINARY) .

# -timeout 必须显式给：Go 的默认值是 10m，而 race 套件在不同机器上耗时差好几倍
# （本仓开发机约 4 分钟，另有构建机实测约 11 分钟），超时会把一次正常的门禁报成失败——
# 症状是全绿的用例跑到一半突然 panic: test timed out，看起来像用例挂了而不是时间不够。
#
# 测试段跑 `./...` 而不是只跑 `./test/`（分支侧 S30 登记的门禁盲区，本轮回骨架收口）：
# **不被门禁跑的断言等于没有断言**。携带形态的源包里本来就有向量对拍与解析层用例
# （`sources/*/protocol/*_test.go`、`sources/*/form/*_test.go`），过去只有人手动跑那一条命令才会红。
# 真实事故：变异验证把两个签名常量互换后没还原就提交，`make build` 全绿。
# 骨架不携带源，这条改动对骨架是零成本超集（没有包内测试的包照样只报 no test files）。
test:
	go test ./... -race -count=1 -timeout 20m

# 携带分支合并前预检：骨架门禁对分支树跑一遍，只报分支自有文件的命中（S50 方法固化）
precheck-branch:
	@./scripts/precheck-branch.sh $(TREE)

vet:
	go vet ./...
	@$(MAKE) --no-print-directory fmt-check
	@$(MAKE) --no-print-directory sql-check
	@$(MAKE) --no-print-directory css-check
	@$(MAKE) --no-print-directory nav-check
	@$(MAKE) --no-print-directory test-order-check
	@$(MAKE) --no-print-directory settings-readers-check
	@$(MAKE) --no-print-directory judgment-count-check
	@$(MAKE) --no-print-directory doc-numbers-check
	@$(MAKE) --no-print-directory tz-check
	@$(MAKE) --no-print-directory param-assert-check
	@$(MAKE) --no-print-directory deploy-atomic-check
	@$(MAKE) --no-print-directory pagination-single-source-check
	@$(MAKE) --no-print-directory ledger-write-check
	@$(MAKE) --no-print-directory ticker-stop-check
	@$(MAKE) --no-print-directory lock-defer-check

# ticker 必须 Stop：这条形状被处理过两次（P80 给号池巡检协程补 defer Stop；第三十遍发现
# app.prewarmCache 是全仓 8 个 ticker 里唯一漏的那个），第三次不再靠人想起来
ticker-stop-check:
	@./scripts/check-ticker-stopped.sh

# gofmt 检查：列出即失败（豁免上述存量文件）
fmt-check:
	@bad=$$(gofmt -l . | grep -v -E '^($(GOFMT_EXEMPT_RE))$$' || true); \
	if [ -n "$$bad" ]; then echo "gofmt 未通过:"; echo "$$bad"; exit 1; fi

# 前端自定义类：用了没定义的类 Tailwind 静默跳过，构建与用例都不会红（待办清单 P31）
css-check:
	@./scripts/check-css-classes.sh

# 集成用例必须先装配测试服务器再碰跨用例共享的全局态（conf.Config / db.DB / utils.DefaultCache）。
# 真实症状是一条用例在 newTestServer 之前读 conf.Config：单跑它 nil panic，整包却绿——
# 它靠的是上一个用例把全局指针建好了，而用例顺序没有任何保证（待办清单 P39 顺带暴露）。
test-order-check:
	@./scripts/check-test-order.sh

# 设置项的两条静默面：① 没有后端读取方（面板上能编辑、值改得动，而后端没人读它）——
# 构建绿、用例绿、面板也绿，只有管理员以为它生效，这是最静默的一种装饰位（待办清单 P53）；
# ② 整数设置的读取口只有一处 `db.SettingInt`，出现本地实现即红（待办清单 P61，同形状被修过三次）。
# 确实只给展示或脚本用的键，写进脚本里的 EXEMPT 并留一句理由。
settings-readers-check:
	@./scripts/check-setting-readers.sh

# 判据页顶上那句「N 条：a 已 / b 部分 / c 未」必须等于当场数出来的，且全页只许有一句。
# 触发它的经验写在那一页自己的《先删后加》段里：同一段曾排过七句各自自称实测的分布，
# 其中一对的差是一行模板被算成了条目——「数字要数出来」反复靠自觉失败，就该由机器数。
judgment-count-check:
	@./scripts/check-judgment-count.sh

# 执行报告里那两个自己声称的数（§8「共 N 条」、那句「清单页 …N 行」）必须等于当场数出来的。
# 触发它的经验是本轮两次凭印象写行数（813/860/890/913 才对上一次）与一个改过两次仍错的条目数——
# 与 judgment-count-check 同一条判据，只是对象从判据页换成报告。
doc-numbers-check:
	@./scripts/check-doc-numbers.sh

# 平台时区与日界只有一处定义（待办清单 P71）：出现 FixedZone / time.Local / 旧的 TZShanghai 调用即红。
# 触发它的经验是本轮第十四遍数出来的七种写法——现网三种口径恰好重合，所以它不会自己报错。
tz-check:
	@./scripts/check-tz-single-source.sh

# 请求参数的值一律是 string：对它做数字断言 = 静默死代码（分支的 toPage 真中过：page 永远回到 1）；
# 同时禁止声明以 `_` 开头的参数名——那是平台注入键的保留前缀（待办清单 P75）。
# 注意这条**扫 sources/\*\***（两棵树的代码都要干净），与 tz-check 的豁免方向不同。
param-assert-check:
	@./scripts/check-param-number-assert.sh

# 部署脚本不许逐字节写「正在被执行的那条路径」：半截二进制会被 Restart=always 立刻 exec
# （现网 14 天三次同款崩溃循环，其中两次没有任何应用日志）——见待办清单 P79
deploy-atomic-check:
	@./scripts/check-deploy-atomic-install.sh

# 分页夹只许一处定义：以前六个端点各抄一遍 `<1 || >100`，值今天一致但第七处可以静默不写；
# 而旧写法丢掉 Atoi 的错误，越界页码会得到 MaxInt64 并让 offset 溢出成负数（待办清单 P86）
pagination-single-source-check:
	@./scripts/check-pagination-single-source.sh

# 判定表与审计表的写入必须读 .Error（待办清单 P88）：这类写入失败**不改对外响应**——
# 请求照常 200、监控记成功，唯一的症状是"额度扣不完 / 用户以为配置改好了 / 过期行只涨不清"。
# §10 早就写了"审计类写入不许吞错"，但那是句话，于是本轮扫出 `quota_usage_logs` 两处、
# `user_source_configs` 两处、`user_roles` 去重一处、过期验证码清理一处都是裸写。
# 固化成扫描器的理由与 P61 同一条：同一个形状被修过第二次，就别再靠人记得住。
ledger-write-check:
	@./scripts/check-ledger-write-checked.sh

# 临界区一律 defer 解锁：P77/P78 把这句写成判据、P93 实测到「panic 跳过尾部 Unlock = 锁被永久持有，
# 症状从"少一行读数"变成"下一个请求卡死"」，当场数出全仓还有 19 个函数是尾解锁。
# 约定被踩过第三次就不再靠人想起来（同 ticker-stop-check / ledger-write-check 的来历）。
# 多段临界区 / 循环体里就地写 defer 会**扩大持锁范围甚至自死锁**，所以脚本要求的是"每段闭包包起来"——
# 理由与写法都写在脚本头部。
lock-defer-check:
	@./scripts/check-lock-defer-unlock.sh

# 管理后台导航完整性：main.js 里每个 /admin/* 路由都要在 Admin.vue 的 shortcuts 里有入口。
# 号池那一页就是这样漏掉的（导航页不是索引的话，管理员只能靠背 URL）。
nav-check:
	@routes=$$(grep -o "path: '/admin[^']*'" web/src/main.js | sed "s#.*'/##; s#'##" | sort -u); \
	navs=$$(grep -o "to: '/admin[^']*'" web/src/pages/Admin.vue | sed "s#.*'/##; s#'##" | sort -u); \
	missing=$$(comm -23 <(echo "$$routes") <(echo "$$navs") | grep -v '^admin$$' || true); \
	if [ -n "$$missing" ]; then echo "管理后台导航缺页（Admin.vue 的 shortcuts 里没有入口）:"; echo "$$missing" | sed 's/^/  /'; exit 1; fi

# 手写 SQL 的保留字别名与反引号：SQLite 容忍、只有 MySQL 报，所以必须在构建时扫
sql-check:
	@./scripts/check-sql-reserved.sh

run: build
	./$(BINARY)

# 容器化构建（多阶段 Dockerfile，含前端与 CGO 编译）
docker:
	docker build -t loomproxy-go .

clean:
	rm -f $(BINARY) $(RELEASE_BINARY)
	rm -rf web/dist
