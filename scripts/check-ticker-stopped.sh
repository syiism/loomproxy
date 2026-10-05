#!/usr/bin/env bash
# ticker 必须 Stop：`time.NewTicker(...)` 所在的那个函数里必须出现 `.Stop()`。
#
# 判据来源：这条形状在本仓被处理过两次——P80 给号池巡检协程补上 `defer t.Stop()`
# （当时的问题是"每轮新建一个 ticker 而旧的没人停"），第三十遍巡检又发现 `app.prewarmCache`
# 是全仓 8 个 ticker 里**唯一**一个不 Stop 的。第三次就不该再靠人想起来。
#
# 为什么还管 `time.Tick`：它按设计就停不掉（返回的是只读 channel 而不是 *Ticker），
# 在本仓唯一合理用法是"进程级一次性协程"，而那正好是 NewTicker + defer Stop 能表达的，
# 所以出现即红——把"这里到底要不要停"变成写的时候就决定的事。
#
# 规则（对非用例的 .go，按顶层 `func` 切块后逐块判）：
#   1. 块里有 `time.NewTicker(` 而没有 `.Stop()` → 红，报创建那一行；
#   2. 块里出现 `time.Tick(` → 红；
#   3. 没有豁免清单：本仓 8 个 ticker（`git ls-files` 数出来的，不是印象）现在全都 Stop 了，
#      留豁免位就是给下一次漏掉留门。
set -euo pipefail

cd "${PRECHECK_SCAN_ROOT:-$(dirname "$0")/..}"  # 预检携带分支时由 precheck-branch.sh 指到分支树，默认仍是本仓根

python3 - <<'PY'
import re, subprocess, sys

files = [f for f in subprocess.run(['git', 'ls-files', '*.go'], capture_output=True, text=True).stdout.split()
         if not f.endswith('_test.go') and not f.startswith(('test/', 'tools/', 'web/'))]

def blocks(lines):
    """按顶层 func 切块；块外的包级声明（var/const）不参与判定。"""
    cur = None
    for idx, line in enumerate(lines, 1):
        if line.startswith('func '):
            if cur: yield cur
            cur = {'head': line, 'rows': []}
        if cur is not None:
            cur['rows'].append((idx, line))
    if cur: yield cur

bad = 0
for path in files:
    lines = open(path, encoding='utf-8').read().split('\n')
    for b in blocks(lines):
        body = '\n'.join(l for _, l in b['rows'])
        for i, l in b['rows']:
            s = l.strip()
            if s.startswith('//'):
                continue
            if 'time.Tick(' in l:
                print(f"{path}:{i}: time.Tick 按设计停不掉——改用 NewTicker + defer Stop", file=sys.stderr)
                bad = 1
        if 'time.NewTicker(' in body and '.Stop()' not in body:
            for i, l in b['rows']:
                if 'time.NewTicker(' in l and not l.strip().startswith('//'):
                    print(f"{path}:{i}: 本函数里的 NewTicker 没有 Stop"
                          f"（判据：P80 那次的形状；本轮数过全仓 8 个 ticker，只有这个没 Stop）", file=sys.stderr)
                    bad = 1
if bad:
    print("check-ticker-stop: 见上——ticker 不停就是让 runtime 替我们记着它", file=sys.stderr)
    sys.exit(1)
print("check-ticker-stop OK")
PY
