#!/usr/bin/env bash
# 集成用例的「第一件事是装配测试服务器」检查（待办清单 P39 顺带暴露的坑）。
#
# 真实症状：一条用例在 `newTestServer(t)` **之前**读 `conf.Config.BillingDedupeSec`，
# 单跑它（`go test -run 那一条`）直接 nil panic，而整包跑着过——因为上一个用例已经把全局指针建好了。
# 这类用例的绿是**借来的**：它依赖别的用例先跑过，而 Go 的用例顺序不属于任何保证
# （换文件、加 `-run`、以后引入 t.Parallel 都会变）。全局态跨用例共享这条我们写在开发约定里，
# 但「共享」不等于「可以假设别人已经初始化过」。
#
# 口径（刻意划窄，别当成用例风格检查）：
#   - 只看 `func TestXxx(t *testing.T)` 的**函数体内部**；
#   - 只盯三个跨用例共享的全局：`conf.Config`、`db.DB`、`utils.DefaultCache()`；
#   - 函数体里没有任何装配动作（`newTestServer(` / `CreateApp(` / `testkit.`）的纯函数测试不管；
#   - 只报每条用例**第一处**越前引用，一行一处，方便定位。
#
# 用法：make vet 会自动跑；单独跑 ./scripts/check-test-order.sh

set -euo pipefail
cd "$(dirname "$0")/.."

python3 - <<'PYEOF'
import glob, re, sys

GLOBAL_RE = re.compile(r'\bconf\.Config\b|\bdb\.DB\b|utils\.DefaultCache\(\)')
SETUP_RE = re.compile(r'newTestServer\(|app\.CreateApp\(|testkit\.')
FUNC_RE = re.compile(r'^func (Test\w+)\(t \*testing\.T[^{]*\{(.*?)^\}', re.M | re.S)

bad = []
for f in sorted(glob.glob('test/*_test.go')):
    src = open(f).read()
    for m in FUNC_RE.finditer(src):
        name, body = m.group(1), m.group(2)
        lines = body.split('\n')
        # 注释行不参与判定：解释「为什么要先装配」的注释里必然提到 conf.Config / db.DB，
        # 把它算成违规就是假警报——而假警报的代价是下一个人开始无视这条检查。
        # （第一版就是这么红的：新用例里那句「装配优先」的注释被当成了引用。）
        code = [(i, l) for i, l in enumerate(lines) if l.strip() and not l.strip().startswith('//')]
        setup_at = next((i for i, l in code if SETUP_RE.search(l)), None)
        if setup_at is None:
            continue  # 不装配测试服务器的纯函数测试，没有全局态可依赖
        hit = next((l for i, l in code if i < setup_at and GLOBAL_RE.search(l)), None)
        if hit is not None:
            bad.append((f, name, lines.index(hit) + 1, hit.strip()))

if bad:
    for f, name, ln, text in bad:
        print("%s: %s 在装配测试服务器之前用了 `%s`（第 %d 行）——"
              "单跑这条会 nil panic 或读到上一个用例的余荫，整包绿不算证据；"
              "把 newTestServer(t) 挪到函数第一句，需要读全局态的准备工作放在它之后（待办清单 P39）"
              % (f, name, text[:70], ln))
    print('用例装配顺序检查未通过')
    sys.exit(1)
PYEOF
