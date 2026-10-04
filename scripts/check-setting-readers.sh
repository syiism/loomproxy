#!/usr/bin/env bash
# 设置项必须有后端读取方：seed 铺出来的每一个键，除了"面板能编辑"之外还得有人真的听它。
#
# 判据来源是待办清单 P53 那一轮巡检：27 个键里 `maintenance_mode` 与 `site_name` 的读取方是 **0**，
# 而 `default_quota_plan` 唯一的出现处是「不许删」名单本身——三条都在面板上渲染成可编辑控件，
# 于是"管理员填了值、系统什么都没发生"这件事在界面上长得跟正常配置一模一样。
# 这类错不会自己报错（构建绿、用例绿、面板绿），所以拦在构建里。
#
# 计数方式刻意粗糙：只在后端 .go 里找字符串 "键名"，排除测试、排除 seed 自己的声明、
# 排除 handlers/admin/settings.go（那份表说的是"能不能删/能不能编辑"，不是"有没有人读"）。
# 确实只打算给面板或脚本用（没有任何后端行为读它）的键，写进下面的 EXEMPT 并留一句为什么——
# 豁免是可选的、可审计的，而**默认是红**。
set -euo pipefail

cd "$(dirname "$0")/.."

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
