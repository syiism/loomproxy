#!/usr/bin/env bash
# 文档引用核对：docs 里用反引号点名的**代码标识符**与**带目录的文件路径**，在本树必须还在。
#
# 这一族出现过三种形态，第一种是把它做成扫描的动因（第四十二遍）：
#   ① **过期引用**——`test/quota_period_speaks_test.go` 与 `handlers/admin/quotas.go` 里的
#      `periodNotEnforced`：选了更彻底的处置（连字段一起删）之后，用例随之消失，
#      而判据页与开发约定还写着"已固化成它们"。名字当时是真的（第四十一遍手工核对撞到）。
#   ② **凭印象编的名字**——`admin.GetStats`：第十四遍巡检把它写进 22d2b8a 的提交说明，再抄进
#      P71 的现象段。`git log -S 'GetStats'` 的命中只有文档自己，**这个函数名从来没存在过**
#      （真实落点是 `handlers/admin/admin.go` 的 `Stats`）。比 ① 危险：它不是漂移，
#      是一个看起来比真的更像真的名字，还住在标题写着"现象/证据"的那一段里。
#   ③ **把当时的名字写成今天的名字**（反向的漂移）——同一个 P71 现象段把格式化出口写成
#      `utils.PlatformZone`，而 ① 之前那一处叫 `utils.TZShanghai`（`PlatformZone` 是收口时新建的）。
#      这一类**扫描器查不出来**（两个名字都真实存在过，其中一个今天还在），只有逐句读的时候才会看见；
#      记在这里是因为它和 ② 是同一句话里抓到的，说明"引用在不在"只是这一族的一半。
#
# 为什么不是"反引号里的东西全查"（这条决定了本检查的边界，别扩）：第四十二遍按形状数过一遍，
# 只数 docs（排除归档与轮次报告）里的反引号 token，把候选放宽到"任何 `x.y` 形"再对回代码，
# **实测 593 处路径形断言里 150 处不通**——不通的绝大多数不是错，而是判定前提不成立：
# 简称（`app.go` 指 `app/app.go`）、携带形态（`sources/uxx/provider.go` 在骨架树里天然没有）、
# 仓外文件（`.qoder/plans/…` 被 gitignore、Obsidian 的 `04_检索索引/…` 不在仓库）、
# 占位名（`…_YYYYMMDD.md`）、表.列与设置键（`users.status`、`accounts_meta.multi_ip_yellow`）。
# 一条会产出上百个假警报的规则，价值是负的（门禁台账那句）：它教人的是"无视输出"。
# 所以只收两条**判据确定**的形状：
#   规则 A `包名.标识符`：包名必须出现在本仓 .go 的 package 子句里 → 标识符必须在该包源码中出现。
#           （按包名筛，标准库/三方/局部变量/结构体字段自动出局——这是 A 误报接近于零的主因。
#            起草那次实测：A 类 199 处候选里只有 2 处不通，一处是历史（进豁免）、一处是 ② 的假名字；
#            收完之后的数由脚本每次运行自己打印，注释里不重复——重复的数字就是第二份事实来源。）
#   规则 B 带目录的文件路径：token 含 `/` 且以已知扩展名结尾 → 该路径必须存在。
#           锚点必须是**被 git 跟踪的顶层条目**（`git ls-files` 的顶层段）：`.qoder/plans/…`、
#           Obsidian 那些路径因此出局；携带形态按 `sources/`、`data/`、`docs/数据源/` 三个前缀跳过，
#           跳过条件是"本树有没有 `docs/数据源`"，所以同一份脚本在两棵树里都成立。
#
# 刻意不做的三件事，都写在这里免得下一个人当"参数没调好"：
#   · **不查纯文件名**（`app.go`、`Quotas.vue`、`buju.md`、`check-docs-init.sh`）。散文用简称是
#     合法形态（`app.go` 指 `app/app.go`），而携带形态/仓外附件/历史产物三种情况让"这个名字不在树里"
#     **不再等于"引用失效"**——判定不确定就不该当门禁。规则 B 用"必须带目录"把这一族挡在外面。
#   · **不判标识符归错包**（把 `quota.nextResetTime` 写成 `admin.nextResetTime` 查不出）。
#     本条判据是"引用还在不在"；归属判断要建全仓符号表，代价是每一次重命名都来改豁免。
#   · **不核对历史与将来时**：`docs/归档/**`、`docs/规范/修复报告/待办清单修复报告_*.md`、`docs/方案/**`
#     与文件名含「方案」的页整棵跳过。轮次记录按定义在陈述当时，方案页按定义在陈述打算——
#     把它们当现状断言来核，等于要求设计文档不得提到还没写的代码（第一次跨树跑就撞在
#     `sources/fq/protocol/session.go` 这条"计划新建"上），也等于逼每一次收口去改写历史页（§4 禁止）。
#     历史引用确实需要豁免时，**豁免要落在还在说话的那一页**。
#     **方案页里点名的路径不作仓库事实**（豁免是遮蔽不是判定）：写法侧的约定见下面 `PLAN_DOC_*` 那一段与待办清单 P111。
#   · **不判运行时产物**：`data/` 前缀一律跳过。仓库里只躺着其中一部分，而运维页写的
#     `data/fq_hg/state.json` 那类是**生产盘上的运行态**，不是仓库事实。
#
# 豁免表按 `文件|token` 记（token 用原文，含目录前缀），**每条必须写理由**；
# 且**没被用到的豁免也算红**——引用被删掉而豁免留着，下一轮就没人知道那条历史还归谁负责。
# （这一条当场生效了一次：起草时我按记忆给 `handlers/admin/quotas.go` 加了一条豁免，
#   跑起来报"未用到的豁免"——那个文件其实还在，是我想当然。**先跑再写。**）
#
# 用法：make vet 会自动跑；单独跑 ./scripts/check-doc-refs.sh
set -euo pipefail
cd "${PRECHECK_SCAN_ROOT:-$(dirname "$0")/..}"

