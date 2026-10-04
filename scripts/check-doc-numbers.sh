#!/usr/bin/env bash
# 报告里那两个**自己声称的数**必须等于当场数出来的：§8 抬头「共 N 条」、以及那句「清单页 …N 行」。
#
# 判据来源是本轮自己撞的两次：同一个「共 11 条」在 P62/P64 两块补进去之后没人改过（实测 13），
# 我顺手把它改成 13 又立刻是错的（加了 P69/P70 之后是 15）；而「清单页 913 行」那一句
# 在本轮被凭印象写过四次（813/860/890/913），最后一次才是 `wc -l`。
# 这与 check-judgment-count.sh 治的是同一件事，只是对象换成了执行报告——**数字要数出来，不该靠自觉**。
set -euo pipefail

cd "$(dirname "$0")/.."

python3 - <<'PY'
import glob, re, subprocess, sys

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
    if sec8 is None:
        continue
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

if bad:
    print('报告里自己声称的数与当场数出来的不一致（数字要数出来，不要凭印象）：')
    for b in bad:
        print('  - ' + b)
    print('「行数」那句只查最新一份报告（%s），旧报告里那句是当时的历史读数。' % latest)
    sys.exit(1)
PY
