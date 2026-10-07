package test

// 用户管理筛选（P46）与「设备与密钥」排序（P45）的用例。
//
// 这一组的存在理由不是"覆盖功能"，而是钉住四件容易静默错的事：
//   ① NULL 有业务含义的三列（永久套餐 / 从未登录 / 未表态同意）不能被当成"过期 / 不活跃 / 关闭留存"；
//   ② 枚举型筛选值非法一律回默认（回 400 会让列表页因为一个手打错的 query 打不开），
//     自由文本型（角色码、关键词）没有非法值可言——筛不中就是空集，但不能报错、更不能当通配符；
//   ③ 排序并列时必须稳定（否则翻页会重复出现同一行、另一些行永远看不到）；
//   ④ 「会话数超上限」的筛选与页面上的活跃会话数必须是同一口径——两处不一致时这页就没法信；
//   ⑤ 为了统一 ④ 的口径而把「未吊销」塞进整条会话查询，会让「已吊销/设备/IP」三列一起静默归零，
//     所以这一列也钉住（活跃走 SQL，其余走窗口内全量）。

import (
	"fmt"
	"loomproxy/db"
	"loomproxy/models"
	"loomproxy/testkit"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// listUserIDs 拉一页用户列表并返回 id 集合（顺带断言 200）
func listUserIDs(t *testing.T, srv *httptest.Server, admin, query string) map[uint]bool {
	t.Helper()
	return testkit.ListUserIDs(t, srv, admin, query)
}

func mustUserRow(t *testing.T, planID *uint, username string) models.User {
	t.Helper()
	u := models.User{Username: username, Email: username + "@example.com", Status: 1, PlanID: planID}
	if err := db.DB.Create(&u).Error; err != nil {
		t.Fatalf("建用户 %s 失败: %v", username, err)
	}
	return u
}

func TestUserFiltersNULLSemantics(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)

	var vipID uint
	if err := db.DB.Model(&models.QuotaPlan{}).Where("code = ?", "vip").Select("id").Row().Scan(&vipID); err != nil {
		t.Fatalf("读 vip 套餐失败: %v", err)
	}
	pid := vipID

	// 四种到期/登录形态各造一个账号
	permanent := mustUserRow(t, &pid, "f_perm")  // plan_expire_at = NULL → 永久
	soon := mustUserRow(t, &pid, "f_soon")       // 3 天后到期
	later := mustUserRow(t, &pid, "f_later")     // 60 天后到期
	expired := mustUserRow(t, &pid, "f_expired") // 已过期
	d3 := time.Now().AddDate(0, 0, 3)
	d60 := time.Now().AddDate(0, 0, 60)
	past := time.Now().AddDate(0, 0, -2)
	db.DB.Model(&models.User{}).Where("id = ?", soon.ID).Update("plan_expire_at", d3)
	db.DB.Model(&models.User{}).Where("id = ?", later.ID).Update("plan_expire_at", d60)
	db.DB.Model(&models.User{}).Where("id = ?", expired.ID).Update("plan_expire_at", past)

	// ① expire=permanent 只应命中 NULL 那个；已过期绝不能算"即将到期"
	perm := listUserIDs(t, srv, admin, "?expire=permanent&page_size=100")
	if !perm[permanent.ID] {
		t.Errorf("expire=permanent 没含从未设到期时间的 %d（NULL=永久这一语义没实现）", permanent.ID)
	}
	for _, id := range []uint{soon.ID, later.ID, expired.ID} {
		if perm[id] {
			t.Errorf("expire=permanent 误含了 %d——有到期时间的账号不是永久", id)
		}
	}

	expiring := listUserIDs(t, srv, admin, "?expire=expiring&expire_days=7&page_size=100")
	if !expiring[soon.ID] {
		t.Errorf("3 天后到期的 %d 没出现在 expire=expiring", soon.ID)
	}
	for _, id := range []uint{later.ID, expired.ID, permanent.ID} {
		if expiring[id] {
			t.Errorf("expire=expiring 误含 %d（60 天后/已过期/永久都不该在 7 天窗口里）", id)
		}
	}
	// "已过期"和"即将到期"必须是两档，混起来运营就读不了
	expd := listUserIDs(t, srv, admin, "?expire=expired&page_size=100")
	if !expd[expired.ID] || expd[soon.ID] {
		t.Errorf("expire=expired 命中错位：%+v 应只含 %d", expd, expired.ID)
	}

	// ② 从未登录是一档，不是"活跃度低"
	db.DB.Model(&models.User{}).Where("id = ?", later.ID).Update("last_login_at", time.Now().AddDate(0, 0, -100))
	never := listUserIDs(t, srv, admin, "?activity=never&page_size=200")
	if !never[permanent.ID] {
		t.Errorf("activity=never 漏掉了从未登录的 %d", permanent.ID)
	}
	if never[later.ID] {
		t.Errorf("activity=never 误含 100 天前登录过的 %d", later.ID)
	}
	stale := listUserIDs(t, srv, admin, "?activity=stale&page_size=200")
	if !stale[later.ID] {
		t.Errorf("activity=stale 漏掉 100 天未登录的 %d", later.ID)
	}
	// NULL 不能同时属于两档
	if never[later.ID] && stale[later.ID] {
		t.Errorf("%d 同时出现在 never 与 stale 两档，边界没收口", later.ID)
	}
}

