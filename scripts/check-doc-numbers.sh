#!/usr/bin/env bash
# 文档里那些**自己声称的数**必须等于当场数出来的：
#   ① 执行报告 §8 抬头的「共 N 条」= 那一节里 `### ` 块的个数；
#   ② 报告里那句「清单页 …N 行」= `wc -l docs/规范/待办清单.md`；
#   ③ 归档 README 每行的「N 条已收口原文」= 那个归档文件里 `## P` 条目的个数；
#   ④ 门禁台账 rule 2 的体量读数（清单页行/在册条、已收口索引行/索引条、判据页行、AGENTS 字节）= 现测。
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


reports = sorted(glob.glob('docs/规范/修复报告/待办清单修复报告_*.md'))
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

# 规则 ④（2026-10-06 加）：门禁台账 rule 2 那三条**体量读数**必须等于现测。
# 加这条的经过本身就是判据：台账那句「清单页 585 行 / 在册 9 条」是一小时前按 `wc -l` 写的，
# 往里加一条 P104 之后就变了，而**没有任何检查会说它过期**——报告里那句有规则② 看着，台账那句没有。
# 「写下的时候是对的」不是对的定义：这三条数是体量阈值的判据本身，一旦与文件脱钩，
# 阈值就变成一句自我认证的话（「规则被兑现一次，比新加一条规则有用」那句也一起作废）。
ledger = 'docs/规范/门禁台账.md'
if os.path.exists(ledger):
    txt = open(ledger, encoding='utf-8').read()

    def wc(path, opt):
        return int(subprocess.run(['wc', opt, path], capture_output=True, text=True).stdout.split()[0])

    # AGENTS_LIMIT 的唯一落点在这里：台账 rule 2 那句「AGENTS.md > 30KB」的机器形态。
    # 分档后两棵树共用这一个数——携带树量的是同源部分，见下面那段注释。
    AGENTS_LIMIT = 30 * 1024

    real_index = (wc('docs/规范/已收口索引.md', '-l'),
                  len(re.findall(r'^\| P\d+\b', open('docs/规范/已收口索引.md', encoding='utf-8').read(), re.M)))
    real_todo = (wc('docs/规范/待办清单.md', '-l'),
                 sum(1 for l in open('docs/规范/待办清单.md', encoding='utf-8') if l.startswith('## P')))
    real_judg = (wc('docs/规范/踩坑判据.md', '-l'),)
    # AGENTS 体量**只在骨架树上核**（第四次命中同一条判据：检查的口径窄于事实的分布，这次窄的是"哪棵树"）。
    # 上一版让分支树去核分支自己那个数，结果是：**携带分支每次 merge 都要改 §0 锚点，那个数写下即过期**
    # ——一台每次例行合并都必然红的机器，教给下一轮的是"超线也不要紧"，比不设阈值更坏。
    # 而台账这一页两棵树共用（接缝判据不许分支单方面改它），所以分支侧不可能把它维持为真；
    # 分支那一格因此退回量法（`wc -c AGENTS.md` + S61），这里**出声说明不核**，不静默跳过。
    agents_txt = open('AGENTS.md', encoding='utf-8').read()
    if '本分支与骨架的差异' in agents_txt:
        # 案 A（维护者 2026-10-08 拍：阈值按树分档）。携带树上核的不是全文，而是
        # 「与骨架同源的那一部分」= 全文 − §0 那一节：
        #   · 随每次 merge 漂移的是同源部分（骨架改了 §5~§13，分支就得跟着走）；
        #   · §0 是分支自有文本，把它算进同一条线，等于让「分支存在」这件事本身常驻超线——
        #     上一版因此只能出声说明不核，那条线在携带树上不说话（比不设线更坏）。
        # 分档之后这条线重新拦得住东西：把同源长段留在分支树上，红的是这里，不是下次合并的冲突。
        m0 = re.search(r'^## 0[.]', agents_txt, re.M)
        m1 = re.search(r'^## 1[.]', agents_txt, re.M)
        if not (m0 and m1):
            bad.append('AGENTS.md：携带树找不到 §0/§1 的节标题，规则④ 的分档量法在这一棵树上无从下手')
        else:
            own = len(agents_txt[m0.start():m1.start()].encode())
            full = len(agents_txt.encode())
            shared = full - own
            if shared > AGENTS_LIMIT:
                bad.append('AGENTS.md：携带树的同源部分 %d 字节，超阈值 %d（全文 %d − §0 %d）——'
                           '要么把那段提回 main，要么逐字搬进 docs/ 对应页，别在分支树上留着'
                           % (shared, AGENTS_LIMIT, full, own))
            else:
                print('AGENTS 体量按树分档（案 A）：携带树核同源部分 %d 字节（全文 %d − §0 %d），阈值 %d —— 在线内'
                      % (shared, full, own, AGENTS_LIMIT))
    else:
        m_agents = re.search(r'`AGENTS\.md` \*\*(\d+) 字节\*\*', txt)
        if not m_agents:
            bad.append('%s：AGENTS 体量那条句式变了，规则④ 对这一项就变成看着空气' % ledger)
        else:
            real_bytes = wc('AGENTS.md', '-c')
            if int(m_agents.group(1)) != real_bytes:
                bad.append('%s：AGENTS.md 体量读数写的是 %s，现测 %d 字节'
                           % (ledger, m_agents.group(1), real_bytes))
    for pat, key, real in (
        (r'清单页 \*\*(\d+) 行 / 在册正文 (\d+) 条\*\*', '清单页', real_todo),
        (r'判据页 (\d+) 行', '判据页', real_judg),
        (r'已收口索引 \*\*(\d+) 行 / 索引 (\d+) 条\*\*', '已收口索引', real_index),
    ):
        m = re.search(pat, txt)
        if not m:
            bad.append('%s：体量读数那条句式变了（%s），规则④ 就变成看着空气——**改那句话要连这条一起改**'
                       % (ledger, pat))
            continue
        want = tuple(int(x) for x in m.groups())
        if want != real:
            bad.append('%s：%s 写的是 %s，现测 %s（行数 `wc -l`、在册条数按 `^## P`、字节 `wc -c`）'
                       % (ledger, key, ' / '.join(map(str, want)), ' / '.join(map(str, real))))

if bad:
    print('文档里自己声称的数与当场数出来的不一致（数字要数出来，不要凭印象）：')
    for b in bad:
        print('  - ' + b)
    print('「行数」那句只查最新一份报告（%s），旧报告里那句是当时的历史读数。' % latest)
    sys.exit(1)
PY
