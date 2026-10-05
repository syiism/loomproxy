#!/usr/bin/env bash
# 待办清单与归档的一致性检查（判据页《条目做完而状态行还写着「待拍」》那一行的机器化部分）。
#
# 这一族连着出现过三次（P34、P70，以及 2026-10-06 的 P48——最后一次是反方向：状态行说"尚未接"，
# 而代码早在 3bc078d 接完了，于是有人领了一条不存在的任务）。那一行自己写着
# 「它被做成扫描的那天就该删行」，所以这里做的是**能确定判定的那两条**：
#
#   规则 1：同一条目不得**同时**存在于清单页与归档页（整条搬出后原地只留索引一行；
#           两处都有正文 = 两份自称现状的事实来源，正是那一行说的病）。
#   规则 2：清单页里每一个「归档：`文件名`」与页顶每一条归档指针，**目标文件必须存在**。
#           第三十九遍跑手工自查时撞到的第一个误报就是"只扫了某一个归档文件"——
#           P98/P101 各有自己的整条文件，只读 P35-P95 那份就会把它们报成"没归档"。
#           所以这里按**链接目标去查**（`docs/归档/**` 全树），不是按某个文件名去查。
#
# 刻意**没做**的一条：那一行还写着「状态行含待拍而正文含已处理」这种互斥词对。
# 本仓合法的状态行大量长这样：「①已修；②等拍」「已定案·非缺陷；代码已落 X，进库那次单独等授权」——
# 标题与正文同时出现"已X"和"待Y"是**正常形状**，不是矛盾。真矛盾要判的是
# "同一个子编号既标已收口又标待拍"，那是语义判断，误报的代价是下一个人开始无视这条检查
# （本页反复写的那件事）。所以词对那半句留给人（循环规范 §3 的回写步骤）。
#
# 这里还有一段**被删掉的第三规则**，值得留着因为它就是上面那段话在演：
# 我起初加了一条「反引号里像哈希的字符串，长度必须是 7/8/9/10/11/12/40」，第一次跑就报
# `1024` 不是提交号——那是一个体积数字。**纯数字的十六进制串本身歧义**，
# 所以这条不是"参数没调好"而是方向错，删掉而不是放宽。判据还是那句：
# **一条会自己制造假警报的规则，价值是负的**——它教给下一个人的是"无视这条检查的输出"。
#
# 用法：make vet 会自动跑；单独跑 ./scripts/check-todo-status.sh
set -euo pipefail
cd "${PRECHECK_SCAN_ROOT:-$(dirname "$0")/..}"

python3 - <<'PY'
import glob, os, re, sys

PAGE = 'docs/规范/待办清单.md'
if not os.path.exists(PAGE):
    sys.exit(0)
page = open(PAGE, encoding='utf-8').read()
arch_files = sorted(glob.glob('docs/归档/**/*.md', recursive=True))
arch_bodies = {f: open(f, encoding='utf-8').read() for f in arch_files}

errs = []

# 规则 1：同一条目不得两处都有正文
on_page = set(re.findall(r'^## (P\d+)\b', page, re.M))
for f, body in arch_bodies.items():
    in_arch = set(re.findall(r'^## (P\d+)\b', body, re.M))
    for pid in sorted(on_page & in_arch):
        errs.append(f'{f}: {pid} 在归档页有整条正文，而清单页也还留着一节——两份自称现状的事实来源')

# 规则 2：归档链接必须指向存在的文件（按链接目标查，不按某个固定文件名查）
exists = {os.path.basename(f) for f in arch_files}
linked = set(re.findall(r'归档：`([^`]+\.md)`', page))
linked |= set(re.findall(r'\]\(\.\./归档/[^)]*/([^)]+\.md)\)', page))
for name in sorted(linked):
    if name not in exists:
        errs.append(f'{PAGE}: 引用了归档页 `{name}`，但 docs/归档/** 下没有这个文件')

if errs:
    print('待办清单与归档不一致（状态行是下一轮唯一的入口，两处并存等于两份事实）：', file=sys.stderr)
    for e in errs:
        print('  - ' + e, file=sys.stderr)
    sys.exit(1)
print('check-todo-status OK')
PY