func TestUserFiltersIllegalValuesFallBack(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)
	// 基线里多几个人，「没筛」与「筛空了」才分得开（只有一个 admin 时两种情况都是 1 行）
	mustUserRow(t, nil, "f_base1")
	mustUserRow(t, nil, "f_base2")
	base := listUserIDs(t, srv, admin, "?page_size=200")
	if len(base) < 3 {
		t.Fatalf("基线只有 %d 行，这批断言没有意义", len(base))
	}

	// 枚举型筛选喂非法值：期望是"不筛"（等于基线），而不是 400——列表页因一个手打错的 query 打不开最糟
	for _, q := range []string{
		"?status=whatever",
		"?plan=-3",
		"?plan=not-a-number",
		"?expire=future",
		"?expire_days=99999",
		"?activity=2d",
		"?consent=maybe",
		"?quota_reset=someday",
		"?keys=lots",
		"?over_cap=yes",
	} {
		got := listUserIDs(t, srv, admin, q+"&page_size=200")
		if len(got) != len(base) {
			t.Errorf("非法值 %q 改变了结果（%d 行 vs 基线 %d 行）——它本该被忽略", q, len(got), len(base))
		}
	}

	// 自由文本型（role/keyword）没有"非法值"可言：等值筛不中就是空集，但它不能报错、也不能当通配符
	if got := listUserIDs(t, srv, admin, "?role=../../etc/passwd&page_size=200"); len(got) != 0 {
		t.Errorf("role=../../etc/passwd 命中 %d 行，等值筛选不该匹配到任何账号", len(got))
	}

	// 关键词里的 SQL/LIKE 通配符不能当通配符用（P43 那条 LIKE 判据的回归位）。
	// % 在 URL 里必须写成 %25——直接写 %& 会让整个 query 解析失败，keyword 变空串，
	// 于是"没转义"与"没传参"两种错在这一条断言下长得一样，用例就先红了给我看。
	if hits := listUserIDs(t, srv, admin, "?keyword=%25&page_size=200"); len(hits) > 0 {
		t.Errorf("keyword=%% 命中了 %d 个账号，未转义时会匹配全表", len(hits))
	}
	// 单字符通配符要挑一个"通配能中、字面不中"的词：账号 f_wildsig 里有 s-i-g，
	// 未转义时 `%s_g%` 会命中它，转义后按字面 `s_g` 匹配就该是空集。
	// （先前我拿 keyword=_ 断言空集，结果被自己造的 f_base1/f_base2 里的下划线正当地命中——
	// 断言写错了不是代码写错了，这种红法会把人引去改本来是对的那一侧。）
	mustUserRow(t, nil, "f_wildsig")
	if hits := listUserIDs(t, srv, admin, "?keyword=s_g&page_size=200"); len(hits) > 0 {
		t.Errorf("keyword=s_g 命中了 %d 个账号，_ 没被转义成字面下划线", len(hits))
	}
	if hits := listUserIDs(t, srv, admin, "?keyword=wildsig&page_size=200"); len(hits) != 1 {
		t.Errorf("keyword=wildsig 应命中刚建的那 1 个账号，实得 %d——转义把正常匹配也弄坏了", len(hits))
	}
}

