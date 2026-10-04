#!/usr/bin/env bash
# loomproxy-go 一键升级：构建 → 备份 → 切换 → 冒烟 → 失败自动回滚
#
# 用法：
#   sudo scripts/deploy.sh                 systemd 模式（默认）：构建后安装到 DEPLOY_DIR 并 systemctl restart
#   scripts/deploy.sh --local              原地模式：在仓库目录就地备份切换，pkill + nohup 重启（免 systemd）
#   scripts/deploy.sh --skip-tests         跳过 vet+test（紧急发版，慎用）
#   scripts/deploy.sh -h                   帮助
#
# 环境变量：
#   DEPLOY_DIR    systemd 模式部署目录（默认 /opt/loomproxy），内含 .env 与 data/
#   SERVICE_NAME  systemd 单元名（默认 loomproxy，对应 deploy/loomproxy.service）
#   PORT          冒烟检查端口（默认读 DEPLOY_DIR/.env 的 SERVER_PORT，再默认 8081）
#   KEEP_BACKUPS  保留备份份数（默认 5）
set -euo pipefail

MODE=systemd
SKIP_TESTS=0
for arg in "$@"; do
	case "$arg" in
	--local) MODE=local ;;
	--skip-tests) SKIP_TESTS=1 ;;
	-h | --help)
		sed -n '2,17p' "$0"
		exit 0
		;;
	*) echo "未知参数: $arg（-h 查看帮助）" >&2; exit 2 ;;
	esac
done

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN=loomproxy-go
SERVICE_NAME="${SERVICE_NAME:-loomproxy}"
KEEP_BACKUPS="${KEEP_BACKUPS:-5}"
if [ "$MODE" = systemd ]; then
	DEPLOY_DIR="${DEPLOY_DIR:-/opt/loomproxy}"
else
	DEPLOY_DIR="$REPO_ROOT" # 原地模式：仓库目录即部署目录
fi

log() { printf '\033[1;34m[deploy]\033[0m %s\n' "$*"; }
die() {
	printf '\033[1;31m[deploy] 失败：%s\033[0m\n' "$*" >&2
	exit 1
}

SUDO=""
if [ "$MODE" = systemd ] && [ "$(id -u)" -ne 0 ]; then
	command -v sudo >/dev/null && SUDO="sudo" || die "systemd 模式需要 root 或 sudo"
fi

# place <源文件> <目标路径>：落地一个「可能被正在运行的服务执行」的文件，
# **只用同一目录内的 rename，绝不逐字节写活路径**。
# install/cp 直接写目标时，写完之前的任何一刻 systemd 都可能把半截文件拿来 exec——
# `Restart=always` 会把随之而来的崩溃循环伪装成「运行中」。现网三次都是这个形状：
# 09-28 23:42 连炸 118 次（203/EXEC）、10-01 10:58 与 10-04 01:10 各一轮 SIGSEGV 且**没有任何应用日志**。
# 同目录 rename 是原子的：活路径要么旧版本、要么新版本，不存在半截那一格。
# 落地后复算 md5——这是 AGENTS §3 换装三查第 2 条要求的事，脚本自己做到，不靠人记住。
place() {
	local src=$1 dst=$2 tmp md5_src md5_dst
	[ -f "$src" ] || die "待安装的文件不存在：$src"
	md5_src=$(md5sum "$src" | cut -d' ' -f1)
	tmp="$dst.part.$$"
	$SUDO install -m 0755 "$src" "$tmp" || die "临时文件落地失败：$tmp"
	$SUDO mv -f "$tmp" "$dst" || die "rename 到活路径失败：$dst"
	md5_dst=$($SUDO md5sum "$dst" | cut -d' ' -f1)
	[ "$md5_src" = "$md5_dst" ] || die "安装后 md5 不一致（源 $md5_src / 目标 $md5_dst）——不重启"
	log "已落地 $(basename "$dst")（md5 $md5_dst）"
}

# 冒烟端口：环境变量 > 部署目录 .env > 默认
detect_port() {
	if [ -n "${PORT:-}" ]; then echo "$PORT"; return; fi
	if [ -f "$DEPLOY_DIR/.env" ]; then
		local p
		p=$(grep -E '^SERVER_PORT=' "$DEPLOY_DIR/.env" | tail -1 | cut -d= -f2 | tr -d '[:space:]')
		[ -n "$p" ] && { echo "$p"; return; }
	fi
	echo 8081
}
PORT="$(detect_port)"

