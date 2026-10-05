#!/usr/bin/env bash
# 临界区的解锁一律走 defer：**把 `Unlock()` 写在函数尾部，panic 会把它一起跳过，
# 于是锁被永久持有**——症状不是"少一行读数"，而是下一个走到这里的请求直接卡死，
# 而最外层 gin 的 recovery 把 panic 兜住了，所以进程照旧 `is-active`、没有崩溃日志。
#
# 判据来源是同一个形状的第三次出现：
#   ① 待办清单 P77/P78（号池的两条后台协程 panic 兜底 + 「临界区一律 defer 解锁」，落成 `p.locked(fn)`）；
#   ② P93（`base/metrics.go` 的 `RecordCall`：尾部解锁 + 一次 nil 解引用 = 监控锁被永久持有，
#      实测把测试段从"红一条断言"变成"整套件超时"）；
#   ③ 本轮按②的判据全仓数一遍，还有 **19 个函数**是尾解锁（分布在 15 个文件，含 P78 那一轮
#      已经"收口"的 `base/pool/state.go`、`base/pool/status.go`）——**说明那句判据当时只是约定**。
#      第三次不再靠人想起来（同 `check-ticker-stopped.sh` 的来历，待办清单 P80/P89）。
#
# 规则（刻意只有一条，且**不试图**判断"该不该 defer"这种需要理解语义的事）：
#   一个函数里出现的每一把锁（`X.Lock()` / `X.RLock()`）：
#     · 不许有裸的 `X.Unlock()` / `X.RUnlock()`（非 defer 的解锁行）；
#     · `X.Lock()` 的次数必须等于 `defer X.Unlock()` 的次数（RLock 同理）。
#   两条合起来就排除了尾解锁，也排除了"锁在一处加、在另一处解"。
#
# **写法提示（不是豁免）**：一个函数里有**多段**临界区（读一段、放锁去做慢事、再写一段），
# 千万别给第一段就地写 `defer`——那会把持锁区间扩到整段慢操作（网络请求、休眠）头上，
# 比尾解锁更糟（正是待办清单 P81「持锁调外部实现必须带截止」反着干）。正确做法是把每一段
# 包进一个闭包，让 defer 的作用域就是那一段：
#     func() { mu.Lock(); defer mu.Unlock(); /* 只这一小段 */ }()
# 同理，**循环体里的 Lock 不能直接配 defer**（defer 到函数返回才执行 = 第二次 tick 就死锁），
# 也要包闭包。
#
# 豁免：无。这条规则是全仓的，加了豁免就等于把"会不会复发"交回给记性。
#   真要豁免（例如确实需要"跨函数交接锁"的少见写法），在本脚本里写明文件+函数+理由，
#   并同步登记进待办清单——**默认是红**。
set -euo pipefail

cd "${PRECHECK_SCAN_ROOT:-$(dirname "$0")/..}"  # 预检携带分支时由 precheck-branch.sh 指到分支树，默认仍是本仓根

python3 - <<'PY'
import os, re, sys

SKIP_DIRS = {'web', 'node_modules', '.git', 'data'}
# 顶层函数体：从 `func ...{` 到下一行行首的 `}`（本仓 gofmt 后成立；跨行签名的函数会被整段包含）
FUNC_RE = re.compile(r'\nfunc (?:\([^)]*\)\s*)?[A-Za-z_][\w]*\s*\([^\n]*\)[^\n]*\{\n(.*?)\n\}\n', re.S)

def lock_names(body):
    return set(re.findall(r'^\t+([A-Za-z_][\w.]*?)\.(?:RLock|Lock)\(\)', body, re.M))

bad = []
for root, dirs, files in os.walk('.'):
    dirs[:] = [d for d in dirs if d not in SKIP_DIRS and not d.startswith('.')]
    for fn in sorted(files):
        if not fn.endswith('.go') or fn.endswith('_test.go'):
            continue
        path = os.path.join(root, fn)
        src = open(path, encoding='utf-8').read()
        for m in FUNC_RE.finditer('\n' + src + '\n'):
            name = m.group(0).split('\n')[1].strip()[:70]
            body = m.group(1)
            for lk in lock_names(body):
                esc = re.escape(lk)
                bare = re.findall(r'^\t+' + esc + r'\.(?:RUnlock|Unlock)\(\)', body, re.M)
                if bare:
                    bad.append((path, name, lk, '尾解锁（panic 会跳过它，锁被永久持有）'))
                    continue
                n_w = len(re.findall(r'^\t+' + esc + r'\.Lock\(\)', body, re.M))
                n_wd = len(re.findall(r'defer ' + esc + r'\.Unlock\(\)', body))
                n_r = len(re.findall(r'^\t+' + esc + r'\.RLock\(\)', body, re.M))
                n_rd = len(re.findall(r'defer ' + esc + r'\.RUnlock\(\)', body))
                if n_w != n_wd or n_r != n_rd:
                    bad.append((path, name, lk,
                                '加锁 %d/%d 次与 defer 解锁 %d/%d 次配不齐（写 Lock 不加 defer，或锁在别处解）'
                                % (n_w, n_r, n_wd, n_rd)))

if bad:
    print('临界区解锁姿势不合格（一律 defer；多段临界区请各包一个闭包）：')
    for path, name, lk, why in bad:
        print('  - %s :: %s :: %s —— %s' % (path, name, lk, why))
    print('共 %d 处。判据与来历见本脚本头部；改法见待办清单 P93。' % len(bad))
    sys.exit(1)
PY

echo "check-lock-defer OK"
