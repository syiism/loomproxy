package utils

import "strconv"

// PageSizeMax 控制面分页的单页上限。**只在这一处定义**：
// 现网最大的一张表也就几万行，一屏拉一万行既看不出东西，又把"手滑改一下 URL"
// 变成一条重查询。以前这条夹在六个端点里各写一遍（`< 1 || > 100` 六份拷贝），
// 值今天一致，但第七个端点可以静默不写——待办清单 P69/P48 那两次的教训都是"同一份规则写多遍"。
const PageSizeMax = 100

// PageMax 页码的硬上限。它存在的唯一理由是让 `(page-1)*pageSize` 不可能在 int 上溢出：
// 页长已被夹到 100，所以 offset ≤ 1e6 × 100 = 1e8——一个真实到不了的数（现网最大的一张表
// 也就几万行），越界到这里意味着输入是手打的或恶意构造的（待办清单 P86）。
// 取 1e6 而不是 MaxInt32 是为了**在 32 位 int 上也不溢出**：那才是这条夹唯一要做到的事。
const PageMax = 1000000

// Paginate 把两个 query 参数夹成 (page, pageSize)，语义与它替代的六处内联写法逐字一致：
// 非法或缺失的页码回 1；页长非法、小于 1 或超过上限时回 **该端点自己的默认页长**
// （面板各处历史上是 20，用户侧流水是 10——默认值是部署事实，不在这里统一）。
// 返回错误是刻意的不做：一个手打的 page=abc 不该让整页打不开。
// 页码除了"非法回 1"，还夹一个 PageMax——`strconv.Atoi` 对超范围的输入返回 ErrRange **但值是 MaxInt64**，
// 只看 err<1 会漏掉"正好等于 MaxInt64"这种连错误都没有的输入。
func Paginate(pageRaw, sizeRaw string, fallbackSize int) (int, int) {
	page, err := strconv.Atoi(pageRaw)
	if err != nil || page < 1 || page > PageMax {
		page = 1
	}
	size, err := strconv.Atoi(sizeRaw)
	if err != nil || size < 1 || size > PageSizeMax {
		size = fallbackSize
	}
	return page, size
}
