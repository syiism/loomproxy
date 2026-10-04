#!/usr/bin/env bash
# 手写 SQL 的别名保护：把「别名必须避开 MySQL 保留字」这条约定变成机器拦截。
#
# 为什么要有这个脚本：SQLite 容忍 `AS keys` 这类写法，测试全绿；只有生产（MySQL）报 1064。
# 约定写在 docs/规范/开发约定.md 里好几年，仍然有人踩——文档记了 ≠ 防住了。
# 判据与退出机制见 docs/规范/踩坑判据.md《退出机制》。
#
# 保留字表不是抄文档，是**实测**出来的：在本地 MySQL 8.0.43 上对每个候选词跑
#   SELECT 1 AS <w>          -- 表达式别名位
#   SELECT * FROM (SELECT 1 a) <w>   -- 派生表别名位
# 两个位置，取「至少一处报错」的词。所以 TEXT / DATE / TIME / TIMESTAMP 不在表里
# （它们是 unreserved keyword，实测能当别名），而 MAX / MIN / COUNT 也不在（函数名，可当别名）。
#
# 五条检查，都只在**含 SQL 关键字的行**上跑，避免误伤普通 Go 代码：
#   1) `AS <ident>` 命中保留字
#   2) `) <ident>`  派生表别名命中保留字
#   3) `<ident>`    反引号引用的标识符——反引号是 MySQL/SQLite 方言，PostgreSQL 只认双引号
#   4) `ESCAPE \'`  —— LIKE 的转义符写成反斜杠：MySQL/MariaDB 会在字符串字面量里把它当引号转义（1064），
#      PostgreSQL 标准模式留两个反斜杠（不是单字符），SQLite 明说「ESCAPE expression must be a single character」。
#      **不存在一种反斜杠写法三家同时成立**，所以只能换字符（本仓用 `likeESCAPE()` 统一生成 `ESCAPE \'!\'`）
#   5) Go 里的 `.Offset(` 没有配套的 `.Limit(` —— MySQL 要求 OFFSET 必须与 LIMIT 同现（裸 `OFFSET 4` 报 1064，sqlite 与 PostgreSQL 都接受）；本仓用例跑 sqlite，所以只能静态扫
#
# 用法：make vet 会自动跑；单独跑 ./scripts/check-sql-reserved.sh；
# 变异验证时可指定文件：./scripts/check-sql-reserved.sh /tmp/fixture.go [文件…]（不给参数即扫全仓 .go）

set -euo pipefail
cd "$(dirname "$0")/.."

RESERVED='accessible add all alter analyze and as asc asensitive before between bigint binary blob both by call cascade case char character check collate column condition constraint continue convert create cross cube cume_dist current_date current_time current_timestamp current_user cursor database databases day_hour day_microsecond day_minute day_second dec decimal declare default delayed delete dense_rank desc describe deterministic distinct distinctrow div double drop dual each else elseif empty enclosed escaped except exists exit explain false fetch first_value float float4 float8 for force foreign from fulltext function generated get grant group grouping groups having hour_microsecond hour_minute hour_second if ignore in infile inner inout insensitive insert int int1 int2 int3 int4 int8 integer interval into is iterate join key keys kill lag last_value lateral lead leading left like limit linear lines load localtime localtimestamp lock long longblob longtext loop low_priority match maxvalue mediumint mediumtext middleint minute_microsecond minute_second mod modifies natural not no_write_to_binlog nth_value ntile null numeric of on optimize option optionally or order out outer over partition percent_rank precision primary procedure purge range rank read reads real references regexp release rename repeat replace require resignal restrict return revoke right rlike row rows row_number schema schemas second_microsecond select sensitive separator show signal smallint spatial specific sql sqlexception sqlstate sqlwarning sql_big_result sql_small_result ssl starting stored straight_join system table terminated to trailing trigger true undo union unique unlock unsigned update usage use using utc_date utc_time utc_timestamp values varbinary varchar varcharacter varying virtual when where while window with write xor year_month zerofill'

