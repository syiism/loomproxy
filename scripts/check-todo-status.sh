#!/usr/bin/env bash
# 待办清单 / 已收口索引 / 归档 三者的一致性检查
# （判据页《条目做完而状态行还写着「待拍」》那一行的机器化部分）。
#
# 这一族连着出现过三次（P34、P70，以及 2026-10-06 的 P48——最后一次是反方向：状态行说"尚未接"，
# 而代码早在 3bc078d 接完了，于是有人领了一条不存在的任务）。那一行自己写着
# 「它被做成扫描的那天就该删行」，所以这里做的是**能确定判定的那几条**：
#
#   规则 1：同一条目不得**同时**有清单页正文与归档页正文（整条搬出后原地只留索引一行；
#           两处都有正文 = 两份自称现状的事实来源，正是那一行说的病）。
#   规则 2：清单页与索引页里每一条归档引用（`归档：\`文件名\`` 与相对链接），**目标文件必须存在**。
#           第三十九遍跑手工自查时撞到的第一个误报就是"只扫了某一个归档文件"——
#           P98/P101 各有自己的整条文件，只读 P35-P95 那份就会把它们报成"没归档"。
#           所以这里按**链接目标去查**（`docs/归档/**` 全树），不是按某个文件名去查。
#   规则 3：归档里每一条 `## PXX` 正文都必须在**索引页**有一行，反过来索引行也不许指向不存在的正文。
#           这一条原来读的是清单页；2026-10-07 索引拆到 `docs/规范/已收口索引.md` 之后必须跟着换页——
#           **门禁读错页不会报错，只会安静地变成"永远通过"**，那正是判据页《断言空转》那一族。
#           所以这里两条方向都查（缺行、多行都会红），并在成功那行打印三个数让人看得出它在数什么。
#   规则 4（同日加）：清单页**不许出现索引行**（`^\| PXX`），索引页**不许出现整条正文**（`^## PXX`）。
#           这是维护者那句"待办清单只放待办任务"的机器口径：两页各自只装一种东西。
#
# 刻意**没做**的一条：那一行还写着「状态行含待拍而正文含已处理」这种互斥词对。
# 本仓合法的状态行大量长这样：「①已修；②等拍」「已定案·非缺陷；代码已落 X，进库那次单独等授权」——
# 标题与正文同时出现"已X"和"待Y"是**正常形状**，不是矛盾。真矛盾要判的是
# "同一个子编号既标已收口又标待拍"，那是语义判断，误报的代价是下一个人开始无视这条检查
# （本页反复写的那件事）。所以词对那半句留给人（循环规范 §3 的回写步骤）。
#
# 这里还有一段**被删掉的规则**，值得留着因为它就是上面那段话在演：
# 我起初加了一条「反引号里像哈希的字符串，长度必须是 7/8/9/10/11/12/40」，第一次跑就报
# `1024` 不是提交号——那是一个体积数字。**纯数字的十六进制串本身歧义**，
# 所以这条不是"参数没调好"而是方向错，删掉而不是放宽。判据还是那句：
# **一条会自己制造假警报的规则，价值是负的**——它教给下一个人的是"无视这条检查的输出"。
#
# 用法：make vet 会自动跑；单独跑 ./scripts/check-todo-status.sh
set -euo pipefail
cd "${PRECHECK_SCAN_ROOT:-$(dirname "$0")/..}"

python3 - <<'PYEOF'
import collections, glob, os, re, sys

PAGE = 'docs/规范/待办清单.md'
INDEX = 'docs/规范/已收口索引.md'
if not os.path.exists(PAGE):
    sys.exit(0)
page = open(PAGE, encoding='utf-8').read()
index = open(INDEX, encoding='utf-8').read() if os.path.exists(INDEX) else ''
arch_files = sorted(glob.glob('docs/归档/**/*.md', recursive=True))
arch_bodies = {f: open(f, encoding='utf-8').read() for f in arch_files}


def strip_fences(text):
    """页里可能把表格当样例放进代码围栏；围栏里的行不参与判定。"""
    out, fence = [], False
    for line in text.split('\n'):
        if line.lstrip().startswith('```'):
            fence = not fence
            continue
        if not fence:
            out.append(line)
    return '\n'.join(out)


page, index = strip_fences(page), strip_fences(index)
errs = []

