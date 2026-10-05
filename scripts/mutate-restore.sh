#!/usr/bin/env bash
# 变异验证的还原步骤：用自己的备份还原，**不要用 git**。
#
# 为什么是脚本而不是一句提醒（判据页「用 git checkout -- 还原自己的临时变异」那条写了判据之后
# 又被绊到第二次，本轮就是第二次）：`git checkout -- <文件>` / `git restore` 回到的是 **HEAD**，
# 而做变异的那个文件本来就带着本轮未提交的真改动——还原"过头"不报错，只会安静地少掉一段代码，
# 症状通常是"还原后重跑还是红"或者更坏："修复没了而用例说它还在"。
#
# 用法：
#   scripts/mutate-restore.sh <要改的文件> <变异脚本.py> -- <验证命令...>
# 变异脚本自己负责 assert 命中数（`assert s.count(old) == 1`），本脚本负责：
#   ① 变异前留一份工作树基线（不是 HEAD）；② 跑验证命令并原样回报退出码；
#   ③ 用 cp 还原；④ 还原后**逐字节比对基线**，不一致就非零退出并把差异说清楚。
set -euo pipefail

if [ $# -lt 4 ]; then
  echo "用法: $0 <文件> <变异脚本.py> -- <验证命令...>" >&2
  exit 2
fi
target=$1
patch=$2
shift 2
if [ "$1" != "--" ]; then
  echo "$0: 第三个参数必须是 -- （把验证命令与前面的文件参数分开）" >&2
  exit 2
fi
shift
cmd=("$@")

base=$(mktemp /tmp/mutate-baseline-XXXXXX)
trap 'rm -f "$base"' EXIT
cp "$target" "$base"

python3 "$patch"
echo "== 变异已应用（$target），跑验证："
rc=0
"${cmd[@]}" || rc=$?

cp "$base" "$target"
if ! cmp -s "$base" "$target"; then
  echo "$0: 还原后与基线不一致——这次变异没清理干净，别在这个状态上提交" >&2
  exit 1
fi
echo "== 已还原并逐字节核对（MUTATION_EXIT=$rc；rc≠0 且这一行说「已还原」= 用例确实挡住了这次变异）"