# ===== 1. 构建 =====
log "构建前端（web/dist 供 go:embed 打包）..."
(cd "$REPO_ROOT/web" && pnpm install --frozen-lockfile && pnpm build)

if [ "$SKIP_TESTS" -eq 0 ]; then
	log "静态检查与测试（--skip-tests 可跳过）..."
	(cd "$REPO_ROOT" && go vet ./... && go test ./test/ -count=1)
fi

log "编译二进制（CGO）..."
(cd "$REPO_ROOT" && CGO_ENABLED=1 go build -ldflags="-s -w" -o "$BIN.new" .)

# ===== 2. 备份（保留最近 KEEP_BACKUPS 份） =====
LATEST_BACKUP=""
if [ -f "$DEPLOY_DIR/$BIN" ]; then
	LATEST_BACKUP="$DEPLOY_DIR/$BIN.bak.$(date +%Y%m%d-%H%M%S)"
	log "备份旧版本 → $(basename "$LATEST_BACKUP")"
	$SUDO cp "$DEPLOY_DIR/$BIN" "$LATEST_BACKUP"
	# 轮换清理旧备份
	ls -1t "$DEPLOY_DIR/$BIN".bak.* 2>/dev/null | tail -n "+$((KEEP_BACKUPS + 1))" | while read -r old; do
		log "清理旧备份 $(basename "$old")"
		$SUDO rm -f "$old"
	done
fi

# ===== 3. 安装 =====
log "安装新版本到 $DEPLOY_DIR"
if [ "$MODE" = systemd ]; then
	place "$REPO_ROOT/$BIN.new" "$DEPLOY_DIR/$BIN"
	[ -f "$REPO_ROOT/favicon.svg" ] && $SUDO cp -f "$REPO_ROOT/favicon.svg" "$DEPLOY_DIR/favicon.svg" || true
	[ -d "$DEPLOY_DIR/data" ] || $SUDO mkdir -p "$DEPLOY_DIR/data"
	rm -f "$REPO_ROOT/$BIN.new"
else
	place "$REPO_ROOT/$BIN.new" "$DEPLOY_DIR/$BIN"
fi

# ===== 4. 重启 =====
restart_service() {
	if [ "$MODE" = systemd ]; then
		$SUDO systemctl restart "$SERVICE_NAME"
	else
		# pkill -x 精确匹配进程名（注意：勿用 $! 记录子 shell pid 的写法，见生产运维排错记录）
		pkill -x "$BIN" 2>/dev/null || true
		sleep 1
		# setsid -f 新会话独立启动：守护进程脱离脚本进程树。普通 `(nohup ... &)`
		# 会让子 shell 滞留 do_wait 等待服务进程且持有 stdout，调用方（CI/管道/
		# 远程 ssh）永不返回；setsid -f 使服务成为孤儿由 init 收养，脚本即时退出
		(cd "$DEPLOY_DIR" && setsid -f "./$BIN" >>loomproxy.log 2>&1 </dev/null)
	fi
}
log "重启服务（$MODE 模式，端口 $PORT）..."
restart_service

# ===== 5. 冒烟 =====
smoke() {
	local i
	for i in $(seq 1 15); do
		curl -fsS "http://127.0.0.1:$PORT/" -o /dev/null 2>&1 && return 0
		sleep 1
	done
	return 1
}

if smoke; then
	log "✅ 升级完成，服务正常：http://127.0.0.1:$PORT/"
	exit 0
fi

# ===== 6. 失败回滚 =====
log "⚠️ 冒烟未通过，尝试回滚..."
if [ -n "$LATEST_BACKUP" ] && [ -f "$LATEST_BACKUP" ]; then
	place "$LATEST_BACKUP" "$DEPLOY_DIR/$BIN"
	restart_service
	if smoke; then
		die "新版本冒烟失败，已回滚到 $(basename "$LATEST_BACKUP") 并恢复服务"
	fi
	die "新版本冒烟失败，回滚后服务仍异常，请人工检查（systemctl status $SERVICE_NAME / loomproxy.log）"
fi
die "新版本冒烟失败且无旧版本可回滚，服务处于异常状态，请人工检查"
