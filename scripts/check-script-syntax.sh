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
#   - 查 `scripts/` 与 **Makefile 的 recipe**，不查 `web/src`：前两者的产物是运维入口与构建纪律本身，
#     后者由 pnpm 构建与 css-check 覆盖；
#   - 只查语法，不查逻辑、不查 shellcheck 那类风格问题——风格规则的数量一旦进来，
#     第一条假警报就会教下一个人无视整条检查（判据页那句"会自己制造假警报的规则，价值是负的"）。
#
# Makefile 那一段管两格，各自拦什么要写清（拦不住的那半别装作拦得住）：
#   ① **recipe 必须声明跑它的 shell**：没声明时 make 用 `/bin/sh`，而 `/bin/sh` 指向谁是发行版的事——
#     CI 首跑就是死在这上面（`nav-check` 的 `<(...)` 在 dash 上炸，本地 bash-as-sh 跑了一年多没红，待办清单 P128）。
#   ② **每条 recipe 得解析得过**：这一段是纯 `bash -n`，和 scripts/*.sh 同代价（毫秒级）。
#     它**拦不住方言**（`<(...)` 在 bash 下语法合法）——方言那一半靠 ① 的声明加上"在非 bash-sh 的机器上
#     真跑一次门禁"（现在就是 CI 在提供这台机器）。所以这条的档位是 `部分`，不是 `已`。
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

# ---- Makefile 的 recipe：① 必须声明跑它的 shell；② 逐条解析得过 ----
mk_bad=0
if ! grep -q '^SHELL := ' Makefile; then
  echo "Makefile：没声明 SHELL——recipe 由 /bin/sh 跑，而 /bin/sh 指向谁是发行版的事（P128：dash 那侧整段 make vet 从没跑起来过）" >&2
  mk_bad=$((mk_bad + 1))
fi
# 抽 recipe 行（制表符开头），把 make 的 `$$` 还原成 shell 的 `$` 再交给 bash 解析。
# 行尾 `\` 的续行原样留着——它们本来就是要连起来解析的。
rec=$(mktemp)
trap 'rm -f "$rec"' EXIT
awk '/^\t/ {sub(/^\t/, ""); sub(/^[-+@]/, ""); gsub(/\$\$/, "$"); print}' Makefile > "$rec"
n_rec=$(grep -c '' "$rec")
if [ "$n_rec" = 0 ]; then
  # **出声，不静默通过**（同上面 node 那一格）：抽不到 recipe 行只可能是 Makefile 改了形状或 awk 写错了，
  # 而"它绿了"与"它没在数"在读数上长得一样。
  echo "⚠️  Makefile：一条 recipe 行都没抽到——这条检查没有对象，去看抽行的判据是不是还成立" >&2
elif ! err=$(bash -n "$rec" 2>&1); then
  echo "Makefile 的 recipe 解析不过（共 ${n_rec} 行）—— $err" >&2
  mk_bad=$((mk_bad + 1))
fi

bad=$((bad + mk_bad))
if [ "$bad" != 0 ]; then
  echo "有 $bad 处解析不过——修它，别把它注释掉（判据：不进门禁的检查也要有一道能进门禁的语法门）" >&2
  exit 1
fi
n_sh=$(ls scripts/*.sh 2>/dev/null | wc -l | tr -d ' ')
n_mjs=$(ls scripts/*.mjs 2>/dev/null | wc -l | tr -d ' ')
echo "脚本语法门通过（shell ${n_sh} 个、mjs ${n_mjs} 个、Makefile recipe ${n_rec} 行）"
