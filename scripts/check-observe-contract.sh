#!/usr/bin/env bash
# 观测契约对齐检（待办清单 P127①，2026-10-11 拍定落地）：
#   models.ApiCallLog 的每个 json 列、base.CallSubject 的每个字段，
#   都必须在契约页 docs/架构/观测契约-信封与明细字段.md 里有名字。
# 「信封与明细字段增删必须同批改这一页」的机器半边——声明位是唯一口子，
# 改实现不改文档当场红（与 config-wired-check 管「配置字段没人装配」同一族）。
# 「源到底填没填值」机器检不了：那半靠用例与样本守卫，契约页的「守卫」列是它的账。
set -euo pipefail

cd "$(dirname "$0")/.."

PAGE=docs/架构/观测契约-信封与明细字段.md
if [ ! -f "$PAGE" ]; then
  echo "观测契约页不存在：$PAGE（P127① 的正本页，字段增删的登记处）"
  exit 1
fi

missing=0

# 明细行的 json 列：从 ApiCallLog 结构体里抽 json tag
while IFS= read -r tag; do
  [ -n "$tag" ] || continue
  if ! grep -qw "$tag" "$PAGE"; then
    echo "  - ApiCallLog 列 \`$tag\` 不在契约页里（加字段要同批改 $PAGE）"
    missing=$((missing + 1))
  fi
done < <(sed -n '/type ApiCallLog struct/,/^}/p' models/api_call_log.go \
         | grep -o 'json:"[a-z_]*"' | sed 's/json:"//;s/"$//')

# 内存态字段：CallSubject 的 Go 字段名
while IFS= read -r field; do
  [ -n "$field" ] || continue
  if ! grep -qw "$field" "$PAGE"; then
    echo "  - CallSubject 字段 \`$field\` 不在契约页里（加字段要同批改 $PAGE）"
    missing=$((missing + 1))
  fi
done < <(sed -n '/type CallSubject struct/,/^}/p' base/subject.go \
         | grep -oE '^[[:space:]]*[A-Z][A-Za-z]+' | tr -d '[:space:]')

if [ "$missing" -gt 0 ]; then
  echo "观测契约对齐失败：$missing 个字段没有登记进契约页——改字段与改页要同一颗提交（P127①）"
  exit 1
fi
echo "观测契约对齐通过：ApiCallLog 与 CallSubject 的全部字段都在契约页里"