python3 - <<'PY'
import os, re, subprocess, sys

DOC_SKIP_DIRS = {'归档'}
DOC_SKIP_NAME_RE = re.compile(r'待办清单修复报告_.*\.md$')
EXTS = {'go', 'js', 'mjs', 'ts', 'vue', 'sh', 'py', 'sql', 'md', 'json', 'css', 'html',
        'sum', 'mod', 'env', 'txt', 'yaml', 'yml', 'toml', 'service', 'example'}
# 形状示例不是具体名字：`/{source}/{action}`、`scripts/check-*.sh`、`…_YYYYMMDD.md`
PLACEHOLDER = ('{', '<', '>', '*', '?', '…', '$', '|', ' / ', '...', 'YYYY', 'NNN', 'xxx', 'XXX')
CJK = ('：', '，', '。', '、', '（', '）', '「', ' 」', ' ')

def docs_paths():
    out = []
    for dirpath, dirnames, filenames in os.walk('docs'):
        if any(s in dirpath.replace(os.sep, '/').split('/') for s in DOC_SKIP_DIRS):
            continue
        for f in filenames:
            if not f.endswith('.md') or DOC_SKIP_NAME_RE.search(f):
                continue
            rel = os.path.join(dirpath, f)
            r = rel.replace(os.sep, '/')
            # 豁免认两条：**目录段**或**文件名**含「方案」。原来只认文件名，于是
            # 「把 `方案-xxx.md` 搬进 `方案/` 并去掉前缀」这一步会把一批将来时页突然变成被核对的现状页，
            # 报出来的还是页里那些早就存在的计划路径（`sources/fq/protocol/session.go` 那类"打算新建"的文件）。
            # 豁免跟着命名走就是脆的——这条在同一次改动里被携带分支撞到（其待办清单 S81 同族：同一份判据两处写）。
            if r.startswith(PLAN_DOC_DIRS) or PLAN_DOC_MARK in f or PLAN_DOC_MARK in r.split('/')[:-1]:
                continue
            out.append(rel)
    for top in ('AGENTS.md', 'README.md'):
        if os.path.exists(top):
            out.append(top)
    return sorted(out)

