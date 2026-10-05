#!/usr/bin/env bash
# 部署脚本不许「逐字节写正在被执行的那条路径」——必须先落同目录临时名再 rename，并复算 md5。
#
# 判据来源是现网 14 天 journal 里的三次同款崩溃循环（09-28 23:42 连炸 118 次 203/EXEC、
# 10-01 10:58 与 10-04 01:10 各一轮 SIGSEGV 且没有任何应用日志）。共同形状：
# 重启发生在文件还没写完的那一刻，systemd 把半截二进制拿来 exec，而 `Restart=always`
# 把崩溃循环伪装成「运行中」。AGENTS §3 的换装三查第 2 条早就写了要「核 md5 与 install 同一条命令、
# 装完复算」，但 deploy.sh 自己没做到——**规范写在文档里而工具不执行它，等于没有规范**（待办清单 P79）。
set -euo pipefail

cd "${PRECHECK_SCAN_ROOT:-$(dirname "$0")/..}"  # 预检携带分支时由 precheck-branch.sh 指到分支树，默认仍是本仓根

SCRIPT=scripts/deploy.sh
bad=()

# ① 活路径必须只由 rename（place）抵达：install/cp/tee/重定向把它当**目的地**一律红
#    （把它当读取源——例如备份 "$SUDO cp "$DEPLOY_DIR/$BIN" "$LATEST_BACKUP""——不算，所以判据是行尾）
while IFS= read -r line; do
	bad+=("直写活路径（应走 place：同目录临时名 + mv + md5 复算）：$line")
done < <(grep -nE '(install|cp|tee)([[:space:]]+[^[:space:]]+)+[[:space:]]+"\$DEPLOY_DIR/\$BIN"[[:space:]]*$|>>?[[:space:]]*"\$DEPLOY_DIR/\$BIN"[[:space:]]*$' "$SCRIPT" \
	| grep -vE '^[0-9]+:[[:space:]]*#' || true)

# ② place 本身不许被改成非 rename：少 mv、或少 md5 复算，都等于这条规则失效
if ! grep -q 'mv -f' "$SCRIPT"; then
	bad+=('place() 里找不到 `mv -f`——rename 才是原子落地的关键，cp/install 直写不是')
fi
# 复算必须是「对**落地的目标**再算一次并比较」——只 grep md5sum 会被源文件那一半骗过去
# （V3 变异：删掉 md5_dst 那行，md5_src 还在，扫描器照样绿。判据：**规则要指向被删的那个东西本身**）
if ! grep -q 'md5sum "$dst"' "$SCRIPT"; then
	bad+=('place() 没有对落地后的目标复算 md5（找不到 md5sum "$dst"）——换装三查第 2 条要脚本自己做到')
elif ! grep -q 'md5_src" = "$md5_dst"' "$SCRIPT"; then
	bad+=('place() 复算了 md5 却没有比较——不复算比直接算等于没算')
fi

# ③ 回滚那一路也必须走 place（回滚同样是在覆盖活路径）
if grep -qE 'cp[[:space:]]+"\$LATEST_BACKUP"' "$SCRIPT"; then
	bad+=('回滚在用 cp 覆盖活路径，改回 place')
fi

if [ ${#bad[@]} -gt 0 ]; then
	echo '部署脚本把二进制逐字节写进正在被执行的路径（半截文件会被 systemd 立刻 exec）：'
	for b in "${bad[@]}"; do
		echo "  - $b"
	done
	exit 1
fi