func TestUserFilterStatusDeleted(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)
	u := mustUserRow(t, nil, "f_gone")
	db.DB.Delete(&models.User{}, u.ID) // 软删：默认视图该看不见它，status=deleted 该看见它

	if got := listUserIDs(t, srv, admin, "?page_size=200"); got[u.ID] {
		t.Errorf("默认列表含已软删的账号 %d，不该看见软删行", u.ID)
	}
	// 「已删除」单列一档：原来的 with_deleted 复选框是把正常+已删除混成一锅
	got := listUserIDs(t, srv, admin, "?status=deleted&page_size=200")
	if !got[u.ID] {
		t.Errorf("status=deleted 没含刚软删的 %d", u.ID)
	}
	db.DB.Unscoped().Delete(&models.User{}, u.ID)
	if still := listUserIDs(t, srv, admin, "?status=normal&page_size=200"); still[u.ID] {
		t.Errorf("status=normal 仍含已删除的 %d", u.ID)
	}
}

func TestUserFilterQuotaResetAndConsent(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)
	a := mustUserRow(t, nil, "f_qr1")
	b := mustUserRow(t, nil, "f_qr2")

	db.DB.Model(&models.User{}).Where("id = ?", a.ID).Update("quota_reset_at", time.Now())
	got := listUserIDs(t, srv, admin, "?quota_reset=any&page_size=200")
	if !got[a.ID] || got[b.ID] {
		t.Errorf("quota_reset=any 命中错位：%+v 应只含 %d", got, a.ID)
	}

	// 同意位：NULL(未表态) 与 true(明确同意) 语义相同但来源不同，筛选要能分开
	db.DB.Model(&models.User{}).Where("id = ?", b.ID).Update("content_consent", false)
	off := listUserIDs(t, srv, admin, "?consent=off&page_size=200")
	if !off[b.ID] {
		t.Errorf("consent=off 漏掉明确关闭的 %d", b.ID)
	}
	if off[a.ID] {
		t.Errorf("consent=off 误含未表态的 %d——NULL 不是 false", a.ID)
	}
	if n := len(listUserIDs(t, srv, admin, "?consent=unset&page_size=200")); n == 0 {
		t.Errorf("consent=unset 一个都没命中；注册即写 true 才算已表态，未表态的存量账号应该在这里")
	}

	// recent 与 any 的差别是「统计窗口」，窗口与「设备与密钥」页共用 auth.WatchWindowStart
	c := mustUserRow(t, nil, "f_qr3")
	db.DB.Model(&models.User{}).Where("id = ?", c.ID).
		Update("quota_reset_at", time.Now().AddDate(0, 0, -30))
	recent := listUserIDs(t, srv, admin, "?quota_reset=recent&page_size=200")
	if !recent[a.ID] {
		t.Errorf("quota_reset=recent 漏掉刚刷过的 %d", a.ID)
	}
	if recent[c.ID] {
		t.Errorf("quota_reset=recent 误含 30 天前刷过的 %d——窗口口径没生效", c.ID)
	}
	anyOne := listUserIDs(t, srv, admin, "?quota_reset=any&page_size=200")
	if !anyOne[c.ID] {
		t.Errorf("quota_reset=any 应含不限时间的 %d", c.ID)
	}
}

// insertKeys 直接写 api_keys 表：这条用例要钉的是筛选档位，不是签发通路（走接口得每人发 5 次）
func insertKeys(t *testing.T, userID uint, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		k := &models.ApiKey{UserID: userID, Key: fmt.Sprintf("lp_test_%d_%03d", userID, i), Name: "t"}
		if err := db.DB.Create(k).Error; err != nil {
			t.Fatalf("建密钥失败: %v", err)
		}
	}
}

