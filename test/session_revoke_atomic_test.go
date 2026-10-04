package test

// 会话吊销的两条性质（待办清单 P56）。
//
// 会话行就是鉴权的执行点（`AuthRequired` → `db.ValidateSession` 每请求查 `revoked_at`），
// 所以"吊销没做成"不是一个小故障，而是**那个人还能继续用**。这一组用例钉两件事：
//   ① 语义：改密码要把别的设备一起作废（`AGENTS.md` §7 一直这么写，而这条最常用的路径没做）；
//   ② 诚实：吊销失败时不许回 200，也不许把"查不了"说成"会话不存在"，
//      并且写凭证与吊销在同一个事务里——回滚就是一起回滚。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"loomproxy/db"
	"loomproxy/models"
)

// blockSessionUpdates 用 sqlite 触发器把 auth_sessions 上的 UPDATE 钉死（吊销就是那条 UPDATE）。
// 返回一个可重复调用的撤销函数——用例里会"挡住→撤开→再验正常路径"，所以撤两次不能报错。
func blockSessionUpdates(t *testing.T) func() {
	t.Helper()
	if err := db.DB.Exec(`CREATE TRIGGER block_session_update BEFORE UPDATE ON auth_sessions
		BEGIN SELECT RAISE(ABORT, 'boom: 用例故意挡住会话吊销'); END`).Error; err != nil {
		t.Fatalf("建挡 UPDATE 的触发器失败: %v", err)
	}
	var dropped bool
	return func() {
		if dropped {
			return
		}
		dropped = true
		if err := db.DB.Exec(`DROP TRIGGER IF EXISTS block_session_update`).Error; err != nil {
			t.Errorf("撤触发器失败: %v", err)
		}
	}
}

func loginUserStatus(t *testing.T, srv *httptest.Server, identity, password string) int {
	t.Helper()
	st, _ := doJSON(t, srv, http.MethodPost, "/auth/login", map[string]interface{}{
		"username": identity,
		"password": password,
	}, nil)
	return st
}

// TestChangePasswordRevokesOtherDevices 改密必须把别的设备一起作废，当前设备保留。
func TestChangePasswordRevokesOtherDevices(t *testing.T) {
	srv := newTestServer(t)
	registerUser(t, srv, "pwdev", "pwdev@example.com", "passw0rd123")

	tokenA := loginUser(t, srv, "pwdev", "passw0rd123")
	tokenB := loginUser(t, srv, "pwdev", "passw0rd123")
	if tokenA == "" || tokenB == "" || tokenA == tokenB {
		t.Fatalf("两次登录没拿到两个不同 token（前提不成立，后面的断言全是空转）")
	}
	// 前提：两个 token 都还能用
	for _, tk := range []string{tokenA, tokenB} {
		if st, _ := doJSON(t, srv, http.MethodGet, "/auth/me", nil, authHeader(tk)); st != http.StatusOK {
			t.Fatalf("改密前 /auth/me status = %d, want 200——两个会话都该活着", st)
		}
	}

	if st, env := doJSON(t, srv, http.MethodPost, "/auth/password", map[string]interface{}{
		"old_password": "passw0rd123",
		"new_password": "newpass4567",
	}, authHeader(tokenA)); st != http.StatusOK || env.Code != 0 {
		t.Fatalf("改密应成功，实得 status=%d msg=%s", st, env.Msg)
	}

	if st, _ := doJSON(t, srv, http.MethodGet, "/auth/me", nil, authHeader(tokenB)); st != http.StatusUnauthorized {
		t.Errorf("改密后另一台设备 /auth/me status = %d, want 401——改密不作废别的设备，「我怀疑号被人用了」时最该起的作用就没起作用", st)
	}
	if st, _ := doJSON(t, srv, http.MethodGet, "/auth/me", nil, authHeader(tokenA)); st != http.StatusOK {
		t.Errorf("改密这台设备应保留登录，实得 %d, want 200", st)
	}
	if st := loginUserStatus(t, srv, "pwdev", "newpass4567"); st != http.StatusOK {
		t.Errorf("用新密码登录 status = %d, want 200", st)
	}
	if st := loginUserStatus(t, srv, "pwdev", "passw0rd123"); st != http.StatusUnauthorized {
		t.Errorf("用旧密码登录 status = %d, want 401", st)
	}
}

