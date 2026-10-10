#!/bin/bash
# 待办清单 P109 的固化：拦「把承担通过与否读数的命令接在管道中间」。
# `make build 2>&1 | tail -25` 报的是 tail 的 0 —— 红门禁会被读成绿门禁，而假绿那次恰好是最不会回头再看输出的一次。
# 这个脚本是给会话的 PreToolUse 钩子用的（不是 make vet 检查项：它判的是一条命令行，不是仓库内容）。
#   钩子用法：stdin 喂事件 JSON，脚本读 .tool_input.command
#   自查用法：scripts/check-gate-run-shape.sh --cmd 'make build 2>&1 | tail -25'
# 退出码：0 放行；2 拦下（理由走 stderr，stdout 必须保持干净——钩子按 JSON 解析它）。
set -u

CMD=""
MODE="hook"

case "${1:-}" in
  --cmd)
    MODE="cmd"
    CMD="${2:-}"
    ;;
esac

if [ "$MODE" = "hook" ]; then
  INPUT=$(cat)
  if command -v jq >/dev/null 2>&1; then
    CMD=$(printf '%s' "$INPUT" | jq -r '.tool_input.command // empty' 2>/dev/null)
  elif command -v python3 >/dev/null 2>&1; then
    CMD=$(printf '%s' "$INPUT" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("tool_input",{}).get("command",""))' 2>/dev/null)
  else
    # 解析器都没有时放行：钩子挂在每一条 Bash 前面，为一个判读把会话卡死比漏判更糟
    echo "gate-run-shape: 无 jq/python3，本次放行" >&2
    exit 0
  fi
fi

[ -n "$CMD" ] || exit 0

# 已经自带退出码保护的写法不算违例：外层管道开了 pipefail，或直接读 PIPESTATUS，或按固定跑法落日志后再判
case "$CMD" in
  *pipefail*|*PIPESTATUS*) exit 0 ;;
esac

# 语义：管道比 && 和 ; 结合得紧，所以 `cd x && make build | tail` 里 tail 吃掉的正是 make 的退出码。
# 于是先按 && / ; / 换行切成一条条命令，再在每条命令里按 | 切段；
# 若 make 出现在**不是最后一段**的那一段，这条命令的回执就不再是 make 的（误报形状也写清楚：
# `grep "make build" | wc -l` 的段首不是 make，`make build > /tmp/g.log; tail /tmp/g.log | wc -l` 的 make 是它那条命令的最后一段）。
BLOCK=""
FLAT=$(printf '%s' "$CMD" | tr '\n' ';' | sed -e 's/&&/;/g' -e 's/||/;/g')
IFS=';' read -r -d '' -a CMDS <<< "$FLAT" || true
for one in "${CMDS[@]}"; do
  IFS='|' read -r -d '' -a SEGS <<< "$one" || true
  n=${#SEGS[@]}
  [ "$n" -gt 1 ] || continue
  for ((i = 0; i < n - 1; i++)); do
    trimmed=$(printf '%s' "${SEGS[$i]}" | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]\n]*$//')
    case "$trimmed" in
      make|make[[:space:]]*) BLOCK=1 ;;
    esac
  done
  [ "$BLOCK" = "1" ] && break
done

[ "$BLOCK" = "1" ] || exit 0

echo "这条命令的退出码读的是管道末端、不是 make（待办清单 P109）。唯一跑法：" >&2
echo "  make build > /tmp/gate.log 2>&1; echo \"BUILD_EXIT=\$?\" >> /tmp/gate.log" >&2
echo "要确实需要管道，就外层加 pipefail（bash -c 'set -o pipefail; ...'）或读 \${PIPESTATUS[0]}。" >&2
exit 2
