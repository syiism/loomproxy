#!/usr/bin/env bash
# 每个长跑循环体必须自带 panic 兜底帧（待办清单 P103③，维护者 2026-10-07 拍的严格版 A）。
#
# 这一族在本仓撞过两次（P77：号池维护 tick 里 panic 把锁留在手里；P103：四条裸跑的后台循环），
# 而"新起的后台循环忘了包 Guard"这件事过去只能靠人读循环体——判据页那条因此停在 `部分`。
# 现在由这道门禁拦：形状确定（顶层 `go` 起的循环 + 循环里没有帧），一次性协程不要求帧。
#
# 为什么不是"调到外部实现才要求帧"：那样要么把 `deductMu.Lock` 这类本地方法误判成外部调用，
# 要么漏掉 `p.provider.Refresh(...)` 这一形（接口值上的调用，纯语法看不见）——
# 存量清点的两条读数在条目里。本版选的判据是"函数里连一帧都没有就红"，
# 代价是个别本来不需要帧的循环也得带帧或写一行 `guard-exempt: <理由>`（**豁免会被数出来**）。
#
# 用法：make vet 会自动跑；单独跑 ./scripts/check-goroutine-guard.sh
#      预检携带分支时 PRECHECK_SCAN_ROOT=<分支树> 指过去（与其余 check-* 同一条口径）
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TREE="${PRECHECK_SCAN_ROOT:-$ROOT}"
TREE=$(cd "$TREE" && pwd) || exit 2

# 扫哪棵树就用那棵树里的扫描器：`./tools/...` 是相对当前模块解析的，
# 于是预检携带分支时跑的是**分支合并过来的那一份**规则。
# 这点要紧是有理由的——若从骨架目录绝对引用，分支上的门禁永远比骨架旧一版，
# 而"门禁新旧不一致"正是判据页《同一个判据在几个包里各写一份》那一族在构建侧的形状。
( cd "$TREE" && go run ./tools/goroutineguard . ) || {
  echo "（修法见 docs/规范/待办清单.md 的 P103 与 base/supervise.go 的头注释）"
  exit 1
}
