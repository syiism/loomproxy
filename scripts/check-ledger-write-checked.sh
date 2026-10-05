#!/usr/bin/env bash
# 判定表与审计表的写入必须读 .Error（AGENTS.md §10「审计类写入不许吞错」的机器化）。
#
# 为什么扫这一条：这类写入的失败**不影响对外响应**——请求已经放行、监控记的是成功，
# 唯一的症状是"读数偏小 / 表只涨不清 / 用户以为改好了"，得从终点倒推才找得到写入那一步。
# 判据页里 `redemption_logs` 那条固化过一次（写入口逐条查 err），本分支的 quota_usage_logs
# 与 user_source_configs 是同形状的漏网：语句末尾没有 `.Error`，等于没人看过结果。
#
# 规则（对非用例的 .go 文件）：
#   1. 命中 `.(Create|FirstOrCreate|Save|Delete)(&?models.<受检表>{` 的语句，
#      其**收尾行**必须出现 `.Error`——GORM 里访问 `.Error` 就是"有人看了结果"的唯一硬证据；
#      写了 `.Error` 却丢给 `_` 也算看了形状，值不值得处理由人读。
#   2. 任何 `Updates(...)` / `UpdateColumn(s)(...)` 同判——它写的是变量而不是字面量，
#      规则 1 的正则看不见（本轮撞出的 `verification_codes.attempts` 就是这个形状）。
#   3. 整条语句把结果赋给变量（`result := db.DB...`）视作"有人拿着结果"，不复验它后来读没读：
#      `if result.Error != nil` 分两行是本仓的合法写法，复验会把合规代码报成红。
#   4. 受检表清单里每一个名字都必须在 models/ 下命中定义，否则报错退出——
#      清单写了不存在的名字，扫描器就对着一个不存在的形状空转（绿灯毫无意义）。
set -euo pipefail

cd "${PRECHECK_SCAN_ROOT:-$(dirname "$0")/..}"  # 预检携带分支时由 precheck-branch.sh 指到分支树，默认仍是本仓根

WATCHED=(QuotaUsageLog RedemptionLog UserSourceConfig VerificationCode ApiCallLog AuthSession PlatformSourceConfig UserQuotaOverride)

# 第 3 条：清单不能指向不存在的结构体
for name in "${WATCHED[@]}"; do
  if ! grep -rqE "^type $name struct" models/; then
    echo "check-ledger-write: 受检表清单里的 $name 在 models/ 下没有结构体定义——扫描器会对着一个不存在的名字空转" >&2
    exit 1
  fi
done

# awk 的动态正则是从变量来的，而 -v 赋值会吃掉一层反斜杠转义——所以这里一律用字符组表示法
# （`[.]` `[(]` `[{]`），不写任何反斜杠，免得正则本身在 awk 里变形。
names=$(
  IFS='|'; echo "${WATCHED[*]}"
)
pattern="[.](Create|FirstOrCreate|Save|Delete)[(][&]?models[.](${names})[{]"
# 第二条规则管 `Updates(...)` / `UpdateColumn(...)`：它的目标是变量（`Model(&row)`）而不是字面量，
# 上面那条正则看不见，而本轮撞出的第三处静默恰好就是这个形状（`verification_codes.attempts`
# 那个防爆破计数器）。这里不区分表名——按列名批量更新本身就是"写库"，没有例外可给。
pattern2="[.](Updates|UpdateColumns|UpdateColumn)[(]"

fail=0
# 排除用例与 test/（用例里的写法由断言自己负责）与 tools/（一次性迁移脚本）
files=$(git ls-files '*.go' | grep -vE '(_test\.go|^test/|^tools/)')

while IFS= read -r file; do
  # awk：从命中行往后累积到括号配平，收尾行没有 .Error 就是吞了结果
  hits=$(awk -v pat="$pattern" -v pat2="$pattern2" -v f="$file" '
    function strip(s) {            # 去掉字符串字面量与行尾注释，别让里面的括号参与配平
      gsub(/`[^`]*`/, "", s)
      gsub(/"[^"]*"/, "Q", s)
      sub(/\/\/.*$/, "", s)
      return s
    }
    # 收尾行没有 .Error，但整条语句把结果**赋给了变量**（`result := db.DB...`），
    # 视作"有人拿着结果"——它后来读没读是另一条判据的活，本扫描不复验，免得把
    # `if result.Error != nil` 这种分两行的合法写法报成红。
    function holds(stmt) { return stmt ~ /:=[[:space:]]*(db[.]DB|DB|tx)[.]/ || stmt ~ /(^|[[:space:]])[a-zA-Z_]+[[:space:]]*=[[:space:]]*(db[.]DB|DB|tx)[.]/ }
    {
      if (depth > 0) {             # 正在收语句的尾巴
        line = strip($0)
        stmt = stmt " " line
        for (i = 1; i <= length(line); i++) {
          ch = substr(line, i, 1)
          if (ch == "(") depth++
          else if (ch == ")") { depth--; if (depth == 0) break }
        }
        if (depth == 0) {
          if ($0 !~ /\.Error/ && !holds(stmt)) printf "%d\t%s\n", start, why
          buf = ""
        }
        next
      }
      l = strip($0)
      why = ""
      if (l ~ pat) why = "受检表的写入没读结果"
      else if (l ~ pat2) why = "按列批量更新没读结果"
      if (why == "") next
      stmt = l
      start = NR
      depth = 0
      for (i = 1; i <= length(l); i++) {
        ch = substr(l, i, 1)
        if (ch == "(") depth++
        else if (ch == ")") depth--
      }
      if (depth <= 0) {            # 单行语句
        if ($0 !~ /\.Error/ && !holds(stmt)) printf "%d\t%s\n", NR, why
        depth = 0
      }
    }
  ' "$file")
  if [ -n "$hits" ]; then
    while IFS=$'\t' read -r ln why; do
      echo "$file:$ln: $why——写库的结果必须读 `.Error`（判据：AGENTS.md §10 审计/判定写入不许吞错）" >&2
      fail=1
    done <<< "$hits"
  fi
done <<< "$files"

if [ "$fail" -ne 0 ]; then
  echo "check-ledger-write: 见上——这类失败不会反映在响应码上，日志是唯一说得出来的地方" >&2
  exit 1
fi
echo "check-ledger-write OK"
