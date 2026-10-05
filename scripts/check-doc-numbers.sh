#!/usr/bin/env bash
# 文档里那些**自己声称的数**必须等于当场数出来的：
#   ① 执行报告 §8 抬头的「共 N 条」= 那一节里 `### ` 块的个数；
#   ② 报告里那句「清单页 …N 行」= `wc -l docs/规范/待办清单.md`；
#   ③ 归档 README 每行的「N 条已收口原文」= 那个归档文件里 `## P` 条目的个数。
#
# 触发它的经验都是本轮自己撞的：同一个「共 11 条」在补进两块之后没人改过（实测 13），
# 我顺手改成 13 又立刻是错的（再加两块是 15）；那句「清单页 913 行」被凭印象写过四次
# （813/860/890/913），最后一次才是 `wc -l`；而 README 里 `待办清单-P65-P68已收口.md` 那行
# 写「三条已收口原文」而文件里是四条——**文件名里就带着 P65~P68 四个号**，
# 说明那条是搬第三、第四家时忘了改数（第十五遍发现的）。
# 这与 check-judgment-count.sh 治的是同一件事，只是对象从判据页换成了报告与索引——
# **数字要数出来，不该靠自觉**。
set -euo pipefail

cd "${PRECHECK_SCAN_ROOT:-$(dirname "$0")/..}"  # 预检携带分支时由 precheck-branch.sh 指到分支树，默认仍是本仓根

python3 - <<'PY'
import glob, os, re, subprocess, sys

CN = {'一': 1, '二': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9, '十': 10}


def as_int(token):
    return int(token) if token.isdigit() else CN.get(token, -1)


reports = sorted(glob.glob('docs/规范/待办清单修复报告_*.md'))
if not reports:
    print('没有执行报告文件——本检查没有对象（新增一轮报告时别改这个 glob 的形状）。')
    sys.exit(1)

latest = reports[-1]
todo_lines = int(subprocess.run(['wc', '-l', 'docs/规范/待办清单.md'],
                                capture_output=True, text=True).stdout.split()[0])
bad = []

for path in reports:
    lines = open(path, encoding='utf-8').read().split('\n')
    sec8 = None
    for i, l in enumerate(lines):
        # 只认「决策项清单」那一节：报告号更早的那份 §8 是「风险提示」，不是这个形状，也不该被这条规则追溯
        if l.startswith('## 8.') and '决策项清单' in l:
            sec8 = i
            break
    if sec8 is not None:
        blocks = sum(1 for l in lines[sec8:] if l.startswith('### '))
        m = re.search(r'共 (\d+) 条', lines[sec8])
        if not m:
            bad.append('%s：§8 抬头没有「共 N 条」——这一节的条目数就没人负责说清' % path)
        elif int(m.group(1)) != blocks:
            bad.append('%s：§8 抬头写「共 %s 条」，实测该节有 %d 个 `### ` 块' % (path, m.group(1), blocks))

    if path == latest:
        for i, l in enumerate(lines):
            if not l.startswith('清单页从'):
                continue
            nums = re.findall(r'\d+', l)
            if not nums:
                bad.append('%s:%d：那句「清单页从…」里一个数字都没有，检查无从下手' % (path, i + 1))
            elif int(nums[-1]) != todo_lines:
                bad.append('%s:%d：那句写清单页最后 %s 行，`wc -l` 实测 %d 行'
                           % (path, i + 1, nums[-1], todo_lines))

# 规则 ③：归档 README 的索引行——「N 条已收口原文」要等于那个文件里的 `## P` 条目数
readme = 'docs/归档/README.md'
if os.path.exists(readme):
    for i, rl in enumerate(open(readme, encoding='utf-8').read().split('\n'), 1):
        m = re.match(r'^\| `(待办清单-[^`]+\.md)` \| ([一二三四五六七八九十\d]+) ?条已收口原文', rl)
        if not m:
            continue
        fname, declared = m.group(1), as_int(m.group(2))
        found = [d + fname for d in glob.glob('docs/归档/*/') if os.path.exists(d + fname)]
        if not found:
            bad.append('%s:%d：那一行指向 %s，但归档目录里没有这个文件' % (readme, i, fname))
            continue
        real = sum(1 for x in open(found[0], encoding='utf-8').read().split('\n') if x.startswith('## P'))
        if declared != real:
            bad.append('%s:%d：那行写「%s 条已收口原文」，%s 里实测 %d 个 `## P` 条目'
                       % (readme, i, m.group(2), fname, real))

if bad:
    print('文档里自己声称的数与当场数出来的不一致（数字要数出来，不要凭印象）：')
    for b in bad:
        print('  - ' + b)
    print('「行数」那句只查最新一份报告（%s），旧报告里那句是当时的历史读数。' % latest)
    sys.exit(1)
PY
