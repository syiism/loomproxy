#!/usr/bin/env bash
# 用法：./scripts/precheck-branch.sh <携带分支工作区路径>
#
# 把骨架的全部门禁对着分支树跑一遍，并按命中文件分类：
#   - 命中的文件在骨架里也存在  → 「陈旧骨架副本」，合并即被替换，不算红；
#   - 命中的文件骨架里没有      → 分支自有文件真红，合并后 make build 必挂，先修再合。
#
# 由来（S50 的合并前预检表固化，2026-10-05）：同一件事手工做了四次，按升级阶梯进脚本。
# 预检是时点快照——骨架每加一道门禁就要重跑，这正是用脚本而不是清单的原因。
set -u
TREE="${1:?用法: precheck-branch.sh <分支工作区路径>}"
TREE=$(cd "$TREE" && pwd) || exit 2
ROOT=$(cd "$(dirname "$0")/.." && pwd)

REAL=0; STALE=0
declare -a REAL_LINES=()

# 分类一行命中：抽出里面的文件路径，骨架里存在即陈旧
classify() {
  local gate="$1" line="$2" f
  for f in $(grep -oE '[A-Za-z0-9_./-]+\.(go|md|vue|js|sh|json|html)' <<<"$line"); do
    f=${f#./}
    if [ ! -e "$ROOT/$f" ]; then
      REAL=$((REAL+1)); REAL_LINES+=("[$gate] $line")
      return
    fi
  done
  STALE=$((STALE+1))
}

echo "== precheck-branch：骨架门禁 → 分支树 $TREE"
export PRECHECK_SCAN_ROOT="$TREE"
for script in "$ROOT"/scripts/check-*.sh; do
  gate=$(basename "$script")
  out=$(cd "$TREE" && bash "$script" 2>&1); rc=$?
  if [ $rc -eq 0 ]; then
    echo "  PASS  $gate"
    continue
  fi
  while IFS= read -r line; do
    [ -n "$line" ] && classify "$gate" "$line"
  done <<< "$out"
  echo "  RED   $gate（命中已分类）"
done

# gofmt：豁免清单在骨架 Makefile 里，这里用骨架同名判据跑分支树
out=$(cd "$TREE" && gofmt -l . 2>&1)
if [ -n "$out" ]; then
  while IFS= read -r line; do classify "gofmt" "$line"; done <<< "$out"
  echo "  RED   gofmt（命中已分类）"
else
  echo "  PASS  gofmt"
fi

echo "== 汇总：分支自有真红 $REAL 条，陈旧骨架副本 $STALE 条（合并即消失）"
if [ $REAL -gt 0 ]; then
  printf '%s\n' "${REAL_LINES[@]}"
  echo "先修真红再合并；陈旧副本不用管。"
  exit 1
fi
echo "可以合并。"
