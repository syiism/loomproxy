#!/usr/bin/env bash
# 给一次慢下来的门禁留下现场证据：每 N 秒抄一行"谁在吃 CPU、机器在等什么"。
#
# 为什么需要这一支（待办清单 P126）：2026-10-10 那三次 `make build` 的测试段报 400s/409s，
# 而同一棵树串行跑是 240s、并发跑两包是 259s、故意让 dist 变一点再全量跑也是 256s。
# **代码、并发、`go:embed` 作废、Redis、内存、别的仓都排除之后，仍然没有任何一条读数说"是谁占的"**——
# 因为事后 `ps` 看不到当时。判据页那句"没有更宽的机器检查就只到 `部分`"在这里的具体形态就是：
# 慢这件事必须**在它发生的那一刻被抄下来**，否则下一轮只能重新猜一遍。
#
# 用法（与门禁并行开一个终端，或后台起）：
#   scripts/gate-watch.sh > /tmp/gate-watch.log 2>&1 &
#   make build > /tmp/gate.log 2>&1; echo "BUILD_EXIT=$?" >> /tmp/gate.log
#   # 跑完把后台收掉：kill %1
# 跑慢了就问日志：`grep -n "^== " /tmp/gate-watch.log` 找峰值段，看那是谁的 %CPU 或哪个计数涨了。
#
# 刻意不进 make vet/build：它要一直跑、会污染门禁自己的耗时读数（同 viewport-probe 那支的理由）。
set -u

INTERVAL="${INTERVAL:-5}"
NPROC=$(nproc)

echo "== gate-watch 起于 $(date '+%F %T')，间隔 ${INTERVAL}s，${NPROC} 核；对照口径：整段跑完再读，别看一眼就下结论"
while true; do
	ts=$(date '+%T')
	read -r l1 l5 l15 rest </proc/loadavg
	# 谁在吃 CPU：按 %CPU 排前 5，带累计 CPU 秒与存活时长（etimes）——
	# "一个新起的重活"与"一个跑了三小时的常驻"是完全不同的结论
	top=$(ps -eo pcpu,etimes,comm --sort=-pcpu | awk 'NR>1 && NR<=6 {printf "%s(%.0fs×%ss) ", $3, $1, $2}')
	# 那两个集成包在等什么：本地 TCP 的 TIME_WAIT 与当前 ESTAB 计数
	# （集成用例每条都要起一个 HTTP 服务并打 localhost，端口状态是它们唯一公共的外部依赖）
	tw=$(ss -tan state time-wait 2>/dev/null | wc -l)
	est=$(ss -tan state established 2>/dev/null | wc -l)
	# 内存与回收：tmpfs(/tmp) 上的临时 SQLite 会算进内存，集成包各建几百个
	read -r mem_total mem_avail < <(awk '/MemTotal/{t=$2} /MemAvailable/{print t, $2}' /proc/meminfo)
	free_kb=$(awk '/^MemFree:/{print $2}' /proc/meminfo)
	st=$(awk '/^stat/{exit} {print}' /proc/vmstat 2>/dev/null | grep -E '^(pgpgin|pgpgout|allocstall_normal|pgscan_direct)' | awk '{printf "%s=%s ", $1, $2}')
	printf '%s load=%s/%s/%s(核%s) top=%s | tw=%s estab=%s | mem可用=%sM 空闲=%sM | %s\n' \
		"$ts" "$l1" "$l5" "$l15" "$NPROC" "$top" "$tw" "$est" \
		"$((mem_avail / 1024))" "$((free_kb / 1024))" "$st"
	sleep "$INTERVAL"
done
