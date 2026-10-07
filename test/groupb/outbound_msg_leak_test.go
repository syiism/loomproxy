package groupb

// 待办清单 P90：`POST /auth/register` 是**匿名可达**的端点，而它过去把写库失败的原话拼进响应：
//
//	fail(c, 500, "注册失败: "+err.Error())
//
// 驱动原文里会躺着 `Duplicate entry 'someone@example.com' for key 'users.email'`
// 或 `Data too long for column 'nickname'`——前者替人做账号枚举（而且正是"预检与插入之间"那个窗口
// 最可能撞上的那条），后者把列名与表形状交出去。同一个函数上面两处重复检查发的都是固定句
// （"邮箱已注册"/"该邮箱已被注销账号占用"），所以这条不是风格之争，是同函数里漏网的一处。

import (
	"net/http"
	"strings"
	"testing"
)

func TestRegisterFailureDoesNotLeakDriverText(t *testing.T) {
	srv := newTestServer(t)

	text := captureLogText(t)
	undo := blockInsert(t, "users")

	body := map[string]string{"username": "leak-probe", "email": "leak-probe@unit.test", "password": "Passw0rd1"}
	status, env := doJSON(t, srv, http.MethodPost, "/auth/register", body, nil)
	if status != http.StatusInternalServerError {
		t.Fatalf("写库被挡时 status = %d（msg=%q）, want 500", status, env.Msg)
	}
	if env.Msg != "注册失败，请稍后再试" {
		t.Errorf("对外的 msg = %q, want 固定句「注册失败，请稍后再试」", env.Msg)
	}
	// 触发了什么就查什么：这一句里不许出现驱动文案的任何一个特征片段
	for _, leak := range []string{"probe:", "INSERT", "TRIGGER", "SQLITE", "constraint", "column", "users", "Duplicate", "Email"} {
		if strings.Contains(strings.ToUpper(env.Msg), strings.ToUpper(leak)) {
			t.Errorf("对外的 msg 里带了内部细节 %q：%q（匿名端点等于替人枚举账号）", leak, env.Msg)
		}
	}
	if !strings.Contains(text(), "注册写库失败") {
		t.Errorf("服务端没把真因留下来（管理员要看得到是哪一类失败）：\n%s", text())
	}

	// 撤掉触发器后同一次注册必须成功：这条修复只许改"说什么"，不许把注册本身判死
	undo()
	status, env = doJSON(t, srv, http.MethodPost, "/auth/register", body, nil)
	if status != http.StatusOK {
		t.Fatalf("正常注册被误判成失败（status=%d msg=%q）", status, env.Msg)
	}
}
