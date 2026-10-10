#!/usr/bin/env bash
# MISSION: `ConfMgr` 里声明的每一个字段，都必须有人把它装配上（待办清单 P124）。
#
# 这条是 `settings-readers-check` 的**镜像**：那一头管"设置键必须有后端读取方"（面板能改、后端没人读），
# 这一头管"配置字段必须有写入方"——反方向同样静默：
#   真实事故：P118 ③ 加了 `RankCacheSec` 并在 `handlers/rank` 读它，
#   但 `conf.Load()` 的装配块里**没有这一行**，于是 Go 给零值 0，而 0 的业务语义就是"每次真算"。
#   结果是那颗公开榜缓存**装了等于没装**：`.env.example` 写着 `RANK_CACHE_SEC=60`、
#   AGENTS §9 写着"默认 60 秒"、四条榜用例全绿——因为它们都直接改 `conf.Config.RankCacheSec` 再跑，
#   那条通路测的是"值怎么用"，永远不经过装配块。
#   同族的旧判据那句「读的是测试脚手架的零值，不是代码的默认」在这里第二次成立。
#
# 判据形状（刻意划窄，别扩成"配置风格检查"）：
#   - 只看 `conf/conf.go` 的 `type ConfMgr struct` 的**字段名**；
#   - 写入方有两种认法：`Config = &ConfMgr{...}` 字面量里的 `字段名:`，或本文件里 `Config.字段名 =` 的后置赋值；
#   - 两种都没有才算漏。**不判"该不该有 env 键"**：派生值与后置赋值是合法形态，判了就成一堆假警报。
#   - 上线那次实测：56 个字段，装配块覆盖 54、后置赋值覆盖 2，**漏 0 个**——
#     一条零假警报的检查才有价值（门禁台账那句：会喊一百条的红检查教人的是"无视输出"）。
#
# USAGE: make vet 自动跑；单独跑 ./scripts/check-config-wired.sh
# GUARDRAILS: 只读；找不到结构体就**出声并非零退出**（那说明判法的前提没了，不是"通过"）。
set -euo pipefail

cd "${PRECHECK_SCAN_ROOT:-$(dirname "$0")/..}"

python3 - <<'PY'
import re, sys

path = 'conf/conf.go'
src = open(path, encoding='utf-8').read()

m = re.search(r'type ConfMgr struct \{(.*?)\n\}', src, re.S)
if not m:
    print('%s: 找不到 `type ConfMgr struct` —— 判法的前提没了，本检查无从下手（改名要同时改这里）。' % path)
    sys.exit(1)

fields = re.findall(r'^\t([A-Z]\w*)\s+\S', m.group(1), re.M)
if not fields:
    print('%s: ConfMgr 结构体里一个字段都没数到——多半是缩进形状变了，不是真的没有字段。' % path)
    sys.exit(1)

lit = re.search(r'Config = &ConfMgr\{(.*?)\n\t\}', src, re.S)
assigned = set(re.findall(r'^\t\t([A-Z]\w*):', lit.group(1), re.M)) if lit else set()
post = set(re.findall(r'Config\.([A-Z]\w*)\s*=', src))

missing = [f for f in fields if f not in assigned and f not in post]
if missing:
    print('%s: `ConfMgr` 有 %d 个字段**声明了却没人装配**（Go 给零值，而零值在配置里几乎总有业务含义）：' % (path, len(missing)))
    for f in missing:
        print('  - %s' % f)
    print('修法：在 `conf.Load()` 的 `Config = &ConfMgr{` 里加 `%s: envXxx("<键名>", 默认),`（待办清单 P124）。' % missing[0])
    print('    只加字段不加装配，症状是"读的人拿到零值、写 .env 的人以为改了有用"——两头都不报错。')
    sys.exit(1)

print('配置装配检查通过（ConfMgr 字段 %d 个：装配块 %d、后置赋值 %d，漏 0）'
      % (len(fields), len([f for f in fields if f in assigned]), len([f for f in fields if f not in assigned])))
PY
