#!/usr/bin/env bash
# 分页夹只许有一处定义：控制面的端点不许再自己写 `Atoi(page) + <1/>100` 那一段。
#
# 判据来源：`utils.Paginate` 之前的现网形状——同一条规则（`pageSize < 1 || pageSize > 100`）
# 在六个端点里各抄一遍（redeem / admin usage_logs / devices / users / monitor history / quota usage_logs），
# 六份拷贝今天值一致，但**第七个端点可以静默不写上限**，那时 `?page_size=100000` 就是一条重查询。
# 这与待办清单 P69（「授权一行的形状有四处构造，两份默认值」）、P48（0 的语义三处各判一次）同族：
# 同一份规则写多遍，第一次漂移之前没有人拦得住。
set -euo pipefail

cd "$(dirname "$0")/.."

bad=()

# ① 控制面里不许再出现「就地解析分页参数」的写法
while IFS= read -r line; do
	bad+=("就地解析分页参数（改走 utils.Paginate）：$line")
done < <(grep -rnE 'strconv\.Atoi\(c\.(DefaultQuery|Query)\("(page|page_size)"' --include="*.go" app handlers gate middleware 2>/dev/null || true)

# ② 唯一的定义必须在，且必须真的夹上限——少了这一半，规则又变成口头约定
if ! grep -q 'const PageSizeMax = 100' utils/paging.go; then
	bad+=('utils/paging.go 里没有 `const PageSizeMax = 100`——上限必须只有一个定义点')
fi
if ! grep -q '> PageSizeMax' utils/paging.go; then
	bad+=('utils/paging.go 的 Paginate 没有用 PageSizeMax 夹上限（定义了不夹等于没定义）')
fi
if ! grep -q 'const PageMax = 1000000' utils/paging.go; then
	bad+=('utils/paging.go 没有页码上限 PageMax——Atoi 的 ErrRange 仍返回 MaxInt64，'+
	      '只判 err 会漏掉"正好 MaxInt64"这种没有错误的输入（待办清单 P86）')
fi
if ! grep -q '> PageMax' utils/paging.go; then
	bad+=('utils/paging.go 定义了 PageMax 却没夹（定义了不夹等于没定义）')
fi

# ③ 旧的内联形状不许复活（有人复制粘贴就会带回来）
while IFS= read -r line; do
	bad+=("内联的分页夹又出现了：$line")
done < <(grep -rnE 'pageSize < 1 \|\| pageSize > [0-9]+' --include="*.go" app handlers gate middleware 2>/dev/null || true)

if [ ${#bad[@]} -gt 0 ]; then
	echo '分页夹不再是单一事实来源（同一份规则写多遍，第七处会静默漏掉上限）：'
	for b in "${bad[@]}"; do
		echo "  - $b"
	done
	exit 1
fi
