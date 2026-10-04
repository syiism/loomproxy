package test

// 待办清单 P67：发码失败在服务端必须留得下归因，而给用户的那句仍然不能泄。
//
// 修前的形状是"两头都不说"：`Issue()` 把上游错误整个丢掉，只回一句固定文案——
// 对用户是对的（不该看见上游状态码与响应体），但**运维也无法回答"为什么发不出去"**：
// 是连不上、被平台拒了、还是 200 却没含成功关键字，三种在库里长得一模一样（没有行、没有日志）。
// 这条与 P27「连不上库就拒绝启动，不要在协程里 panic」是同一条纪律的另一面：
// **失败要出声，但出声的位置是服务端日志，不是下游响应**。

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"loomproxy/db"
	"loomproxy/models"
	"loomproxy/utils"
)

const (
	failTarget  = "send-fail-probe@unit.test"
	upstreamTag = "UPSTREAM-INTERNAL-DETAIL"
)

func TestVerifySendFailureSpeaksServerSide(t *testing.T) {
	srv := newTestServer(t)
	enableVerifyScene(t, "register")

	// 一个只会 500 并回吐一段内部细节的本地"发码平台"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(upstreamTag + ": token=fake-not-a-real-secret"))
	}))
	defer up.Close()

	setVerifySetting(t, "verify_provider", "http")
	setVerifySetting(t, "verify_http_url", up.URL)
	setVerifySetting(t, "verify_http_method", "POST")
	setVerifySetting(t, "verify_http_body", "")
	setVerifySetting(t, "verify_http_headers", "")
	setVerifySetting(t, "verify_http_success_keyword", "")
	// 上面的写入都绕过了管理端校验，这里等一次缓存过期不如直接清
	db.InvalidateSettingCache("")

	var buf bytes.Buffer
	oldOut := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(oldOut) })

	status, env := sendCode(t, srv, "register", failTarget)
	if status != http.StatusBadRequest {
		t.Fatalf("发码通道失败时 status = %d, want 400（msg=%q）", status, env.Msg)
	}

	// 1) 对用户：固定文案，不带上游状态码、不带响应体、更不带回吐里的"密钥"
	if env.Msg != "验证码发送失败，请稍后再试" {
		t.Errorf("给用户的 msg = %q, want 固定文案（这一句变了就是对外契约变了）", env.Msg)
	}
	for _, leak := range []string{upstreamTag, "500", "Internal", "token="} {
		if strings.Contains(env.Msg, leak) {
			t.Errorf("给用户的 msg 里出现了上游内容 %q：%q", leak, env.Msg)
		}
	}

	// 2) 对服务端：这一声必须有，而且要能回答"是哪个通道、为什么"
	line := ""
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.Contains(l, "验证码发送失败（通道=") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatalf("服务端日志里没有发码失败的归因（这条修的就是「没人知道为什么发不出去」）；整份日志：\n%s", buf.String())
	}
	if !strings.Contains(line, "通道=http") {
		t.Errorf("归因里没说是哪个通道：%q", line)
	}
	if !strings.Contains(line, "500") {
		t.Errorf("归因里没有上游状态码，读的人仍然要猜：%q", line)
	}
	// 3) 但**收件地址不进日志**：这是运维事件，不是把用户邮箱抄进 journal 的理由
	if strings.Contains(line, failTarget) {
		t.Errorf("日志里带了收件目标（用户邮箱/手机号不该进 journal）：%q", line)
	}

	// 4) 失败不留行：验证码行只在发送成功之后才创建（否则"发不出去"也能攒出一堆死码）
	var n int64
	if err := db.DB.Model(&models.VerificationCode{}).
		Where("scene = ? AND target = ?", "register", failTarget).Count(&n).Error; err != nil {
		t.Fatalf("数验证码行失败: %v", err)
	}
	if n != 0 {
		t.Errorf("发码失败却留下 %d 行（应先发送成功再落库）", n)
	}

	// 样本自证：确实有一次失败经过了这条通路（否则上面那些断言是在数空气）
	if !strings.Contains(buf.String(), "验证码发送失败") {
		t.Fatalf("整份日志里没有任何失败痕迹——这条用例的通路没接上")
	}
}