func TestUserFilterKeysAndActiveWindow(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)

	// 档位从响应里读，用例自己不抄数字：抄了就等于允许它和后端常量各自漂移
	status, env := doJSON(t, srv, http.MethodGet, "/admin/users?page_size=1", nil, authHeader(admin))
	if status != http.StatusOK {
		t.Fatalf("读筛选档位失败: status=%d", status)
	}
	meta, _ := env.dataMap(t)["filters_meta"].(map[string]interface{})
	threshold, _ := meta["many_api_keys"].(float64)
	if threshold < 2 {
		t.Fatalf("filters_meta.many_api_keys 没下发或太小：%+v", meta)
	}

	none := mustUserRow(t, nil, "k_none")
	few := mustUserRow(t, nil, "k_few")
	many := mustUserRow(t, nil, "k_many")
	insertKeys(t, few.ID, int(threshold)-1)
	insertKeys(t, many.ID, int(threshold))

	if got := listUserIDs(t, srv, admin, "?keys=none&page_size=200"); !got[none.ID] || got[few.ID] || got[many.ID] {
		t.Errorf("keys=none 命中错位（应只含无密钥的 %d）：%+v", none.ID, got)
	}
	if got := listUserIDs(t, srv, admin, "?keys=any&page_size=200"); got[none.ID] || !got[few.ID] || !got[many.ID] {
		t.Errorf("keys=any 命中错位（应含 %d 与 %d、不含 %d）", few.ID, many.ID, none.ID)
	}
	// 边界就卡在档位值上：threshold-1 不算「偏多」，threshold 算
	if got := listUserIDs(t, srv, admin, "?keys=many&page_size=200"); got[few.ID] || !got[many.ID] {
		t.Errorf("keys=many 边界错位：%d 把（档位 %v-1）被算进来了，或 %d 把的没进来", int(threshold)-1, threshold, many.ID)
	}

	// expire=active：有到期时间且未过期。永久（NULL）**不在这一档**——它没有期限，
	// 把「没期限」读成「一直有效」是这一列最容易读错的地方
	soon := mustUserRow(t, nil, "k_active")
	past := mustUserRow(t, nil, "k_past")
	perm := mustUserRow(t, nil, "k_perm")
	db.DB.Model(&models.User{}).Where("id = ?", soon.ID).Update("plan_expire_at", time.Now().AddDate(0, 0, 3))
	db.DB.Model(&models.User{}).Where("id = ?", past.ID).Update("plan_expire_at", time.Now().AddDate(0, 0, -1))
	act := listUserIDs(t, srv, admin, "?expire=active&page_size=200")
	if !act[soon.ID] {
		t.Errorf("expire=active 漏掉 3 天后到期的 %d", soon.ID)
	}
	for _, id := range []uint{past.ID, perm.ID} {
		if act[id] {
			t.Errorf("expire=active 误含 %d（已过期或永久都不在「有效期内」）", id)
		}
	}
}

