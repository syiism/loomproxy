#!/usr/bin/env bash
# 前端自定义类的机器拦截：用了没定义的类，Tailwind 静默跳过、构建不报错、用例跑不到，
# 只有肉眼能看出来——真实例子：`btn-secondary` 被写了 7 处而它根本不存在（按钮一直没底色，
# 桌面端糊了好几天没人报）；同一次巡检又顺出 `label`（17 处）与 `checkbox`（5 处）同样没定义。
# 判据来自 docs/规范/待办清单.md 的 P31 与 docs/规范/踩坑判据.md 的升级阶梯：
# **能机器拦的约定别停在文档里**（与 scripts/check-sql-reserved.sh 同形态、同一个挂载点）。
#
# 口径（是刻意划窄的，别当成全量样式校验）：
#   - 只盯「本仓自定义类」这一族：CURATED 白名单前缀之外的类名一律视为 Tailwind 工具类，跳过。
#     工具类有上千个、又允许任意组合，判它们不存在需要完整 Tailwind 词汇表，收益不抵成本。
#   - 一个类算「已定义」的两个来源：`web/src/styles.css`（全局 `@layer components`），
#     或**同一个 .vue 自己的 `<style>` 块**（scoped 定义不出这个文件，这正是 `label` 的成因）。
#   - `:class` 里的三元/模板串只看引号内的字面量段，动态拼出来的类名判不到（写错也只能人工发现）。
#
# 用法：make vet 会自动跑；单独跑 ./scripts/check-css-classes.sh

set -euo pipefail
cd "$(dirname "$0")/.."

STYLES='web/src/styles.css'
[ -f "$STYLES" ] || { echo "找不到 $STYLES（前端目录挪了？同步本脚本）"; exit 1; }

python3 - "$STYLES" <<'PYEOF'
import glob, os, re, sys

styles_path = sys.argv[1]
custom_prefixes = ('btn-',)
# `custom_exact` 是**手抄**的，但它不是 styles.css 那份定义的复印件，别把它"对齐"回去：
# 名单的语义是"这些名字长得像本仓组件类，用了就必须查得到定义"，所以
# **已经消失的类要留在名单里**（`table-head` 现在哪儿都没有定义、也没人用——
# 有人重新写出 `class="table-head"` 时，正是这条名单把它拦下来）。
# 今天试过改成"从 `web/dist` 的生成 CSS 推导"，结论是**不做**：
# 用一个正则去解 CSS 选择器会同时吃到 `.reveal.in`、`.hover\:opacity-70:hover`、
# 以及注释里 `example.org` 这类东西，15 条"找不到"复核后 15 条全是扫描器自己的错
# （判据见踩坑判据《用正则扫「配置项漂移」，扫出来四条假开关》那一条的第 ④ ⑤ 两种机制）。
custom_exact = {
    'btn', 'card', 'card-hover', 'table-wrap', 'table-base', 'table-head',
    'tag', 'kbd', 'input', 'input-sm', 'label', 'checkbox', 'reveal', 'pg-btn',
}


def is_custom(tok):
    return tok.startswith(custom_prefixes) or tok in custom_exact


def defs_in(text):
    # 选择器里的 .foo：忽略伪类与组合（只取类名本身）
    return set(re.findall(r'\.([a-zA-Z][a-zA-Z0-9_-]*)', text))


global_defs = defs_in(open(styles_path).read())

# 待检查的文件：组件与页面（class 属性出现在这些里）
files = []
for root, dirs, names in os.walk('web/src'):
    dirs[:] = [d for d in dirs if d not in ('node_modules', 'dist')]
    for n in names:
        if n.endswith(('.vue', '.js')):
            files.append(os.path.join(root, n))

bad = []
for f in sorted(set(files)):
    text = open(f).read()
    own = global_defs.copy()
    for block in re.findall(r'<style[^>]*>(.*?)</style>', text, flags=re.S):
        own |= defs_in(block)
    for m in re.finditer(r'(?:^|[\s:>])class="([^"]*)"', text):
        for tok in re.split(r'[\s\'"`]+', m.group(1)):
            if tok and is_custom(tok) and tok not in own:
                bad.append((f, tok))
    # :class="..." 里的字面量段
    for m in re.finditer(r':class="([^"]*)"', text):
        for lit in re.findall(r"'([^']*)'", m.group(1)):
            for tok in lit.split():
                if tok and is_custom(tok) and tok not in own:
                    bad.append((f, tok))

if bad:
    for f, tok in bad:
        print("%s: 用了自定义类 `%s`，但它既不在 styles.css、也不在本文件的 <style> 里"
              " —— Tailwind 对不存在的类静默跳过，样式等于没写（待办清单 P31）" % (f, tok))
    print('前端类名检查未通过')
    sys.exit(1)
PYEOF
