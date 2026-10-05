#!/usr/bin/env bash
# 踩坑判据页顶上那句「本轮 N 条：a 已 / b 部分 / c 未」必须等于当场数出来的数。
#
# 判据来源是这一页自己的历史：同一段里排过七句各自自称实测的分布，其中一对的差是一行模板被算成条目。
# 「数字要数出来」这件事反复靠自觉失败过，所以它自己就该是条判据——而判据的终点是固化。
#
# 数法（写在这里，因为「写明怎么数的」正是那对矛盾数字缺的东西）：
#   条目 = 「## 条目格式」之后的每一个 `## ` 标题（之前的全是本页的元说明，不是坑）；
#   状态 = 该条目正文里第一条 `- **固化**` 后**反引号内的第一个词**（已/部分/未）；
#   「条目格式」章节里那行模板长得很像条目，靠上面这条位置规则天然排除。
set -euo pipefail

cd "${PRECHECK_SCAN_ROOT:-$(dirname "$0")/..}"  # 预检携带分支时由 precheck-branch.sh 指到分支树，默认仍是本仓根

python3 - <<'PY'
import re, sys

path = 'docs/规范/踩坑判据.md'
lines = open(path, encoding='utf-8').read().split('\n')

start = None
for i, l in enumerate(lines):
    if l.startswith('## 条目格式'):
        start = i
        break
if start is None:
    print('判据页找不到「## 条目格式」这一节——数法的前提没了，本检查无从下手。')
    sys.exit(1)

entries = []
cur = None
for l in lines[start + 1:]:
    if l.startswith('## '):
        cur = {'title': l[3:].strip(), 'fixed': None}
        entries.append(cur)
    elif cur is not None and cur['fixed'] is None and l.startswith('- **固化**'):
        m = re.search(r'`([^`]+)`', l)
        cur['fixed'] = m.group(1)[0] if m else '?'

counts = {'已': 0, '部分': 0, '未': 0}
bad = []
for e in entries:
    if e['fixed'] == '已':
        counts['已'] += 1
    elif e['fixed'] == '部':
        counts['部分'] += 1
    elif e['fixed'] == '未':
        counts['未'] += 1
    else:
        bad.append(e)

if bad:
    print('以下条目没有 `**固化**` 字段，或该字段第一个反引号里不是 已/部分/未（准入判据要求每条填得出固化去处）：')
    for e in bad:
        print('  - %s（读到：%r）' % (e['title'][:40], e['fixed']))
    sys.exit(1)

declared = []
for l in lines[:start]:
    m = re.search(r'\*\*(\d+) 条：(\d+) 已 / (\d+) 部分 / (\d+) 未\*\*', l)
    if m:
        declared.append(m)

if not declared:
    print('判据页顶部没有「**N 条：a 已 / b 部分 / c 未**」这一句——每轮收尾要重数一次并只留最新的那个数。')
    sys.exit(1)
if len(declared) > 1:
    print('判据页顶部有 %d 句分布数字——本页的规矩是只留最新的一条，历史数字归 git log 管。' % len(declared))
    sys.exit(1)

n, a, b, c = (int(g) for g in declared[0].groups())
total = len(entries)
real = (counts['已'], counts['部分'], counts['未'])
if (n, a, b, c) != (total,) + real:
    print('判据页写的分布与当场数出来的不一致：')
    print('  页上：%d 条：%d 已 / %d 部分 / %d 未' % (n, a, b, c))
    print('  实数：%d 条：%d 已 / %d 部分 / %d 未' % (total, *real))
    print('数法见本脚本头部注释。要么改页上那句，要么这条目本来就不该进来（没有固化手段的坑先别写）。')
    sys.exit(1)
PY