func TestDeviceSortStabilityAndKeys(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)

	// 造一个"明显最不像一个人用"的账号，和一批并列 1 个会话的账号
	hot, hotName := watchUser(t, srv, admin)
	for i := 1; i <= 6; i++ {
		loginFrom(t, srv, hotName, "pass1234", fmt.Sprintf("10.99.%d.%d", i, i))
	}
	plain, plainName := watchUser(t, srv, admin)
	loginFrom(t, srv, plainName, "pass1234", "10.99.9.9")
	// 一个从未登录的账号：last_login 的 NULL 兜底断言没有它就整段是空转
	// （第一版我就是这么绿的——这条用例里三个人全都登录过，"NULL 排最后"一句谁也没验到）
	mustUserRow(t, nil, "sort_never")

	firstPage := func(query string) []map[string]interface{} {
		status, env := doJSON(t, srv, http.MethodGet, "/admin/devices"+query, nil, authHeader(admin))
		if status != http.StatusOK || env.Code != 0 {
			t.Fatalf("GET /admin/devices%s status=%d msg=%s", query, status, env.Msg)
		}
		raw, _ := env.dataMap(t)["list"].([]interface{})
		out := make([]map[string]interface{}, 0, len(raw))
		for _, r := range raw {
			m, _ := r.(map[string]interface{})
			out = append(out, m)
		}
		return out
	}

	rows := firstPage("?sort=sessions&dir=desc&page_size=10")
	if len(rows) == 0 {
		t.Fatalf("排序后列表为空")
	}
	if id, _ := rows[0]["user_id"].(float64); uint(id) != hot {
		t.Errorf("按会话数降序第一条应是 %d（6 个会话），实得 %v", hot, rows[0]["user_id"])
	}
	// 降序单调性：不能只有第一条对
	var prev int64 = 1 << 60
	for _, r := range rows {
		n, _ := r["active_sessions"].(float64)
		if int64(n) > prev {
			t.Fatalf("active_sessions 不单调递减：%v 出现在 %v 之后", n, prev)
		}
		prev = int64(n)
	}

	// 非法 sort 回默认而不是 400（列表页打不开比静默用默认更糟）
	fallback := firstPage("?sort=(SELECT+1)&dir=sideways&page_size=5")
	if len(fallback) == 0 {
		t.Fatalf("非法 sort/dir 让列表空了——它该回落到默认排序")
	}
	if fb, _ := fallback[0]["user_id"].(float64); uint(fb) != hot {
		t.Errorf("非法 sort 的回落结果不是默认序第一条（默认应是会话数最多的 %d，实得 %v）", hot, fallback[0]["user_id"])
	}

	// ③ 并列值的次序：这一页大量账号都是「1 个会话 / 0 把密钥」，并列不是边角而是常态，
	// 页面必须给出可复现的次序（并列按 user_id 倒序），否则翻页会重复出现同一行、另一些行永远看不到。
	// 断言写在**页面上**而不是写在比较器里。变异验证过两个方向：把二级键改成 id 正序 → 这里红；
	// 把 SliceStable 换成 Slice 且去掉二级键 → **仍然绿**（十几行数据时 pdqsort 走插入排序，并列不动），
	// 所以这条钉的是「并列次序是什么」这个读数，钉不住「用了哪种 sort」——后者本来就属于实现细节。
	tied := firstPage("?sort=keys&dir=desc&page_size=100")
	ties := 0
	for i := 1; i < len(tied); i++ {
		k0, _ := tied[i]["api_keys"].(float64)
		k1, _ := tied[i-1]["api_keys"].(float64)
		if k0 == k1 {
			ties++
		}
	}
	// 空转守卫（本轮在下面的 NULL 断言上被这种绿骗过一次）：并列样本为 0 时这条什么也没验
	if ties == 0 {
		t.Fatalf("这批数据里没有并列的密钥数（共 %d 行），「并列按 id 倒序」这条断言是空转", len(tied))
	}
	var prevKeys, prevID float64 = 1 << 60, 1 << 60
	for _, r := range tied {
		k, _ := r["api_keys"].(float64)
		id, _ := r["user_id"].(float64)
		if k > prevKeys {
			t.Fatalf("api_keys 不单调递减：%v 出现在 %v 之后", k, prevKeys)
		}
		if k == prevKeys && id > prevID {
			t.Errorf("并列值 %v 把 user_id=%v 排在了 %v 之前——二级次序不是 id 倒序，翻页会重行或漏行", k, id, prevID)
		}
		prevKeys, prevID = k, id
	}

	// 跨页不重不漏
	seen := map[uint]bool{}
	for p := 1; p <= 3; p++ {
		page := firstPage(fmt.Sprintf("?sort=sessions&dir=desc&page=%d&page_size=2", p))
		for _, r := range page {
			id, _ := r["user_id"].(float64)
			if seen[uint(id)] {
				t.Errorf("按会话数排序时 user_id=%d 在第 %d 页重复出现", uint(id), p)
			}
			seen[uint(id)] = true
		}
		if len(page) < 2 {
			break
		}
	}

	// 「从未登录」(NULL) 在升、降两种方向下都排最后：那是「没有这个值」，不该插进有值的序列中间
	for _, d := range []string{"asc", "desc"} {
		rows := firstPage("?sort=last_login&dir=" + d + "&page_size=100")
		nulls, withs := 0, 0
		for _, r := range rows {
			if r["last_login_at"] == nil {
				nulls++
			} else {
				withs++
			}
		}
		if nulls == 0 || withs == 0 {
			t.Fatalf("dir=%s 时 NULL 样本 %d、有值样本 %d——这条断言在这批数据上是空转（本轮就先这么绿过一次：那三个账号全都登录过）",
				d, nulls, withs)
		}
		seenNull := false
		for _, r := range rows {
			if r["last_login_at"] == nil {
				seenNull = true
			} else if seenNull {
				t.Errorf("dir=%s 时有 last_login 的行排在了 NULL 之后——NULL 没兜在最后", d)
			}
		}
	}

	// ④ 筛选与排序同口径：over_cap 筛出来的人，页面上的会话数必然都超上限
	capN := db.GetSetting("max_active_sessions")
	if capN == "" {
		capN = "5"
	}
	over := listUserIDs(t, srv, admin, "?over_cap=1&page_size=100")
	if !over[hot] {
		t.Errorf("over_cap=1 没含 6 个会话的 %d——两处口径不一致（P45/P46 必须共用活跃会话定义）", hot)
	}
	if over[plain] {
		t.Errorf("over_cap=1 误含只有 2 个会话（注册+登录）的 %d，上限 %s", plain, capN)
	}

	// ⑤ 「已吊销」这一列不许静默成 0。上一版把 `revoked_at IS NULL` 塞进整条会话查询，
	// 症状是这列恒空、按它排序全并列，而没有任何用例会红——因为它本来就不在断言里。
	var revokedID uint
	if err := db.DB.Model(&models.AuthSession{}).Where("user_id = ?", hot).
		Order("id DESC").Limit(1).Pluck("id", &revokedID).Error; err != nil || revokedID == 0 {
		t.Fatalf("取一条会话备用: id=%d err=%v", revokedID, err)
	}
	if err := db.DB.Model(&models.AuthSession{}).Where("id = ?", revokedID).
		Update("revoked_at", time.Now()).Error; err != nil {
		t.Fatalf("吊销会话失败: %v", err)
	}
	revRows := firstPage("?sort=revoked&dir=desc&page_size=10")
	if id, _ := revRows[0]["user_id"].(float64); uint(id) != hot {
		t.Errorf("按「已吊销」降序第一条应是刚被吊销的 %d，实得 %v", hot, revRows[0]["user_id"])
	}
	if n, _ := revRows[0]["revoked_recent"].(float64); n != 1 {
		t.Errorf("刚吊销一条会话，revoked_recent 实得 %v——被 WHERE 筛掉就不叫「看到旧痕迹」了", n)
	}
	// 吊销只减活跃，不减设备/IP 的分布（7 个会话来自 7 个 IP，其中 1 个已吊销）
	if n, _ := revRows[0]["active_sessions"].(float64); n != 6 {
		t.Errorf("吊销一条后 active_sessions 应是 6，实得 %v", n)
	}
	if n, _ := revRows[0]["login_ips"].(float64); n < 7 {
		t.Errorf("login_ips 应仍含被吊销那条的来源 IP，实得 %v", n)
	}
}