# 被跟踪的顶层条目 = 仓库事实的锚点；不在其中的带目录引用属仓外，不判
try:
    tracked = subprocess.run(['git', 'ls-files'], capture_output=True, text=True,
                             check=True).stdout.splitlines()
    anchors = {p.split('/')[0] for p in tracked}
except Exception:
    anchors = {e for e in os.listdir('.') if os.path.isdir(e) or e.endswith(('.md', '.go', '.mod', '.sum'))}

# 携带形态（sources/* 与 docs/数据源/）的内容只在 sources/* 分支树里；骨架树里缺席不是"引用失效"。
# 判据用 docs/数据源 是否存在——同一份脚本在两棵树里都成立。
CARRIED = os.path.isdir('docs/数据源')
BRANCH_SIDE_PREFIXES = ('sources/', 'docs/数据源/')
# `data/` 是**运行时产物目录**：仓库里只躺着其中一部分（bookSource.json 之类），
# 而运维页写的 `data/fq_hg/state.json` 这类是生产盘上的运行态——不是仓库事实，两棵树都不判。
RUNTIME_PREFIXES = ('data/',)
# 方案/提案页是**将来时**的页面：`sources/fq/protocol/session.go` 那类是"打算新建的文件"，
# 拿现状断言去核它们，等于要求设计文档不得提到还没写的代码（第一次跨树跑就撞到这形态）。
# 轮次记录（归档、修复报告）已经整棵跳过，这一条是同一判据的另一种形态：**页时态不是现在的，就不判**。
PLAN_DOC_DIRS = ('docs/方案/',)
PLAN_DOC_MARK = '方案'   # 含「方案」的页都是将来时：`方案-数据源接入-…`、`号池方案-…`，以及搬进 `方案/` 后的裸名页
# **豁免是遮蔽，不是判定**（待办清单 P111）。这一类页里点名的路径**不作仓库事实**——
# 半年后没人分得清"打算新建 `x/y.go`"与"那个文件已经不存在了"，而后者长得更像真的（`admin.GetStats` 那一族的形状）。
# 所以写方案页的人负责让形状自带将来时：**计划新建的路径写成 `x/y.go`（新）**，或明写"文件名未定"；
# 已定但本树还没有的路径不要在册的待办/现状页里用反引号点名（反引号在这套判据里就是"本树存在"的断言）。
# 收窄成"只豁免带（新）/（计划）标注那一条"是 P111 的出路②，那要动这里的判定逻辑，本轮没做（先让措辞这一半落地）。

pkg_files = {}
for dirpath, dirnames, filenames in os.walk('.'):
    dirnames[:] = [d for d in dirnames if d not in ('.git', 'node_modules', 'dist', '.qoder', 'vendor')]
    for f in filenames:
        if not f.endswith('.go'):
            continue
        p = os.path.join(dirpath, f)
        try:
            head = open(p, encoding='utf-8').read(4096)
        except (OSError, UnicodeDecodeError):
            continue
        m = re.search(r'^package\s+([A-Za-z0-9_]+)', head, re.M)
        if m:
            pkg_files.setdefault(m.group(1), []).append(p)

token_re = re.compile(r'`([^`\n]{1,90})`')
dot_re = re.compile(r'^([a-z][a-z0-9_]*)\.([A-Za-z_][A-Za-z0-9_]*)$')

