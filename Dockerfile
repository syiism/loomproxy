# syntax=docker/dockerfile:1
# loomproxy-go 一体化镜像：前端构建 → CGO 编译 → 精简运行时
# 构建：docker build -t loomproxy-go .
# 运行：docker run -d --name loomproxy --env-file .env -p 8081:8081 -v loomproxy-data:/app/data loomproxy-go

# ===== 阶段 1：前端构建（产物供 go:embed 打包） =====
FROM node:24-alpine AS web
WORKDIR /build/web
# 固定 pnpm 版本与开发机一致：pnpm 10.28+ 对未批准的构建脚本（esbuild）
# 会报 ERR_PNPM_IGNORED_BUILDS 导致安装失败，不固定版本易随 latest 漂移
RUN corepack enable && corepack prepare pnpm@10.28.1 --activate
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

# ===== 阶段 2：后端编译（CGO 必需：gorm sqlite 驱动底层为 mattn/go-sqlite3） =====
FROM golang:1.26-bookworm AS go
# 模块代理可用 --build-arg 覆盖（默认 goproxy.cn，proxy.golang.org 在部分网络不可达）
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# 以阶段 1 的新鲜产物为准（宿主机的 web/dist 可能过期）
COPY --from=web /build/web/dist ./web/dist
RUN CGO_ENABLED=1 go build -ldflags="-s -w" -o loomproxy-go .

# ===== 阶段 3：运行时（与构建阶段同 bookworm glibc，杜绝版本错配） =====
FROM debian:bookworm-slim
# ca-certificates：抓取 https 上游必需；curl：健康检查
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=go /build/loomproxy-go /app/loomproxy-go
COPY favicon.svg /app/favicon.svg
RUN mkdir -p /app/data

# 配置经环境变量注入（docker run --env-file 或 compose env_file），
# 也可把 .env 放到数据卷挂载的 /app 下（cwd 加载）。
# 镜像默认 SQLite + 关 Redis（单容器零依赖；外部 MySQL/Redis 经 env 覆盖）
ENV SERVER_HOST=0.0.0.0 \
    SERVER_PORT=8081 \
    DATA_DIR=/app/data \
    DB_TYPE=sqlite \
    REDIS_ENABLED=false
EXPOSE 8081
VOLUME ["/app/data"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD curl -fsS http://127.0.0.1:8081/ || exit 1

CMD ["/app/loomproxy-go"]
