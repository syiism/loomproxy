#!/usr/bin/env bash
# scripts/ 下所有 shell 与 node 脚本的**语法门**（待办清单 P120 的出路 B）。
#
# 为什么需要这一条：`make build` 只跑它点名的那些检查脚本，而**不进 vet 的那几个**
# （`viewport-probe.mjs` / `run-viewport-probe.sh` / `mutate-restore.sh` / `deploy.sh` / `precheck-branch.sh`）
# 坏了没有任何东西会报。真实事故：2026-10-07 那颗 `f6320ef` 往 viewport-probe.mjs 的
# **模板字符串内部**加了一句用反引号包名字的注释，反引号提前终结模板 → 整个探针从那天起解析不过，
# 直到 2026-10-10 下一次有人想起来跑它。面板窄视口这条防线**坏了三周，而台账写着 P65 那一族的固化去处是它**。
#
# 判据：**刻意不进门禁的检查，必须先进一次能进门禁的语法门**。
# 这条不是"把探针跑起来"（那要浏览器与临时实例，会把门禁变成等待），
# 只是 `bash -n` 与 `node --check`——零误报的纯解析检查，代价是几毫秒。
#
# 边界（别扩）：
#   - 只查 `scripts/`，不查 `web/src`：前者的产物是运维入口，后者由 pnpm 构建与 css-check 覆盖；
#   - 只查语法，不查逻辑、不查 shellcheck 那类风格问题——风格规则的数量一旦进来，
#     第一条假警报就会教下一个人无视整条检查（判据页那句"会自己制造假警报的规则，价值是负的"）。
set -uo pipefail
cd "$(dirname "$0")/.."

bad=0
for f in scripts/*.sh; do
  [ -f "$f" ] || continue
  if ! err=$(bash -n "$f" 2>&1); then
    echo "$f: shell 语法不过 —— $err" >&2
    bad=$((bad + 1))
  fi
done
if command -v node >/dev/null 2>&1; then
  for f in scripts/*.mjs; do
    [ -f "$f" ] || continue
    if ! err=$(node --check "$f" 2>&1); then
      echo "$f: node 语法不过 —— $(echo "$err" | grep -m1 -E 'Error|error' || echo "$err" | head -1)" >&2
      bad=$((bad + 1))
    fi
  done
else
  # 没装 node 的机器上这条检查没有对象。**出声，不静默通过**——
  # 静默跳过正是 P120 那一族："它绿了"与"它没在数"在读数上长得一样。
  echo "⚠️  本机没有 node：scripts/*.mjs 的语法门跳过（shell 那半仍然查了）" >&2
fi

if [ "$bad" != 0 ]; then
  echo "有 $bad 个脚本解析不过——修它，别把它注释掉（判据：不进门禁的检查也要有一道能进门禁的语法门）" >&2
  exit 1
fi
n_sh=$(ls scripts/*.sh 2>/dev/null | wc -l | tr -d ' ')
n_mjs=$(ls scripts/*.mjs 2>/dev/null | wc -l | tr -d ' ')
echo "脚本语法门通过（shell ${n_sh} 个、mjs ${n_mjs} 个）"
