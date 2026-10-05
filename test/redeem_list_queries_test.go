package test

// 待办清单 P87：卡密列表原来**每行两条** `First`（查套餐名、查兑换者用户名）。
// 一页 20 条就是约 25 条 SQL，页长上限 100 时到 104 条，而 `redemption_codes` 正是
// 待办清单 P54 点名的"只增不减"表之一。现网今天只有 39 行，所以这不是救火，是把形状钉住：
// 名称改成整页一次 IN 取回，并断言**一次请求的 SQL 条数不随行数增长**。
//
// 断言有两条腿，缺一不可：条数要 bounded，**名称还要对**——
// 只测条数的话，把两行查询整个删掉也能"变快"，那不是我要的守卫。

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"loomproxy/db"
	"loomproxy/models"
)

// newQueryCounter 挂一次查询计数回调，返回"跑一段代码、告诉你它发了几条查询、都是哪些表"的闭包。
// **只数这一页的读对象**：第一版数的是"这段时间里的所有查询"，而回调注册在 `db.DB` 上是**全局**的——
// 整套件里任何一条后台查询（号池巡检、监控回填、命名缓存体检）都会落进这个窗口。
// 本轮就在 `make build` 的测试段被这样污染过一次：25 行那页报 8 条、3 行那页 7 条，
// 而单独跑这条用例 3/3 全绿——多出来那条根本不是这一页发的。
// 判据：**计数器的口径必须与断言的性质同宽**——断的是"这一页的查询不随行数变"，就只数这一页碰的表。
// 同时把数到的表名带进失败信息：下一次再红，第一眼就能看出是谁的查询。
// **只注册一次**（第一版每条用例各注册一次，GORM 会报 duplicated callback 警告——
// 数还是数对了，但读数里混着警告就不干净）；计数器每次调用前归零。
func newQueryCounter(t *testing.T, tables ...string) func(func()) (int, string) {
	t.Helper()
	want := make(map[string]bool, len(tables))
	for _, x := range tables {
		want[x] = true
	}
	n := 0
	var seen []string
	if err := db.DB.Callback().Query().Register("test/count-queries", func(tx *gorm.DB) {
		tbl := tx.Statement.Table
		if tbl == "" && tx.Statement.Schema != nil {
			tbl = tx.Statement.Schema.Table
		}
		if want[tbl] {
			n++
			seen = append(seen, tbl)
		}
	}); err != nil {
		t.Fatalf("注册计数回调失败: %v", err)
	}
	t.Cleanup(func() { _ = db.DB.Callback().Query().Remove("test/count-queries") })
	return func(fn func()) (int, string) {
		n, seen = 0, nil
		fn()
		return n, tallyTables(seen)
	}
}

// tallyTables 把数到的表名压成「表×次数」——失败信息要看得懂，而不是把 30 个词摊开成一面墙。
func tallyTables(list []string) string {
	if len(list) == 0 {
		return "（一条都没数到）"
	}
	order := make([]string, 0, len(list))
	count := make(map[string]int, len(list))
	for _, x := range list {
		if _, ok := count[x]; !ok {
			order = append(order, x)
		}
		count[x]++
	}
	parts := make([]string, 0, len(order))
	for _, name := range order {
		if count[name] == 1 {
			parts = append(parts, name)
			continue
		}
		parts = append(parts, fmt.Sprintf("%s×%d", name, count[name]))
	}
	return strings.Join(parts, " ")
}

// seedRedeemBatch 造 n 张已兑换的卡密（每张一个独立兑换者；每第 5 张指向一个不存在的套餐）。
// 返回 卡密 id → 期望的兑换者用户名。
func seedRedeemBatch(t *testing.T, vipID, ghostPlan uint, batch string, n int) map[uint]string {
	t.Helper()
	out := map[uint]string{}
	for i := 0; i < n; i++ {
		u := models.User{
			Username: fmt.Sprintf("%s-u-%02d", batch, i),
			Email:    fmt.Sprintf("%s-%02d@example.test", batch, i),
			Status:   1,
		}
		if err := db.DB.Create(&u).Error; err != nil {
			t.Fatalf("建用户 %s-%d 失败: %v", batch, i, err)
		}
		pid := vipID
		if i%5 == 0 {
			pid = ghostPlan // 套餐不存在：名称该留空，而且旧写法会为它每行重查
		}
		now := time.Now()
		rc := models.RedemptionCode{
			Code: fmt.Sprintf("%sCODE%02d", strings.ToUpper(batch), i), PlanID: pid, DurationDays: 30,
			Status: 2, BatchNo: batch, UsedBy: &u.ID, UsedAt: &now,
		}
		if err := db.DB.Create(&rc).Error; err != nil {
			t.Fatalf("建卡密 %s-%d 失败: %v", batch, i, err)
		}
		out[rc.ID] = u.Username
	}
	return out
}

