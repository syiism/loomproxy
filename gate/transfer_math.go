package gate

// 额度转移的算术（P97 的实现核心，抽成纯函数的理由写在下面）。
//
// 抽出来的第二个理由比"好测"更硬：第一版我把 `transferDelta` 的返回值（**有效额度**口径）
// 直接当成**覆盖增量**写库了——两者差一个 `base`，而这种错位不 panic、不报错，
// 症状是"转 100 出去，那边变成 100+套餐限额"，要等有人去读覆盖行才发现。
// 把全部算术收进一个函数、参数与返回都写清口径，才有地方把这条钉住。

// transferMath 一次转移的完整算式结果。
// 命名规矩：**`*Before`/`*After` 一律是有效额度**（套餐限额 + 覆盖增量，下限 0），
// `*OverrideAfter` 才是要写回 `user_quota_overrides` 的**增量**；0 由调用方删行表达。
type transferMath struct {
	FromBefore        int64
	FromAfter         int64
	ToBefore          int64
	ToAfter           int64
	FromOverrideAfter int64
	ToOverrideAfter   int64
}

// transferPlan 算转移一笔之后的两端有效额度与覆盖增量。
//
// 入参是两端的 `base`（套餐限额，调用方保证 ≥ 0 且存在——"不限"与"未授权"在它之前就被拒了）
// 与当前的覆盖增量 `ov`（没有覆盖行传 0）。
// 三条规矩：
//   - `amount ≥ 1` 且 `fromBefore ≥ amount`：转出方没有那么多可转就直接拒，**不许夹到 0 后继续**
//     （夹到 0 再继续 = 把"超额转出"读成"成功转了一半"）。
//   - 转出恰减、转入恰加，两端有效额度之和不变 ⇒ 转出去再转回来不产生新额度（守恒）。
//   - 增量算完后如果正好是 0，**返回 0 让调用方删行**：这张表上 0 的语义是"没有覆盖"（P48/P85 那一族
//     「0 是没写还是要 0」的实例），留一行 0 会让「被手工刷过额度」的筛选（P46）把没刷过的人数进去。
func transferPlan(baseFrom, ovFrom, baseTo, ovTo, amount int64) (transferMath, bool) {
	var m transferMath
	if amount <= 0 {
		return m, false
	}
	m.FromBefore = clampLimit(baseFrom + ovFrom)
	m.ToBefore = clampLimit(baseTo + ovTo)
	if m.FromBefore < amount {
		return m, false
	}
	m.FromAfter = m.FromBefore - amount
	m.ToAfter = clampLimit(m.ToBefore + amount)
	// 反推回增量：有效额度是"套餐 + 覆盖"，写库要写的是覆盖，所以必须减回 base。
	// 这一步漏掉就是第一版那个 bug：把 100 当增量写进去，实际有效额度变成 100+套餐限额。
	m.FromOverrideAfter = m.FromAfter - baseFrom
	m.ToOverrideAfter = m.ToAfter - baseTo
	return m, true
}