func TestUserFilterPlanAndRole(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)

	var vip models.QuotaPlan
	if err := db.DB.Where("code = ?", "vip").First(&vip).Error; err != nil {
		t.Fatalf("读 vip 失败: %v", err)
	}
	a := mustUserRow(t, &vip.ID, "p_vip")
	b := mustUserRow(t, nil, "p_free")

	got := listUserIDs(t, srv, admin, fmt.Sprintf("?plan=%d&page_size=100", vip.ID))
	if !got[a.ID] || got[b.ID] {
		t.Errorf("plan=%d 命中错位：%+v", vip.ID, got)
	}

	// 「绑着的套餐已被软删」这一档：现网 0 例，但软删套餐后这批人不能凭空消失
	var orphan models.QuotaPlan
	if err := db.DB.Where("code = ?", "free").First(&orphan).Error; err != nil {
		t.Fatalf("读 free 失败: %v", err)
	}
	c := mustUserRow(t, &orphan.ID, "p_orphan")
	if g := listUserIDs(t, srv, admin, "?plan=unavailable&page_size=100"); g[c.ID] {
		t.Errorf("套餐还在世就被 plan=unavailable 命中，语义应是「绑的套餐已不可见」")
	}
	db.DB.Unscoped().Delete(&models.QuotaPlan{}, orphan.ID)
	if g := listUserIDs(t, srv, admin, "?plan=unavailable&page_size=100"); !g[c.ID] {
		t.Errorf("套餐软删后 plan=unavailable 没含仍绑着它的 %d（这批人会在筛选里凭空消失）", c.ID)
	}

	// 角色筛选（单角色模型，等值）
	if st, env := doJSON(t, srv, http.MethodPost, "/admin/users/"+fmt.Sprint(c.ID)+"/roles",
		map[string]interface{}{"role_code": "vip"}, authHeader(admin)); st != http.StatusOK || env.Code != 0 {
		t.Fatalf("设角色失败: %d %s", st, env.Msg)
	}
	if g := listUserIDs(t, srv, admin, "?role=vip&page_size=100"); !g[c.ID] {
		t.Errorf("role=vip 没含刚设成 vip 角色的 %d", c.ID)
	}
	if g := listUserIDs(t, srv, admin, "?role=nosuchrole&page_size=100"); len(g) != 0 {
		t.Errorf("不存在的角色码应筛出空集，实得 %d 行", len(g))
	}
}