SQL_MARK='(^|[^a-z0-9_])(select|from|where|join|left join|inner join|group by|order by|union|insert into|update|delete from|having|case when)([^a-z0-9_]|$)'

status=0
while IFS= read -r f; do
  if out=$(awk -v reserved="$RESERVED" -v mark="$SQL_MARK" '
    BEGIN {
      n = split(reserved, w, /[ \t]+/)
      for (i = 1; i <= n; i++) res[w[i]] = 1
      split("as and or not in is null then else end when from where having group order by limit offset union" \
            " all distinct on using join inner left right full cross asc desc between like regexp rlike" \
            " into values set case select exists any some true false next", s, /[ \t]+/)
      for (i in s) skipword[s[i]] = 1
      bt = sprintf("%c", 96)
      re_as  = "(^|[^a-z0-9_])as[ \t]+[a-z_][a-z0-9_]*"
      re_der = "\\)[ \t]+[a-z_][a-z0-9_]*"
      re_bt  = bt "[a-z_][a-z0-9_]*" bt
    }
    function hit(tok, rule,   id) {
      id = tolower(tok); sub(/^.*[ \t]/, "", id)
      if (rule != "派生表别名" || !(id in skipword)) {
        if (id in res) {
          printf "%s:%d: %s —— 别名 `%s` 在 MySQL 8.0 不可用作别名（SQLite 不报，只有生产报 1064），换个名\n", FILENAME, FNR, rule, id
          found = 1
        }
      }
    }
    {
      raw = $0
      if (raw ~ /dialect-allow/) next      # 明知故犯的对照组用例，见 test/dialect_quoting_test.go
      sub(/\/\/.*/, "", raw)              # 行注释不参与
      low = tolower(raw)
      # 5) 裸 OFFSET：MySQL 语法不接受（sqlite/PG 接受），且用例跑 sqlite 测不出来
      if (raw ~ /\.Offset\(/ && raw !~ /\.Limit\(/) {
        printf "%s:%d: OFFSET —— MySQL 要求 OFFSET 必须与 LIMIT 同现（sqlite/PG 接受裸 OFFSET，本仓用例测不出），改为取回后在 Go 里切片或显式 .Limit()\n", FILENAME, FNR
        found = 1
      }
      # 4) LIKE ESCAPE 写成反斜杠：跨方言必炸（MySQL 把 '\\' 当引号转义、SQLite 要求单个字符）。
      #    这条排在 mark 闸门**之前**——含 ESCAPE 的行往往没有 select/from 之类关键字，排在后面等于永不触发
      if (low ~ /escape[ \t]*.[\\]/) {
        printf "%s:%d: LIKE ESCAPE —— 转义符不能写成反斜杠（MySQL 1064 / SQLite 报「必须单个字符」，sqlite 用例测不出来），改用 likeESCAPE()\n", FILENAME, FNR
        found = 1
      }
      if (low !~ mark) next               # 不是手写 SQL 的行，一律放过
      t = low
      while (match(t, re_as)) { hit(substr(t, RSTART, RLENGTH), "AS 别名"); t = substr(t, RSTART + RLENGTH) }
      t = low
      while (match(t, re_der)) { hit(substr(t, RSTART, RLENGTH), "派生表别名"); t = substr(t, RSTART + RLENGTH) }
      if (match(low, re_bt)) {
        id = substr(low, RSTART + 1, RLENGTH - 2)
        printf "%s:%d: 反引号标识符 —— SQL 里写了 `%s`；反引号只认 MySQL/SQLite，PostgreSQL 会挂，条件里的列名交给 GORM 按方言加引号\n", FILENAME, FNR, id
        found = 1
      }
    }
    END { if (found) exit 1 }
  ' "$f"); then :; else status=1; echo "$out"; fi
done < <(if [ "$#" -gt 0 ]; then printf '%s\n' "$@"
  else git ls-files '*.go' | grep -v '^web/'
  fi)

if [ "$status" -ne 0 ]; then
  echo "SQL 别名检查未通过（判据：docs/规范/开发约定.md「手写 SQL 的别名必须避开 MySQL 保留字」）"
  exit 1
fi
