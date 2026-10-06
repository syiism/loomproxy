package gate

// transferPlan 的包内用例：这段算术是额度转移唯一"错了也不报错"的地方，
// 所以它必须能在不碰数据库的情况下被喂样本（骨架的包内单测这轮才进 `make build`，S30 那个盲区）。

import "testing"

func TestTransferPlanWritesOverridesNotEffectiveLimits(t *testing.T) {
	// 核心一条：返回的增量要**减回 base**。第一版实现直接把有效额度当增量写库，
	// 于是 base=100、转 30 出去，覆盖行会写成 70（有效额度变成 170），而接口一路 200。
	cases := []struct {
		name                           string
		baseFrom, ovFrom, baseTo, ovTo int64
		amount                         int64
		wantFromOv, wantToOv           int64
		wantFromBefore, wantFromAfter  int64
		wantToBefore, wantToAfter      int64
		ok                             bool
	}{
		{"无覆盖的两端", 100, 0, 50, 0, 30, -30, 30, 100, 70, 50, 80, true},
		{"转出方本来有追加", 100, 50, 50, 0, 30, 20, 30, 150, 120, 50, 80, true},
		{"转出方本来被扣减", 100, -40, 50, 0, 20, -60, 20, 60, 40, 50, 70, true},
		// 正好转空：有效额度 0 = 覆盖写成 -base（**不是 0**，0 是"没有覆盖"）
		{"转空", 100, 0, 50, 0, 100, -100, 100, 100, 0, 50, 150, true},
		// **增量落到 0 的那一头**：转出方正好回到套餐值 ⇒ 增量算出来是 0，调用方要删行而不是留一行 0
		// （0 在这张表上的语义是"没有覆盖"，留着它 P46 那个「被手工刷过额度」的筛选就会把没刷过的人数进去）
		{"转出回到套餐值（增量落 0）", 100, 30, 50, 0, 30, 0, 30, 130, 100, 50, 80, true},
		// 转入侧被补回到套餐值：base=50、ov=-30（有效 20），转进 30 ⇒ 有效 50、增量 0 ⇒ 也要删行。
		// 两端都可能落 0，所以删行这件事**不是转出侧专用的补丁**。
		{"转入回到套餐值（增量落 0）", 100, 0, 50, -30, 30, -30, 0, 100, 70, 20, 50, true},
		// 守恒：两端变化量之和恒为 0
		{"转出超过可转量被拒", 100, 0, 50, 0, 101, 0, 0, 0, 0, 0, 0, false},
		{"零与负数量被拒", 100, 0, 50, 0, 0, 0, 0, 0, 0, 0, 0, false},
		{"负数量被拒", 100, 0, 50, 0, -5, 0, 0, 0, 0, 0, 0, false},
		// 转入方原来被扣到**超出套餐限额**（base=50、ov=-80，有效额度被 clampLimit 夹在 0）：
		// 多扣的那 30 在有效额度里本来就看不见，所以这次转移写回的增量是 `-40` 而不是 `-70`——
		// **副作用**：转移会把"超出部分的扣减"归一化掉（存储值向 0 靠）。有效额度不受影响（一直是 0/10），
		// 但如果将来套餐限额调高，这个人会比"原本那行没被碰过"多出一截余量。
		// 这个形状是 clamp 语义的必然结果，不是这次的疏忽；写在这里是因为它会出现在支持工单里。
		{"转入侧原来夹在 0（超出部分被归一化）", 100, 0, 50, -80, 10, -10, -40, 100, 90, 0, 10, true},
	}
	for _, c := range cases {
		m, ok := transferPlan(c.baseFrom, c.ovFrom, c.baseTo, c.ovTo, c.amount)
		if ok != c.ok {
			t.Errorf("%s: ok = %v, want %v", c.name, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if m.FromBefore != c.wantFromBefore || m.FromAfter != c.wantFromAfter {
			t.Errorf("%s: 转出侧有效额度 %d→%d, want %d→%d", c.name, m.FromBefore, m.FromAfter, c.wantFromBefore, c.wantFromAfter)
		}
		if m.ToBefore != c.wantToBefore || m.ToAfter != c.wantToAfter {
			t.Errorf("%s: 转入侧有效额度 %d→%d, want %d→%d", c.name, m.ToBefore, m.ToAfter, c.wantToBefore, c.wantToAfter)
		}
		if m.FromOverrideAfter != c.wantFromOv || m.ToOverrideAfter != c.wantToOv {
			t.Errorf("%s: 要写回的增量 = (%d,%d), want (%d,%d)——**这是有效额度与覆盖增量混淆的那一格**",
				c.name, m.FromOverrideAfter, m.ToOverrideAfter, c.wantFromOv, c.wantToOv)
		}
		// 守恒是结构性的：一次转移不产生也不消灭额度（转出恰减、转入恰加）
		if d := (m.FromAfter - m.FromBefore) + (m.ToAfter - m.ToBefore); d != 0 {
			t.Errorf("%s: 两端有效额度之和变化 %d，应为 0（守恒破了就是凭空造额度）", c.name, d)
		}
		// 反推回来的增量必须能还原有效额度：base + ov == after，否则写库后判定读的不是算出来的那个数
		if got := clampLimit(c.baseFrom + m.FromOverrideAfter); got != m.FromAfter {
			t.Errorf("%s: 写回增量后转出侧有效额度 = %d，与算出的 %d 不一致", c.name, got, m.FromAfter)
		}
		if got := clampLimit(c.baseTo + m.ToOverrideAfter); got != m.ToAfter {
			t.Errorf("%s: 写回增量后转入侧有效额度 = %d，与算出的 %d 不一致", c.name, got, m.ToAfter)
		}
	}
}