// TestRevokeFailuresRollBackAndSpeak 挡住吊销之后：各条路径都不许把失败说成成功。
func TestRevokeFailuresRollBackAndSpeak(t *testing.T) {
	srv := newTestServer(t)
	registerUser(t, srv, "pwrb", "pwrb@example.com", "passw0rd123")
	token := loginUser(t, srv, "pwrb", "passw0rd123")
	uid := userIDByName(t, "pwrb")
	admin := authHeader(adminToken(t, srv))

	release := blockSessionUpdates(t)
	defer release()

	// 1) 改密：吊销失败 → 500，且密码没被改（同一个事务，回滚成对）
	st, env := doJSON(t, srv, http.MethodPost, "/auth/password", map[string]interface{}{
		"old_password": "passw0rd123",
		"new_password": "newpass4567",
	}, authHeader(token))
	if st == http.StatusOK {
		t.Errorf("吊销被挡住时改密仍回成功（msg=%s）——写库与吊销分开提交才会出现「密码改了、旧设备还在」", env.Msg)
	} else if st != http.StatusInternalServerError {
		t.Errorf("改密 status = %d, want 500", st)
	}
	if got := loginUserStatus(t, srv, "pwrb", "passw0rd123"); got != http.StatusOK {
		t.Errorf("回滚后旧密码应仍可登录（密码没改），实得 %d", got)
	}
	if got := loginUserStatus(t, srv, "pwrb", "newpass4567"); got == http.StatusOK {
		t.Errorf("回滚后新密码不该能登录")
	}
	if got, _ := doJSON(t, srv, http.MethodGet, "/auth/me", nil, authHeader(token)); got != http.StatusOK {
		t.Errorf("回滚后原会话应仍然有效，实得 %d", got)
	}

	// 2) 单会话撤销：DB 报错不许说成「不存在」
	var sess models.AuthSession
	if err := db.DB.Where("user_id = ?", uid).First(&sess).Error; err != nil {
		t.Fatalf("取一条会话失败: %v", err)
	}
	st, env = doJSON(t, srv, http.MethodDelete, "/auth/sessions/"+itoa(sess.ID), nil, authHeader(token))
	if st == http.StatusNotFound || strings.Contains(env.Msg, "不存在") {
		t.Errorf("吊销报错被说成「不存在」：status=%d msg=%s——用户会以为那台设备已经退出了", st, env.Msg)
	}
	if st != http.StatusInternalServerError {
		t.Errorf("单会话撤销 status = %d, want 500", st)
	}

	// 3) 管理面禁用用户：吊销失败 → 不许成功，且 status 仍为 1
	st, env = doJSON(t, srv, http.MethodPatch, "/admin/users/"+itoa(uid),
		map[string]interface{}{"status": 0}, admin)
	if st == http.StatusOK {
		t.Errorf("禁用被挡住时仍回成功（msg=%s）——管理员看到「更新成功」而那人还在正常使用", env.Msg)
	}
	if got := userStatus(t, uid); got != 1 {
		t.Errorf("回滚后 users.status = %d, want 仍为 1", got)
	}

	// 4) 退出登录：不挡住用户交还设备（仍 200），但要把"服务端没作废成功"说出来
	st, env = doJSON(t, srv, http.MethodPost, "/auth/logout", map[string]interface{}{}, authHeader(token))
	if st != http.StatusOK || env.Code != 0 {
		t.Errorf("退出登录 status = %d, want 200（挡住它没意义：用户要的是把这台设备交出去）", st)
	}
	if notice, _ := env.dataMap(t)["notice"].(string); !strings.Contains(notice, "未能作废") {
		t.Errorf("退出登录没说清服务端没做成：data=%s", string(env.Data))
	}

	// 撤掉障碍：同一条路径必须正常做成（证明上面那些红不是路径本身坏了）
	release()
	if st, env = doJSON(t, srv, http.MethodPatch, "/admin/users/"+itoa(uid),
		map[string]interface{}{"status": 0}, admin); st != http.StatusOK || env.Code != 0 {
		t.Fatalf("障碍撤掉后禁用应成功，实得 status=%d msg=%s", st, env.Msg)
	}
	if got := userStatus(t, uid); got != 0 {
		t.Errorf("禁用未落库：status = %d, want 0", got)
	}
	if got, _ := doJSON(t, srv, http.MethodGet, "/auth/me", nil, authHeader(token)); got != http.StatusUnauthorized {
		t.Errorf("禁用成功后该用户的会话应失效，实得 %d, want 401", got)
	}
}

func userStatus(t *testing.T, uid uint) int {
	t.Helper()
	var u models.User
	if err := db.DB.First(&u, uid).Error; err != nil {
		t.Fatalf("查用户 %d 失败: %v", uid, err)
	}
	return u.Status
}
