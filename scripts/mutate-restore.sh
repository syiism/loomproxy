#!/usr/bin/env bash
# 变异验证的还原步骤：用自己的备份还原，**不要用 git**。
#
# 为什么是脚本而不是一句提醒（判据页「用 git checkout -- 还原自己的临时变异」那条写了判据之后
# 又被绊到第二次，本轮就是第二次）：`git checkout -- <文件>` / `git restore` 回到的是 **HEAD**，
# 而做变异的那个文件本来就带着本轮未提交的真改动——还原"过头"不报错，只会安静地少掉一段代码，
# 症状通常是"还原后重跑还是红"或者更坏："修复没了而用例说它还在"。
#
# 第三十五遍补的两条（都是这一轮真撞到的）：
#   ① **还原范围从"一个 target"扩到"所有脏文件"**：把 patch 改的文件写在参数之外（脚本参数指 `auth.go`、
#      patch 里 open 的却是 `setting.go`）时，旧版只会"还原"那个没被碰过的文件，并且照样打印"已还原并逐字节核对"——
#      于是真变异留在工作树里，而下一次 `go test` 复跑红才被发现。现在备份的是 `git diff --name-only` 的全部文件
#      （含 target），核对的是"这批文件的清单与哈希是否与变异前一致"，那才是一句能失败的话。
#   ② **target 没被改动就直接退出**：一次没改到任何东西的变异，验证结果毫无意义（红与绿都不是被测代码给的）。
#
# 用法：
#   scripts/mutate-restore.sh <要改的文件> <变异脚本.py> -- <验证命令...>
# 变异脚本自己负责 assert 命中数（`assert s.count(old) == 1`），本脚本负责：
#   ① 变异前留一份工作树基线（不是 HEAD，且**覆盖所有脏文件**）；② 跑验证命令并原样回报退出码；
#   ③ 用 cp 还原；④ 还原后**逐文件比对哈希**，不一致就非零退出并把差异说清楚。
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

snap=$(mktemp -d /tmp/mutate-snap-XXXXXX)
before=$(mktemp /tmp/mutate-before-XXXXXX)
after=$(mktemp /tmp/mutate-after-XXXXXX)
# 快照默认删；只有"还原没核对上"这一条路径留着它，并把路径打在报错里——
# 那种状态下工作树已经被脚本动过，得给下一个人一个手工还原的去处。
keep_snap=0
cleanup() {
  if [ "$keep_snap" = 1 ]; then
    echo "$0: 变异前的快照留在 $snap（逐文件 cp 回去即可还原）" >&2
  else
    rm -rf "$snap"
  fi
  rm -f "$before" "$before.files" "$after"
}
trap cleanup EXIT

# 要还原的文件 = target ∪ 相对 HEAD 脏的已跟踪文件
{
  printf '%s\n' "$target"
  git diff --name-only
} | sort -u > "$before.files"

: > "$before"
while IFS= read -r f; do
  [ -f "$f" ] || continue
  mkdir -p "$snap/$(dirname "$f")"
  cp "$f" "$snap/$f"
  md5sum "$f" >> "$before"
done < "$before.files"

python3 "$patch"

if cmp -s "$snap/$target" "$target"; then
  echo "$0: 变异脚本没有改动 target（$target）——它写的是别的文件，这次验证不成立。" >&2
  echo "   已把改动过的文件还原回基线；把 patch 改成作用于参数里那个文件再跑。" >&2
  while IFS= read -r f; do
    if [ -f "$snap/$f" ]; then cp "$snap/$f" "$f"; fi
  done < "$before.files"
  exit 3
fi

echo "== 变异已应用（$target），跑验证："
rc=0
"${cmd[@]}" || rc=$?

: > "$after"
while IFS= read -r f; do
  # 刻意写成 if 而不是 `[ -f … ] && cp …`：`set -e` 下测试失败会让这条列表返回非零，
  # 而它正好是循环体的最后一条语句——脚本会在还原到一半时退出（这本身就是"还原"这一步的坑）
  if [ -f "$snap/$f" ]; then cp "$snap/$f" "$f"; fi
  if [ -f "$f" ]; then md5sum "$f" >> "$after"; fi
done < "$before.files"

if ! diff -q "$before" "$after" >/dev/null; then
  keep_snap=1
  echo "$0: 还原后这批文件与变异前的哈希不一致（这次变异没清理干净，别在这个状态上提交）：" >&2
  diff "$before" "$after" >&2 || true
  exit 1
fi
echo "== 已还原并逐文件核对哈希（覆盖 $(wc -l < "$before") 个脏文件；MUTATION_EXIT=$rc；"
echo "   rc≠0 且这一行说「已还原」= 用例确实挡住了这次变异）"