func TestRedeemListQueriesDoNotGrowWithRows(t *testing.T) {
	srv := newTestServer(t)
	admin := authHeader(adminToken(t, srv))

	var vipID uint
	if err := db.DB.Model(&models.QuotaPlan{}).Where("code = ?", "vip").Select("id").Row().Scan(&vipID); err != nil {
		t.Fatalf("读 vip 套餐失败: %v", err)
	}
	const ghostPlan = uint(99999) // 指向一个不存在的套餐：旧写法会为它**每行重查一次**

	// 两批行数不同的卡密：真正要钉的性质是「这一页的查询条数不随行数变」，不是"某个绝对条数"——
	// 计数只数这一页的三张读对象（列表两次 + 套餐名 IN + 兑换者名 IN），
	// 鉴权与设置那些固定开销不在口径内（它们两边一样，且不属于这一页的形状）。
	byID := seedRedeemBatch(t, vipID, ghostPlan, "p87a", 25)
	seedRedeemBatch(t, vipID, ghostPlan, "p87b", 3)

	countQueries := newQueryCounter(t, "redemption_codes", "quota_plans", "users")
	countFor := func(batch string) (int, string, []interface{}, map[uint]string) {
		var status int
		var env apiEnvelope
		queries, tables := countQueries(func() {
			status, env = doJSON(t, srv, http.MethodGet, "/admin/redeem-codes?batch_no="+batch+"&page_size=100", nil, admin)
		})
		if status != http.StatusOK || env.Code != 0 {
			t.Fatalf("GET /admin/redeem-codes?batch_no=%s status=%d code=%d msg=%s", batch, status, env.Code, env.Msg)
		}
		raw, _ := env.dataMap(t)["list"].([]interface{})
		if queries == 0 {
			t.Fatal("一条查询都没数到——计数回调没生效，条数断言是空转")
		}
		return queries, tables, raw, byID
	}

	qBig, tBig, rawBig, names := countFor("p87a")
	qSmall, tSmall, rawSmall, _ := countFor("p87b")
	t.Logf("这一页数到的查询：25 行 = %d 条 [%s]；3 行 = %d 条 [%s]", qBig, tBig, qSmall, tSmall)
	if len(rawBig) != 25 || len(rawSmall) != 3 {
		t.Fatalf("两批样本 = %d / %d 条, want 25 / 3——前提不成立，下面的比对是空转", len(rawBig), len(rawSmall))
	}
	if qBig != qSmall {
		t.Errorf("25 行的一页发了 %d 条查询、3 行的一页 %d 条——查询条数在随行数增长（N+1）。"+
			"两边各数到：big=[%s] small=[%s]", qBig, qSmall, tBig, tSmall)
	}
	// 上限按**这个口径**给，数字是当场数出来的：实测 5 条 =
	// 鉴权 `users` 1 + 列表 `redemption_codes` 2（Count 与 Find）+ 两次名称 IN 2；6 是余量。
	// 变异回 P87 之前的逐行重查（每行一条 `First`）实测成 **25 行 = 30 条、3 行 = 8 条**，
	// 两条断言同时红——这条上限就是为那一支存在的。
	if qBig > 6 {
		t.Errorf("这一页发了 %d 条查询，want ≤6（口径=%s）——固定开销不该涨到这个量级，多半是逐行重查回来了", qBig, tBig)
	}
	_ = names

	// 第二条腿：名称必须仍然对（含"套餐不存在就留空"这一支）
	var wantVip int
	for _, r := range rawBig {
		m, _ := r.(map[string]interface{})
		id, _ := m["id"].(float64)
		name, _ := m["used_by_name"].(string)
		if want := names[uint(id)]; want != "" && name != want {
			t.Errorf("卡密 %v 的 used_by_name = %q, want %q——整页取名改错了映射", id, name, want)
		}
		plan, _ := m["plan_id"].(float64)
		pname, _ := m["plan_name"].(string)
		if uint(plan) == ghostPlan && pname != "" {
			t.Errorf("不存在的套餐 %v 却拿到了名字 %q——旧写法留空，新版也该留空", ghostPlan, pname)
		}
		if uint(plan) == vipID {
			if pname == "" {
				t.Errorf("vip 套餐的卡密没拿到套餐名——IN 查询漏了它")
			}
			wantVip++
		}
	}
	if wantVip == 0 {
		t.Error("这一页里没有一条是 vip 套餐的样本——套餐名的断言没被执行到（断言空转）")
	}
}
