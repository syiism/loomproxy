# loomproxy-go 构建纪律：make build 一键完成 前端构建 → 静态检查 → 测试 → 后端编译
#
# 常用目标：
#   make build    完整流水线（web + vet + test + go-build），产出调试版 ./loomproxy-go
#                 与正式版 ./loomproxy-go-release（-ldflags="-s -w" strip，部署用）
#   make web      仅构建前端（web/dist，go:embed 编译期必需）
#   make test     运行全部测试（-race）
#   make vet      go vet + gofmt 检查 + 手写 SQL 的保留字别名/反引号扫描 + 前端未定义类扫描
#   make run      build 后启动服务
#   make clean    清理二进制与 web/dist

BINARY := loomproxy-go
RELEASE_BINARY := loomproxy-go-release

# 存量未 gofmt 文件（历史遗留，勿动；见 AGENTS.md），gofmt 检查时豁免
GOFMT_EXEMPT_RE := handlers/admin/usage_logs\.go|handlers/common/datasource\.go|handlers/quota/middleware\.go|models/api_call_log\.go

.PHONY: build web vet fmt-check sql-check css-check test go-build run docker clean

build: web vet test go-build

web:
	cd web && pnpm install && pnpm build

# 调试版保留调试符号（本地排障）；正式版 strip（部署用，与 deploy.sh/Dockerfile 一致）
go-build:
	CGO_ENABLED=1 go build -o $(BINARY) .
	CGO_ENABLED=1 go build -ldflags="-s -w" -o $(RELEASE_BINARY) .

test:
	go test ./test/ -race -count=1

vet:
	go vet ./...
	@$(MAKE) --no-print-directory fmt-check
	@$(MAKE) --no-print-directory sql-check
	@$(MAKE) --no-print-directory css-check

# gofmt 检查：列出即失败（豁免上述存量文件）
fmt-check:
	@bad=$$(gofmt -l . | grep -v -E '^($(GOFMT_EXEMPT_RE))$$' || true); \
	if [ -n "$$bad" ]; then echo "gofmt 未通过:"; echo "$$bad"; exit 1; fi

# 前端自定义类：用了没定义的类 Tailwind 静默跳过，构建与用例都不会红（待办清单 P31）
css-check:
	@./scripts/check-css-classes.sh

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
