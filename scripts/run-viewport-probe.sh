#!/usr/bin/env bash
# 起一份**临时实例**（临时 SQLite + 随机端口 + 一次性口令），再按视口量一遍面板。
#
# 为什么要这一层而不是一条 node 命令：探针要的是一个"真的在跑的服务"，
# 而每次手搓那五行 env + 起进程 + 等它报端口 + 收尾，就会有一次忘记收尾——
# 这条脚本存在的理由与 deploy.sh 的 place() 同一条：**把纪律变成机器**。
#
# 用法：
#   make viewport-check            # 或 ./scripts/run-viewport-probe.sh
#   WIDTHS=390 SEED_USERS=0 ./scripts/run-viewport-probe.sh
#   PROBE=scripts/header-geometry-probe.mjs WIDTHS=1280 ./scripts/run-viewport-probe.sh   # 换一支探针，实例只起一次
#
# 前提（脚本会自己说清缺哪一条，不写"本环境没有浏览器"这种无证据的断言）：
#   - 已 make build（需要 ./loomproxy-go，面板是 go:embed 进去的）
#   - playwright-core 装在 /tmp（`npm --prefix /tmp i playwright-core`，不进仓库依赖）
#   - chromium 在 ~/.cache/ms-playwright/ 下，或用 CHROME=<路径> 指定
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN="$REPO_ROOT/loomproxy-go"
WIDTHS="${WIDTHS:-390,1280}"
SEED_USERS="${SEED_USERS:-6}"
PW_CORE="${PW_CORE:-/tmp/node_modules/playwright-core}"
# 同一份临时实例可以喂给别的"按视口量一次"的探针（比如表头几何那支），所以探针名可换、实例只起一次。
# 复制一份起实例的脚本 = 长出第二个事实来源，收尾那半（ kill 要打到 exec 过的那颗 pid）就会漏。
PROBE="${PROBE:-scripts/viewport-probe.mjs}"

[ -x "$BIN" ] || { echo "找不到 $BIN——先 make build（面板产物是 go:embed 进二进制的）"; exit 2; }
[ -d "$PW_CORE" ] || { echo "缺 $PW_CORE——跑一次：npm --prefix /tmp i playwright-core"; exit 2; }

# **先解析再起实例**：这条探针刻意不进 make build（它要浏览器与一份临时实例，挂进门禁会把门禁变成等待），
# 代价是它坏了没有任何东西会报——2026-10-07 那颗提交把一段带反引号的注释写进了模板字符串里，
# 整个脚本从那天起语法坏掉，直到 2026-10-10 下一次真要用它才发现（待办清单 P120）。
# 判据：**不进门禁的检查，至少要在自己开工前过一次语法门**——`node --check` 两行成本，换掉"静默坏掉三周"。
if ! node --check "$REPO_ROOT/$PROBE" >/dev/null 2>&1; then
  echo "探针脚本自身解析不过——先修 $PROBE，别起实例（起了也只会跑到一半报语法错）" >&2
  node --check "$REPO_ROOT/$PROBE" 2>&1 | head -5 >&2
  exit 2
fi

TMP="$(mktemp -d /tmp/loom-viewport.XXXXXX)"
ADMIN_PASS="vp$(od -An -N6 -tx1 /dev/urandom | tr -d ' \n')"   # 一次性口令，不进仓库、不打印
cat > "$TMP/.env" <<EOF
DB_TYPE=sqlite
SERVER_HOST=127.0.0.1
SERVER_PORT=0
AUTH_ENABLED=true
JWT_SECRET=viewport-probe-$RANDOM$RANDOM
ADMIN_USERNAME=admin
ADMIN_PASSWORD=$ADMIN_PASS
DATA_DIR=$TMP/data
TZ_OFFSET_HOURS=8
REDIS_ENABLED=false
SERVER_LOG_LEVEL=release
EOF
mkdir -p "$TMP/data"

cleanup() {
  # 第一版这里只有一个 `kill "$APP_PID"`，结果**跑一次泄漏一个服务器进程**（7 次运行 = 7 个活的
  # loomproxy-go，而临时目录已经被 rm 掉了——cwd 显示 (deleted)，所以从目录里看不出来）。
  # 根因是 `( cd X && bin )` 这个子 shell 不保证 exec：kill 打在子 shell 上，
  # 真正的二进制是它的子进程，成了孤儿。修法是显式 `exec`，再加"确认死了才收尾"。
  if [ -n "${APP_PID:-}" ] && kill -0 "$APP_PID" 2>/dev/null; then
    kill "$APP_PID" 2>/dev/null
    for _ in $(seq 1 20); do
      kill -0 "$APP_PID" 2>/dev/null || break
      sleep 0.2
    done
    kill -9 "$APP_PID" 2>/dev/null
  fi
  # 兜底：按进程名再收一次，确保这条脚本不留下活口
  for p in $(pgrep -f "^$BIN$" 2>/dev/null); do kill -9 "$p" 2>/dev/null; done
  rm -rf "$TMP"
}
trap cleanup EXIT INT TERM

( cd "$TMP" && exec "$BIN" > "$TMP/app.log" 2>&1 ) &
APP_PID=$!

BASE=""
for _ in $(seq 1 60); do
  BASE=$(grep -oE 'LoomProxy starting on 127\.0\.0\.1:[0-9]+' "$TMP/app.log" 2>/dev/null | tail -1 | awk -F: '{print "http://127.0.0.1:" $2}')
  [ -n "$BASE" ] && break
  kill -0 "$APP_PID" 2>/dev/null || { echo "实例没起来，日志尾巴："; tail -5 "$TMP/app.log"; exit 2; }
  sleep 0.5
done
[ -n "$BASE" ] || { echo "等了 30 秒没听到端口——看 $TMP/app.log"; exit 2; }
echo "临时实例：$BASE（临时库在 $TMP，用完即删）"

# 表格里没行的话，"不溢出"是不强的证据——窄屏这一族恰恰是被长内容顶开的。所以先铺几个用户。
if [ "$SEED_USERS" != "0" ]; then
  for i in $(seq 1 "$SEED_USERS"); do
    curl -s -o /dev/null -X POST "$BASE/auth/register" -H 'Content-Type: application/json' \
      -d "{\"username\":\"probe_user_$i\",\"email\":\"probe_user_$i@example.invalid\",\"password\":\"ProbePass2026\"}"
  done
  echo "已铺 $SEED_USERS 个用户（只进临时库）"
fi

cd "$REPO_ROOT"
BASE="$BASE" ADMIN_USER=admin ADMIN_PASS="$ADMIN_PASS" WIDTHS="$WIDTHS" ROUTES="${ROUTES:-}" \
  STICKY="${STICKY:-}" \
  PW_CORE="$PW_CORE" OUT="${OUT:-/tmp/viewport-probe}" node "$REPO_ROOT/$PROBE"
RC=$?
echo "$PROBE 退出码 $RC（0=两档视口下每条路由都不溢出、本页请求无 ≥400；1=有未达标项，上面逐行点名；2=起不来）"
exit $RC