EXEMPT = {
    # P71 的现象段在陈述 ①（22d2b8a）之前的七种写法——那一段随 P71 收口整条搬进 docs/归档/，
    # 而归档整棵不核对（DOC_SKIP_DIRS），所以这里的两条豁免（verify.beijingDayStart /
    # docs/规范/待办清单.md|admin.GetStats）已无对应引用，按"豁免也要先删后加"删掉。
    'docs/规范/踩坑判据.md|admin.GetStats':
        '反例本身：P70 那一行的固化格在记这一族第二种形态（编的名字 vs 漂移的名字），不是引用现存代码',
    'docs/规范/门禁台账.md|admin.GetStats':
        '反例本身：台账的「拦住过什么」必须写出具体的名字，那条名字正是本门禁上线时拦下的对象',
    'docs/规范/门禁台账.md|test/quota_period_speaks_test.go':
        '历史：同上，那一格在记上一处过期引用（P70② 删字段时用例一起没了）',
    'docs/规范/开发约定.md|test/quota_period_speaks_test.go':
        '历史：P70② 连 period 字段一起把它删了（3672d22），这一句写的是"当时固化成它"',
    'docs/规范/踩坑判据.md|test/quota_period_speaks_test.go':
        '历史：同上，判据页那一行记录 ① 的固化落点',
}
used = set()
problems = []
n_a = n_b = 0

for d in docs_paths():
    for i, line in enumerate(open(d, encoding='utf-8'), 1):
        for raw in token_re.findall(line):
            t = raw.strip()
            if not t or any(p in t for p in PLACEHOLDER):
                continue
            key = d + '|' + t
            if key in EXEMPT:
                used.add(key)
                continue
            m = dot_re.match(t)
            if m and m.group(1) in pkg_files:          # 规则 A
                pkg, ident = m.group(1), m.group(2)
                if ident.lower() not in EXTS:          # `conf.go` 这种是文件名，交给 B
                    hit = False
                    for f in pkg_files[pkg]:
                        try:
                            src = open(f, encoding='utf-8').read()
                        except (OSError, UnicodeDecodeError):
                            continue
                        if re.search(r'\b' + re.escape(ident) + r'\b', src):
                            hit = True
                            break
                    n_a += 1
                    if not hit:
                        problems.append((d, i, 'A', t, '包 %s 的源码里没有这个标识符' % pkg))
                        continue
            # 规则 B：必须带目录、以已知扩展名结尾、锚点是被跟踪的顶层条目
            if '/' not in t or any(c in t for c in CJK):
                continue
            leaf = t.split('/')[-1]
            if '.' not in leaf or leaf.rsplit('.', 1)[1] not in EXTS:
                continue
            if t.startswith(('/', './')):
                continue
            if t.split('/')[0] not in anchors:
                continue
            if t.startswith(RUNTIME_PREFIXES):
                continue
            if not CARRIED and t.startswith(BRANCH_SIDE_PREFIXES):
                continue
            n_b += 1
            if not os.path.exists(t):
                problems.append((d, i, 'B', t, '本树没有这个路径（顶层锚点在，故属仓库事实）'))

dead = sorted(set(EXEMPT) - used)
for k in dead:
    print('未用到的豁免：%s' % k)
if problems:
    print('文档引用核对失败（%d 处；本次共核对 包名.标识符 %d 处、带目录路径 %d 处）：'
          % (len(problems), n_a, n_b))
    for d, i, rule, t, why in problems:
        print('  %s:%d  [规则%s] `%s`  —— %s' % (d, i, rule, t, why))
    print('\n判据：docs 里反引号点名的标识符与带目录路径必须在本树还在。')
    print('要么改成今天真实的名字（**用 `git log -S` 查，不要凭印象**），')
    print('要么加一条带理由的豁免，并在被引用的那一句里写明它说的是历史。')
    sys.exit(1)
if dead:
    print('文档引用核对失败：%d 条豁免已无对应引用（见上），把豁免删掉。' % len(dead))
    sys.exit(1)
print('文档引用核对通过（包名.标识符 %d 处、带目录路径 %d 处，生效豁免 %d 条）'
      % (n_a, n_b, len(used)))
PY