# 规则 1：清单页与归档页不得同时有正文
on_page = set(re.findall(r'^## (P\d+)\b', page, re.M))
arch_ids = set()
for f, body in arch_bodies.items():
    in_arch = set(re.findall(r'^## (P\d+)\b', body, re.M))
    arch_ids |= in_arch
    for pid in sorted(on_page & in_arch):
        errs.append(f'{f}: {pid} 在归档页有整条正文，而清单页也还留着一节——两份自称现状的事实来源')

# 规则 2：归档引用必须指得到文件（两页都查，按链接目标查）
exists = {os.path.basename(f) for f in arch_files}
linked = set(re.findall(r'归档：`([^`]+\.md)`', page)) | set(re.findall(r'归档：`([^`]+\.md)`', index))
linked |= set(re.findall(r'\]\(\.\./归档/[^)]*/([^)]+\.md)\)', page))
linked |= set(re.findall(r'\]\((?:\.\./)?归档/[^)]*/([^)]+\.md)\)', index))
for name in sorted(linked):
    if name not in exists:
        errs.append(f'归档引用 `{name}` 指不到 docs/归档/** 下的文件（查的是清单页与已收口索引两页）')

# 规则 3：归档正文 ⟺ 索引行，两个方向都查
#   正向（缺行）判据是 `^## PXX`；反方向"有行没正文"要认那些**编号不写成标题**的归档页，
#   所以判"这一行是否指得到一个存在的归档文件"，指不到才算红。
#   ARCH_ID_EXEMPT 是带理由的豁免表——豁免必须署名并写清形状，否则下一个人只会把它当噪声删掉。
live = set(re.findall(r'^\| (P\d+)\b', index, re.M))
for pid in sorted(arch_ids - live):
    errs.append(f'{INDEX}: 归档里有 {pid} 的整条正文，索引表却没有这一行（搬完要补一行——编号是下一轮唯一的入口）')
ARCH_ID_EXEMPT = {
    'P49': '整条正文住在 2026-10-瘦身轮/巡检记录-第2-40遍.md，那一页的标题形状是 `### 第 N 遍`'
           '（巡检日志本来就没有条目编号），所以 `^## PXX` 认不出它；索引行指得到那个文件即算落处',
}
rowfile = dict(re.findall(r'^\| (P\d+)\b.*?`([^`]+\.md)`', index, re.M))
for pid in sorted(live - arch_ids):
    if pid in ARCH_ID_EXEMPT:
        f = rowfile.get(pid, '')
        if f and f in exists:
            continue
        errs.append(f'{INDEX}: {pid} 在豁免表里，但那一行没指得到存在的归档文件（豁免的前提是"另有落处"）')
        continue
    errs.append(f'{INDEX}: 索引行 {pid} 指不到对应的归档正文（也没有 `## PXX` 那样的落处）——先搬正文，再加索引行')

# 规则 5（同日拆页后加）：索引行不得有重复编号。
#   拆完当天一次命中 9 对：同一件事的两个时期各写了一行（一行"已处理·细节"、一行批量拍板的套话），
#   这就是"索引越来越大"里真正该丢的那一半——不是字多，是同一件事有两份自称现状的说法。
counts = collections.Counter(re.findall(r'^\| (P\d+)\b', index, re.M))
for pid, n in sorted(counts.items()):
    if n > 1:
        errs.append(f'{INDEX}: {pid} 有 {n} 行索引——同一件事的两份说法，合并成一行（保留细节那行，把套话那行的提交号与归档指针并进去）')

# 规则 4：两页各装一种东西
if re.search(r'^\| P\d+\b', page, re.M):
    errs.append(f'{PAGE}: 这一页还留着索引行——索引住在 {INDEX}，本页只放还开着的条目')
if re.search(r'^## P\d+\b', index, re.M):
    errs.append(f'{INDEX}: 这一页还留着整条正文——正文在 docs/归档/**，本页只放一行索引')

if errs:
    print('待办清单 / 已收口索引 / 归档 三者不一致（状态行是下一轮唯一的入口，两处并存等于两份事实）：',
          file=sys.stderr)
    for e in errs:
        print('  - ' + e, file=sys.stderr)
    sys.exit(1)
print(f'check-todo-status OK（清单页在册 {len(on_page)} 条、索引 {len(live)} 行、归档正文 {len(arch_ids)} 条，三者对得上）')
PYEOF
