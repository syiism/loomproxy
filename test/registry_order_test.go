package test

// 注册表下发的顺序必须是**全序**（第十五遍巡检，判据「下发给面板/调用方的顺序有没有二级键」）。
//
// 底层是 sync.Map，`Range` 的遍历顺序按桶散列、同一份表两次调用就可能不同。
// 所以 `AllSorted()` 原来那句「按优先级降序」在有并列时其实是"降序 + 随机"：
// 调用方看不出这是顺序问题，只会看到一份每次略微不同的列表。
// 本仓为同一个形状付过学费——管理端按聚合列排序不带二级键时，翻页会重行/漏行（待办清单 P45）。

import (
	"sort"
	"strings"
	"testing"

	"loomproxy/base"
)

func TestRegistryAllSortedIsTotalOrder(t *testing.T) {
	r := base.NewRegistry[base.Handler]()

	// 注册顺序故意打乱；12 个同优先级制造大量并列，3 个高优先级验证第一级键仍说话
	names := []string{
		"m_zero", "k_seven", "a_one", "z_nine", "c_two", "b_tree",
		"h_six", "g_five", "e_four", "d_eeee", "n_ten", "l_eight",
	}
	for _, n := range names {
		if err := r.Register(n, newFake(n), 1, nil); err != nil {
			t.Fatalf("注册 %s 失败: %v", n, err)
		}
	}
	for _, n := range []string{"vip_top", "admin_top", "other_top"} {
		if err := r.Register(n, newFake(n), 10, nil); err != nil {
			t.Fatalf("注册 %s 失败: %v", n, err)
		}
	}

	// 期望值由本用例自己按「优先级降序，同优先级名字升序」算一遍（不复用被测实现的排序）
	want := append([]string{}, names...)
	want = append(want, "vip_top", "admin_top", "other_top")
	sort.SliceStable(want, func(i, j int) bool {
		pi, pj := 1, 1
		if strings.HasSuffix(want[i], "_top") {
			pi = 10
		}
		if strings.HasSuffix(want[j], "_top") {
			pj = 10
		}
		if pi != pj {
			return pi > pj
		}
		return want[i] < want[j]
	})
	if len(want) != 15 {
		t.Fatalf("样本数 = %d, want 15——fixture 没铺够，后面的比较没有并列可验", len(want))
	}

	// 反复调用：并列没二级键时，顺序会随 Range 的桶序变；跑 20 次里任意一次不一致就报出来
	for pass := 1; pass <= 20; pass++ {
		got := namesOf(r.AllSorted())
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("第 %d 次 AllSorted 的顺序 = %v\nwant %v\n"+
				"（并列项没有二级键时，sync.Map 的桶序会让同一份注册表排出不同顺序）", pass, got, want)
		}
	}
}
