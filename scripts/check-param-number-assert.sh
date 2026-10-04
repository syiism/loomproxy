#!/usr/bin/env bash
# 请求参数的值一律是 string——所以对**用户可控参数**做数字类型断言是静默死代码。
#
# 判据来源是携带形态里真发生过的一次：sources/fq/form/handlers.go 的 toPage 早先写 p.(float64)，
# 而 params 的值由 app.buildParams 一律以 string 注入（c.Query 本来就是字符串），
# 于是那个断言**恒不成立**：不报错、不日志，只是 page 永远回到 1，
# 症状是「分类第三页没有内容」而代码看起来完全正常。map 是 map[string]interface{}，编译器也不帮忙。
#
# 三种 map 的数字口径各不同，取之前要问清手里是哪一种：
#   · 请求参数（params["key"] / getParam("key")）——只有 string，要数字就 strconv.*；
#   · 上游响应（各源自己 json.Unmarshal 出来的）——默认是 float64，除非那一段自己开了 UseNumber；
#   · 平台出口（base/legado.normalizeEnvelope）——刻意 UseNumber()，所以是 json.Number
#     （走 float64 会把 1675148 这样的字数在回写时变成 1.675148e+06）。
#
# 这条扫描管两件事：
#   ① 不许对用户可控的参数值做数字类型断言。键必须是双引号字面量且**不以 _ 开头**——
#      以 _ 开头的是平台注入的键（__uid 是 uint、_datafile_parts 是 []string，本来就不是 string 口径），
#      它们由 buildParams 的保留前缀守卫保证客户端占不进去。
#   ② 不许声明以 _ 开头的参数名：那是平台保留前缀。源若声明同名参数，客户端 ?__uid=7 就能占住这个键——
#      平台只在"解析出了用户 id 时"覆盖它、没解析出来时**不会删除**，
#      于是将来任何按 string 读它的代码都会读到伪造值。
set -euo pipefail

cd "$(dirname "$0")/.."

NUMERIC='(float64|float32|int|int8|int16|int32|int64|uint|uint64|json\.Number)'

hits=$(grep -rnE \
  "(params|Params)\[\"[^_][^\"]*\"\][[:space:]]*:?[[:space:]]*\.?\((${NUMERIC})\)|getParam\(\"[^_][^\"]*\"\)[[:space:]]*:?[[:space:]]*\.?\((${NUMERIC})\)" \
  --include='*.go' --exclude='*_test.go' . 2>/dev/null || true)

decl=$(grep -rnE '(QueryParams|RequiredParams)[^)]*"_[A-Za-z]' --include='*.go' --exclude='*_test.go' . 2>/dev/null || true)

fail=0
if [ -n "$hits" ]; then
  fail=1
  echo "对请求参数做了数字类型断言（用户可控的 params 值一律是 string，这个断言恒不成立、会静默走默认值）:"
  echo "$hits" | sed 's/^/  /'
  echo "改法：取字符串再用 strconv 转——strconv.Atoi(getParam(params, \"page\"))。"
fi
if [ -n "$decl" ]; then
  fail=1
  echo "声明了以 _ 开头的参数名——那是平台保留前缀（__uid、_datafile_parts 由平台注入）:"
  echo "$decl" | sed 's/^/  /'
  echo "改法：换个名字。声明位不该占用平台注入的键，否则客户端能往那个键里塞字符串。"
fi
if [ "$fail" != 0 ]; then
  echo '已知盲区（诚实写明）：先把值拷进另一个变量再断言（page := params["page"]; page.(float64)）扫不到；'
  echo '用变量作键（params[k]）也不在范围内。这条是绊线不是证明，判据在 docs/规范/踩坑判据.md。'
  exit 1
fi
