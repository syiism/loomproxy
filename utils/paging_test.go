package utils

import (
	"math"
	"strconv"
	"testing"
)

// oldInlineRule 是被 utils.Paginate 取代的那六处内联写法（逐字对照，不是凭记忆重写）：
// 页码与页长各自 Atoi 并丢掉错误，然后 page<1 回 1、页长越界回该端点默认值。
//
// 用例的形状是"新旧两式在合法输入上逐点同值"，而不是只测新式——**收成一处时最容易改坏的是边界语义**
// （比如把"超上限回默认"顺手写成"钳到上限"，面板就会在 ?page_size=500 时拿到 100 而不是 20）。
func oldInlineRule(pageRaw, sizeRaw string, fallback int) (int, int) {
	page, _ := strconv.Atoi(pageRaw)
	size, _ := strconv.Atoi(sizeRaw)
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = fallback
	}
	return page, size
}

const (
	hugePage  = "999999999999999999999" // 超出 int64：Atoi 报错，但**值仍是 MaxInt64**
	maxInt64S = "9223372036854775807"   // 正好 MaxInt64：连错误都没有
)

func TestPaginateMatchesTheSixInlineCopies(t *testing.T) {
	cases := []struct {
		pageRaw, sizeRaw string
		fallback         int
	}{
		{"", "", 20},
		{"abc", "xyz", 20},
		{"0", "0", 20},
		{"-3", "-1", 10},
		{"1", "1", 20},
		{"2", "100", 20},
		{"2", "101", 20}, // 超上限 → 回该端点默认，不是钳到 100
		{"2", "100000", 10},
		{" 3 ", "25", 20},     // 带空格是非法值（两式都回默认）
		{"1000000", "25", 20}, // 恰好等于页码上限：两式一致放行
		{"3", "20", 10},       // 正常路径
	}
	for _, c := range cases {
		wantPage, wantSize := oldInlineRule(c.pageRaw, c.sizeRaw, c.fallback)
		gotPage, gotSize := Paginate(c.pageRaw, c.sizeRaw, c.fallback)
		if gotPage != wantPage || gotSize != wantSize {
			t.Errorf("Paginate(%q,%q,%d)=(%d,%d) 与旧内联写法 (%d,%d) 不一致",
				c.pageRaw, c.sizeRaw, c.fallback, gotPage, gotSize, wantPage, wantSize)
		}
	}
	if PageSizeMax != 100 {
		t.Errorf("PageSizeMax = %d, want 100——上限值本身是六个端点共享的那个数", PageSizeMax)
	}
}

// TestPaginateFixesTheOutOfRangePage 断言的是这次"收成一处"顺带修掉的缺陷（待办清单 P86）。
// 两半都要测：**新式给什么**，以及**旧式的坏值今天仍然成立**——
// 后者是这条缺陷的凭据，前提一变（比如 Atoi 不再钳位）就该重看，而不是让它悄悄过期。
//
// 旧式的形状：`page, _ := strconv.Atoi(...)` 丢掉错误 ⇒ Atoi 对超范围输入返回 ErrRange
// 但**值仍是 MaxInt64** ⇒ `(page-1)*pageSize` 在 int 上溢出成负数（20 一页时实测 -40）
// ⇒ GORM 对负 offset 的处理是**整段不写 OFFSET**（DryRun 实测只剩 `… ORDER BY id DESC LIMIT 20`）
// ⇒ 响应用 `page: 9223372036854775807` 的壳子装着**第一页的数据**。
func TestPaginateFixesTheOutOfRangePage(t *testing.T) {
	if got, _ := Paginate(hugePage, "20", 20); got != 1 {
		t.Errorf("超范围页码没有得到第 1 页，实得 %d", got)
	}
	if got, _ := Paginate(maxInt64S, "20", 20); got != 1 {
		t.Errorf("正好 MaxInt64 的页码（Atoi 不报错）没有得到第 1 页，实得 %d——只判 err 就会漏掉这一支", got)
	}

	oldHuge, errHuge := strconv.Atoi(hugePage)
	if errHuge == nil || oldHuge != math.MaxInt64 {
		t.Fatalf("Atoi 对超范围输入不再返回钳位值（got=%d err=%v）——P86 的成因变了，这条凭据要重看", oldHuge, errHuge)
	}
	exact, errExact := strconv.Atoi(maxInt64S)
	if errExact != nil || exact != math.MaxInt64 {
		t.Fatalf("正好 MaxInt64 的输入本应无错通过（err=%v, got=%d）——前提变了", errExact, exact)
	}
	if off := (oldHuge - 1) * 20; off >= 0 {
		t.Errorf("旧写法在超范围页码下 offset=%d，不再为负——上面那条 GORM 丢 OFFSET 的读数要重测", off)
	}
	if off := (exact - 1) * 20; off >= 0 {
		t.Errorf("旧写法在 MaxInt64 页码下 offset=%d，不再为负——同上", off)
	}
}
