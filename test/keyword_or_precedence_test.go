package test

// 第二十二遍巡检（判法：「这条布尔表达式的结合顺序是谁的」）。
//
// 这条钉的是管理面关键词与别的筛选条件**组合**时的语义：`?keyword=…&status=normal` 里，
// 被 status 排除掉的账号绝不能因为命中关键词回来。
//
// **它不是缺陷守卫，而是防回归的钉子**：第二十二遍巡检查这一点时，GORM 实测会把每个
// Where 的裸表达式包一层括号（拼出 `WHERE (username LIKE ? OR email LIKE ?) AND users.status = ?`），
// 所以裸写 OR 今天是安全的；`likeESCAPEGroup` 的括号是显式化，不是修 bug。
// 用例故意**不断言生成的 SQL**——那会把用例绑在库版本的拼接细节上；它只问行为。
//
// 这一遍我自己还栽过一次，值得写在下面那段 fixture 注释里：`users.status` 带 `gorm:"default:1"`，
// Create 时零值被跳过，所以"造一个禁用账号"根本没落库，于是我一度把正确行为读成了缺陷。

import (
	"testing"

	"loomproxy/db"
	"loomproxy/models"
)

func mustRawUser(t *testing.T, u models.User) models.User {
	t.Helper()
	if err := db.DB.Create(&u).Error; err != nil {
		t.Fatalf("建用户 %s 失败: %v", u.Username, err)
	}
	return u
}

func TestKeywordOrGroupStaysInsideOtherFilters(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)

	// 命中位置刻意分开放：
	//   byEmail —— 只有 email 含关键词，状态正常（两个条件下都该出现）
	//   byNick  —— 只有 nickname 含关键词，状态正常（同上）
	//   disabledByUser —— username 含关键词但状态=禁用（**只该被关键词命中，绝不该出现在结果里**）
	//   deletedByUser   —— username 含关键词但已软删（同上）
	const kw = "ztestkw"
	byEmail := mustRawUser(t, models.User{Username: "plain-a", Email: kw + "-a@example.com", Nickname: "a", Status: 1})
	byNick := mustRawUser(t, models.User{Username: "plain-b", Email: "b@example.com", Nickname: kw + "-c", Status: 1})
	disabled := mustRawUser(t, models.User{Username: kw + "-d", Email: "d@example.com", Nickname: "d", Status: 1})

	// 软删那一条要走框架自己的删除口，否则形态不像（deleted_at 手工写也行，这里用它）
	if err := db.DB.Delete(&models.User{}, disabled.ID).Error; err != nil {
		t.Fatalf("软删失败: %v", err)
	}
	// 禁用与软删是两个维度，这里要的是"两种都被排除"的样本，所以另建一个只禁用不删的
	// `users.status` 带 `gorm:"default:1"`——Create 会**跳过零值**，所以 Status:0 根本写不进去
	// （这正是待办清单 P66 那条老坑，我自己这次又踩了一次）：改成建完显式更新，并把值读回来确认样本成立。
	justDisabled := mustRawUser(t, models.User{Username: kw + "-e", Email: "e@example.com", Nickname: "e"})
	if err := db.DB.Model(&models.User{}).Where("id = ?", justDisabled.ID).Update("status", 0).Error; err != nil {
		t.Fatalf("把 %s 置禁用失败: %v", justDisabled.Username, err)
	}
	var back int
	if err := db.DB.Model(&models.User{}).Where("id = ?", justDisabled.ID).Select("status").Row().Scan(&back); err != nil || back != 0 {
		t.Fatalf("禁用样本没落库（status=%d, want 0, err=%v）——样本不成立，下面的排除断言就是空转", back, err)
	}

	normal := listUserIDs(t, srv, admin, "?keyword="+kw+"&status=normal")
	for _, u := range []models.User{byEmail, byNick} {
		if !normal[u.ID] {
			t.Errorf("正常且命中关键词的 %s 没出现在 status=normal 的结果里——前提不成立", u.Username)
		}
	}
	for _, u := range []models.User{justDisabled} {
		if normal[u.ID] {
			t.Errorf("禁用的 %s 带着 status=normal 回来了：关键词那组 OR 逃出了括号，别的筛选被绕过\n  结果集=%v", u.Username, keysOf(normal))
		}
	}

	// 反向自检：如果整条路由坏了，上面四条"没回来"可能什么都证明不了
	all := listUserIDs(t, srv, admin, "?keyword="+kw)
	if !all[byEmail.ID] || !all[byNick.ID] || !all[justDisabled.ID] {
		t.Fatalf("不带 status 时关键词本身就没搜全（%v）——上面的排除断言是空转", keysOf(all))
	}

	// 软删的那台：默认作用域就该挡掉，带关键词也一样
	if all[disabled.ID] {
		t.Errorf("已软删的 %s 被关键词搜出来了（默认作用域没能挡住 OR 组合）", disabled.Username)
	}
}

func keysOf(m map[uint]bool) []uint {
	out := make([]uint, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
