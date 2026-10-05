#!/usr/bin/env bash
# 平台时区与日界只有一处定义（待办清单 P71）。
#
# 真实症状：`TZ_OFFSET_HOURS` 是配置项、`utils` 读它，而「今天零点」这个算法在骨架里有七种写法——
# 四处写死 `FixedZone("CST", 8*3600)`（额度日界、验证码日限、距下次重置、统计面板今日新增），
# 三处跟着进程时区 `time.Local`（榜单窗口、趋势图日界、名称回填窗口）。
# 改配置只有格式化那一侧跟着动；把系统时区换成 UTC，「今日调用数」与「今日额度」就不是同一天。
# 现网（Asia/Shanghai + 配置 8）三种口径恰好重合，所以这条**不会自己报错**。
#
# 判据（写法层面）：算日界与取时区一律走 `utils.DayStart` / `utils.PlatformZone`；
# 出现下面三种形状即构建失败——
#   1. `FixedZone(`          —— 又一个自带偏移的时区
#   2. `time.Local`          —— 跟着进程时区算日界（宿主机是什么就是什么）
#   3. `utils.TZShanghai(`   —— 旧别名不许在骨架里新增调用方（携带形态的 sources/** 不在扫描范围，
#                              它那处调用点由分支侧待办 S51 迁移；调用点清零后这个别名就删掉）
set -euo pipefail

cd "${PRECHECK_SCAN_ROOT:-$(dirname "$0")/..}"  # 预检携带分支时由 precheck-branch.sh 指到分支树，默认仍是本仓根

# 只扫 .go，排除测试（测试里为了验证口径会故意造时区）、前端产物、以及携带形态的源目录
files=$(find . -name '*.go' -not -name '*_test.go' -not -path './web/*' -not -path './sources/*' | sort)

hits=$(grep -n -E 'FixedZone\(|time\.Local|utils\.TZShanghai\(' $files \
  | grep -v '^./utils/norm.go:' || true)

if [ -n "$hits" ]; then
  echo "平台时区/日界出现了第二处写法（应走 utils.DayStart 或 utils.PlatformZone）:"
  echo "$hits" | sed 's/^/  /'
  echo "确属例外就在本脚本里豁免并写明理由——豁免要可审计，默认是红。"
  exit 1
fi