// A) 通道已发出、但验证码没落库：这句话对用户不必改，服务端必须知道自己手里有一张"死码"。
//
// 用 sqlite 的 BEFORE INSERT 触发器把写入钉死（同 P55/P56 那两条的做法）。
// 断言的是两件事：**出声**，以及**出声不等于泄密**（响应里不许带 SQL/驱动的字样）。
func TestVerifyCodeWriteFailureSpeaks(t *testing.T) {
	srv := newTestServer(t)
	enableVerifyScene(t, "register")

	var buf bytes.Buffer
	oldOut := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(oldOut) })

	if err := db.DB.Exec(`CREATE TRIGGER probe_no_insert_verification_codes BEFORE INSERT ON verification_codes
		BEGIN SELECT RAISE(ABORT, 'probe: 插入被挡'); END`).Error; err != nil {
		t.Fatalf("建触发器失败: %v", err)
	}
	t.Cleanup(func() {
		if e := db.DB.Exec("DROP TRIGGER IF EXISTS probe_no_insert_verification_codes").Error; e != nil {
			t.Errorf("撤触发器失败: %v", e)
		}
	})

	status, env := sendCode(t, srv, "register", "write-fail-probe@unit.test")
	if status != http.StatusBadRequest {
		t.Fatalf("落库失败时 status = %d, want 400（msg=%q）", status, env.Msg)
	}
	if env.Msg != "验证码写入失败，请稍后再试" {
		t.Errorf("给用户的 msg = %q, want 原句（这条改动不许动对外文案）", env.Msg)
	}
	for _, leak := range []string{"probe:", "INSERT", "TRIGGER", "SQLITE", "constraint"} {
		if strings.Contains(strings.ToUpper(env.Msg), strings.ToUpper(leak)) {
			t.Errorf("给用户的 msg 里带了数据库细节 %q：%q", leak, env.Msg)
		}
	}
	if !strings.Contains(buf.String(), "验证码落库失败（通道已发出") {
		t.Fatalf("服务端没为这次落库失败出声（发出去的码将不可校验，这是运维事件）：\n%s", buf.String())
	}
}

// B) 「查不到密钥」与「查不动库」必须分得开——而回给调用者的话一字不变。
//
// 用改名把 `api_keys` 这张表临时藏起来（本地 sqlite 测试库，测完改回）：
// 这时候 GORM 给的是 `no such table`，**不是** ErrRecordNotFound，
// 走的正是原来与"不存在"混在一起的那条分支。
func TestApiKeyLookupFailureSpeaks(t *testing.T) {
	newTestServer(t)

	// 先确认"真的不存在"这一支**不出声**（否则下面那条"出了声"的断言分不清成因）
	if _, err := utils.LookupApiKeyIdentity("lp_definitely_not_in_db"); err == nil ||
		!strings.Contains(err.Error(), "API Key 不存在") {
		t.Fatalf("不存在的密钥应回「API Key 不存在」，实得 %v", err)
	}

	var buf bytes.Buffer
	oldOut := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(oldOut) })

	if _, err := utils.LookupApiKeyIdentity("lp_also_not_in_db"); err == nil {
		t.Fatalf("第二次查询本该仍然回「不存在」")
	}
	if n := strings.Count(buf.String(), "API Key 校验查询失败"); n != 0 {
		t.Fatalf("「查不到」不该被报成「查不动」（出声 %d 条, want 0）——那正是这条改动要修的东西", n)
	}

	if err := db.DB.Exec("ALTER TABLE api_keys RENAME TO probe_api_keys_gone").Error; err != nil {
		t.Fatalf("藏表失败: %v", err)
	}
	t.Cleanup(func() {
		if e := db.DB.Exec("ALTER TABLE probe_api_keys_gone RENAME TO api_keys").Error; e != nil {
			t.Errorf("还原 api_keys 表失败: %v", e)
		}
	})

	// 缓存只在**查到**时写入，所以换一把新密钥就会真的打到库上
	_, err := utils.LookupApiKeyIdentity("lp_table_is_gone")
	if err == nil || !strings.Contains(err.Error(), "API Key 不存在") {
		t.Errorf("库故障时回给调用者的话必须与「不存在」一字不差（不许把内部状态透出去），实得 %v", err)
	}
	if !strings.Contains(buf.String(), "API Key 校验查询失败") {
		t.Fatalf("库故障没有被区分出来：服务端日志里没有那一声\n%s", buf.String())
	}
	// 出声里要带着成因，读的人不必再猜是哪种失败
	if !strings.Contains(buf.String(), "no such table") {
		t.Errorf("日志里没带上游错误原文（只有「出事了」三个字等于没出声）：\n%s", buf.String())
	}
}
