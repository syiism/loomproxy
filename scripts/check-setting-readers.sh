#!/usr/bin/env bash
# 设置项的两条静默面，都在构建里拦：
#   一、seed 铺出来的每一个键，除了"面板能编辑"之外还得**有人真的听它**；
#   二、number 型键的读取只许走 `db.SettingInt` 一处，不得再有本地实现。
#
# 第一条的判据来源是待办清单 P53 那一轮巡检：27 个键里 `maintenance_mode` 与 `site_name` 的读取方是 **0**，
# 而 `default_quota_plan` 唯一的出现处是「不许删」名单本身——三条都在面板上渲染成可编辑控件，
# 于是"管理员填了值、系统什么都没发生"这件事在界面上长得跟正常配置一模一样。
# 这类错不会自己报错（构建绿、用例绿、面板绿），所以拦在构建里。
#
# 第二条的判据来源是 P48 → P60 → P61 这三跳：同一个「上限类设置填了不生效的值」的形状，
# 先后以三种不同写法躺在三个包里（一份出声、一份手写逐字符解析还会溢出、一份 `Atoi` 配 `, _`），
# 每修一处都有另外两处没人知道。**被踩到第三次就不要再靠约定第四次**（待办清单 P61）。
# 模式刻意只覆盖已知的两种写法（`Atoi(db.GetSetting(` 与本地 `settingInt` 函数），
# 不做"任何 GetSetting 之后必须跟 SettingInt"那种宽判据——猜出来的规则比没规则更贵。
#
# 计数方式刻意粗糙：只在后端 .go 里找字符串 "键名"，排除测试、排除 seed 自己的声明、
# 排除 handlers/admin/settings.go（那份表说的是"能不能删/能不能编辑"，不是"有没有人读"）。
# 确实只打算给面板或脚本用（没有任何后端行为读它）的键，写进下面的 EXEMPT 并留一句为什么——
# 豁免是可选的、可审计的，而**默认是红**。
set -euo pipefail

cd "$(dirname "$0")/.."

# —— 第二条：整数设置的读取口只有一处 ——
if int_hits=$(grep -rnE 'Atoi\(db\.GetSetting\(|func settingInt\(' --include='*.go' . \
  | grep -v '_test\.go' | grep -v '^\./db/setting\.go:'); then
  echo "number 型设置项出现了本地读取实现（同一个填错的值在两个包会有两种静默行为）："
  printf '%s\n' "$int_hits" | sed 's/^/  /'
  echo "请改用 db.SettingInt(key, 默认值)：它把空值/非整数/非正数这三种「填了没生效」都出了声。"
  exit 1
fi

# 只读展示 / 由外部脚本消费的键：键名|理由
EXEMPT="
maintenance_mode|待办清单 P53：全站维护闸门的语义未定（谁豁免、返回什么码），先留在面板但显式豁免
site_name|待办清单 P53：面板能改而没有任何地方显示它；显示位置待定
"

is_exempt() {
  printf '%s\n' "$EXEMPT" | grep -q "^$1|"
}

offenders=()
while read -r key; do
  [ -n "$key" ] || continue
  is_exempt "$key" && continue
  hits=$(grep -rl --include='*.go' --exclude-dir=test "\"$key\"" . \
    | grep -v -e '^./db/seed.go$' -e '^./handlers/admin/settings.go$' || true)
  if [ -z "$hits" ]; then
    offenders+=("$key")
  fi
done < <(grep -oE 'Key: "[a-z_0-9]+"' db/seed.go | sed -e 's/Key: "//' -e 's/"//' | sort -u)

if [ ${#offenders[@]} -gt 0 ]; then
  echo "设置项没有后端读取方（面板上能编辑，但没人听）："
  for k in "${offenders[@]}"; do
    echo "  - $k"
  done
  echo "要么把它接上，要么从 seed 与面板里摘掉；确实只给展示/脚本用就写进 scripts/check-setting-readers.sh 的 EXEMPT 并留理由（见待办清单 P53）。"
  exit 1
fi
