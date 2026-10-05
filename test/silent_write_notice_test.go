package test

// 第二十九遍巡检（判法：「写库的结果没人看」）。扫出两类同形状缺陷：
//   ① 额度流水 quota_usage_logs 两处裸 `db.DB.Create(...)`——写失败等于这格用量今天没扣上，
//      而请求已放行、监控记的是成功，日志里一行都没有，症状要等到「额度怎么永远用不完」才显形；
//   ② PUT /user/source-configs 不看结果就回「已更新」——同族的写入口（生成密钥/加黑名单/存别名）
//      都是失败就 500，它是唯一一个把用户的话当成真写完的。
//
// 手法沿用 P55/P56/P62：sqlite 的 BEFORE INSERT 触发器把写入钉死，
// 断言三件事——**出声**、**出声不泄密**、**修完不许把好写的路也判死**（撤掉触发器后必须落库）。

import (
	"bytes"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"loomproxy/db"
	"loomproxy/gate"
	"loomproxy/handlers/verify"
	"loomproxy/models"
)

// blockInsert 钉死某张表的插入，返回撤除函数。
// 表名走白名单而不是随手拼接：这个助手里没有参数校验位，将来传错只会报一句晦涩的建触发器失败。
func blockInsert(t *testing.T, table string) func() {
	t.Helper()
	switch table {
	case "user_source_configs", "quota_usage_logs", "users":
	default:
		t.Fatalf("blockInsert 不认识这张表 %q（要加就去白名单里加）", table)
	}
	name := "probe_no_insert_" + table
	if err := db.DB.Exec("CREATE TRIGGER " + name + " BEFORE INSERT ON " + table +
		" BEGIN SELECT RAISE(ABORT, 'probe: 插入被挡'); END").Error; err != nil {
		t.Fatalf("建触发器挡住 %s 的插入失败: %v", table, err)
	}
	return func() {
		if e := db.DB.Exec("DROP TRIGGER IF EXISTS " + name).Error; e != nil {
			t.Errorf("撤触发器失败: %v", e)
		}
	}
}

func captureLogText(t *testing.T) func() string {
	t.Helper()
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })
	return func() string { return buf.String() }
}

func countRows(t *testing.T, where string, arg interface{}) int64 {
	t.Helper()
	var n int64
	if err := db.DB.Model(&models.UserSourceConfig{}).Where(where, arg).Count(&n).Error; err != nil {
		t.Fatalf("回读 user_source_configs 失败: %v", err)
	}
	return n
}

// TestSourceConfigWriteFailureSaysSo 用户配置没写进去时，不能回「已更新」。
func TestSourceConfigWriteFailureSaysSo(t *testing.T) {
	srv := newTestServer(t)
	token := registerUser(t, srv, "cfg-write-fail", "cfg-write-fail@unit.test", "Passw0rd!")

	const url = "https://cfg-probe.test"
	text := captureLogText(t)
	undo := blockInsert(t, "user_source_configs")

	status, env := doJSON(t, srv, http.MethodPut, "/user/source-configs", map[string]interface{}{
		"configs": []map[string]string{{"source_name": "fake_a", "base_url": url}},
	}, authHeader(token))
	if status != http.StatusInternalServerError {
		t.Errorf("写库失败时 status = %d（msg=%q）, want 500——回 200 等于告诉用户地址改好了", status, env.Msg)
	}
	if env.Msg != "保存失败" {
		t.Errorf("对用户的 msg = %q, want 原句「保存失败」（对外文案不随这次改动变）", env.Msg)
	}
	for _, leak := range []string{"probe:", "INSERT", "TRIGGER", "SQLITE", "constraint", "GORM"} {
		if strings.Contains(strings.ToUpper(env.Msg), strings.ToUpper(leak)) {
			t.Errorf("对用户的 msg 带了数据库细节 %q：%q", leak, env.Msg)
		}
	}
	if !strings.Contains(text(), "数据源配置写入失败") {
		t.Errorf("服务端没为这次写入失败出声（用户随后看到的仍是旧地址）：\n%s", text())
	}
	if !strings.Contains(text(), "fake_a") {
		t.Errorf("服务端那句没带上是哪个源：%s", text())
	}
	if n := countRows(t, "base_url = ?", url); n != 0 {
		t.Errorf("被挡住的写入仍有 %d 行落了库——前提不成立", n)
	}

	// 撤掉触发器后同一次请求必须成功：这条改动只许把「假的成功」变成「真的失败」，不许把好写的路判死
	undo()
	status, env = doJSON(t, srv, http.MethodPut, "/user/source-configs", map[string]interface{}{
		"configs": []map[string]string{{"source_name": "fake_a", "base_url": url}},
	}, authHeader(token))
	if status != http.StatusOK {
		t.Fatalf("正常写入被误判成失败（status=%d msg=%q）", status, env.Msg)
	}
	if n := countRows(t, "base_url = ?", url); n != 1 {
		t.Errorf("正常路径落了 %d 行, want 1", n)
	}

	// 清空（Delete 删 0 行）不是失败：这条必须测，否则「改完再也没法清空配置」要等用户来报
	status, env = doJSON(t, srv, http.MethodPut, "/user/source-configs", map[string]interface{}{
		"configs": []map[string]string{{"source_name": "fake_b", "base_url": ""}},
	}, authHeader(token))
	if status != http.StatusOK {
		t.Errorf("清空一个本来就没有配置的数据源被回成失败（status=%d msg=%q）, want 200", status, env.Msg)
	}
}

// TestQuotaLedgerWriteFailureSpeaks 额度流水写失败必须留在日志里——它是 UsedToday 的读对象。
func TestQuotaLedgerWriteFailureSpeaks(t *testing.T) {
	srv := newTestServer(t)
	registerUser(t, srv, "ledger-probe", "ledger-probe@unit.test", "Passw0rd!")

	var u models.User
	if err := db.DB.Where("username = ?", "ledger-probe").First(&u).Error; err != nil {
		t.Fatalf("准备用户失败: %v", err)
	}
	if u.ID == 0 {
		t.Fatal("用户的 id 是 0——DeductAggregateTarget 会当成匿名直接返回，用例什么都验不了")
	}

	text := captureLogText(t)
	undo := blockInsert(t, "quota_usage_logs")

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	gate.DeductAggregateTarget(c, &u, "fake_a", "search", 1)

	if !strings.Contains(text(), "额度流水写入失败") {
		t.Fatalf("额度流水没写上而日志一个字都没有——这正是本轮修掉的那两处静默：\n%s", text())
	}
	if !strings.Contains(text(), "fake_a") || !strings.Contains(text(), "search") {
		t.Errorf("那一句没带上源与动作，读日志的人对不上是哪笔扣减：%s", text())
	}
	if !strings.Contains(text(), "UsedToday") {
		t.Errorf("那一句没说明后果（这格用量没扣上、当日已用会偏小）：%s", text())
	}
	if n := countUsageRows(t, u.ID); n != 0 {
		t.Errorf("被挡住的流水仍有 %d 行, want 0——前提不成立", n)
	}

	undo()
	gate.DeductAggregateTarget(c, &u, "fake_a", "search", 1)
	if n := countUsageRows(t, u.ID); n != 1 {
		t.Errorf("撤掉触发器后落了 %d 行, want 1——共用的那个写入口正常路径没走通", n)
	}
}

func countUsageRows(t *testing.T, userID uint) int64 {
	t.Helper()
	var n int64
	if err := db.DB.Model(&models.QuotaUsageLog{}).Where("user_id = ?", userID).Count(&n).Error; err != nil {
		t.Fatalf("回读 quota_usage_logs 失败: %v", err)
	}
	return n
}

// blockUpdate 钉死某张表的更新（验证码那张计数器用的就是 UPDATE，插入触发器挡不住它）。
func blockUpdate(t *testing.T, table, name string) func() {
	t.Helper()
	if err := db.DB.Exec("CREATE TRIGGER " + name + " BEFORE UPDATE ON " + table +
		" BEGIN SELECT RAISE(ABORT, 'probe: 更新被挡'); END").Error; err != nil {
		t.Fatalf("建触发器挡住 %s 的更新失败: %v", table, err)
	}
	return func() {
		if e := db.DB.Exec("DROP TRIGGER IF EXISTS " + name).Error; e != nil {
			t.Errorf("撤触发器失败: %v", e)
		}
	}
}

// TestVerifyAttemptCounterFailureSpeaks 防爆破计数器写不进去时必须出声：
// 本次请求仍按内存值判定，但库里那一列不再前进，下一次读到的还是旧值——锁死不了任何人。
// **改不改语义（写不进就 fail-closed）是另一件事，已登成待拍**，这条用例只钉"不许静默"。
func TestVerifyAttemptCounterFailureSpeaks(t *testing.T) {
	newTestServer(t)
	enableVerifyScene(t, "register")

	const target = "attempts-probe@unit.test"
	if _, err := verify.Issue("127.0.0.9", "register", target); err != nil {
		t.Fatalf("发码失败: %v", err)
	}

	text := captureLogText(t)
	undo := blockUpdate(t, "verification_codes", "probe_no_update_verification_codes")
	defer undo()

	_, err := verify.Check("register", target, "000000")
	if !errors.Is(err, verify.ErrMismatch) {
		t.Fatalf("错码校验返回 %v, want ErrMismatch——这次改动不许动鉴权语义", err)
	}
	if !strings.Contains(text(), "验证码失败次数没记进库") {
		t.Errorf("计数器写失败而日志一个字没有（下次读到的仍是旧值，防爆破从此不再前进）：\n%s", text())
	}

	var row models.VerificationCode
	if e := db.DB.Where("target = ?", target).First(&row).Error; e != nil {
		t.Fatalf("回读验证码失败: %v", e)
	}
	if row.Attempts != 0 {
		t.Errorf("被挡住的更新仍写了库（attempts=%d）——前提不成立", row.Attempts)
	}

	undo()
	if _, err = verify.Check("register", target, "111111"); err == nil {
		t.Fatal("撤掉触发器后校验仍成功？")
	}
	if e := db.DB.Where("target = ?", target).First(&row).Error; e != nil {
		t.Fatalf("回读验证码失败: %v", e)
	}
	if row.Attempts != 1 {
		t.Errorf("正常路径下 attempts = %d, want 1——计数写入没走通", row.Attempts)
	}
}

// TestPlatformSourceConfigWriteFailureSaysSo 平台默认地址是同一形状的第二个写入口。
// 用户级额度覆盖（`PUT /admin/users/:id/quota`）是同形状第三处，这里不重复开用例，由扫描器兜。
func TestPlatformSourceConfigWriteFailureSaysSo(t *testing.T) {
	srv := newTestServer(t)
	admin := adminToken(t, srv)

	text := captureLogText(t)
	undo := blockInsertNamed(t, "platform_source_configs", "probe_no_insert_psc")
	defer undo()

	status, env := doJSON(t, srv, http.MethodPut, "/admin/source-configs", map[string]interface{}{
		"configs": []map[string]string{{"source_name": "fake_a", "base_url": "https://psc-probe.test"}},
	}, authHeader(admin))
	if status != http.StatusInternalServerError {
		t.Errorf("平台默认地址写失败时 status = %d（msg=%q）, want 500——它决定所有没自配地址的人打到哪个域", status, env.Msg)
	}
	if env.Msg != "保存失败" {
		t.Errorf("对管理员的 msg = %q, want 原句「保存失败」", env.Msg)
	}
	if !strings.Contains(text(), "平台默认地址写入失败") {
		t.Errorf("服务端没出声：\n%s", text())
	}
	undo()
	status, env = doJSON(t, srv, http.MethodPut, "/admin/source-configs", map[string]interface{}{
		"configs": []map[string]string{{"source_name": "fake_a", "base_url": "https://psc-probe.test"}},
	}, authHeader(admin))
	if status != http.StatusOK {
		t.Fatalf("正常写入被误判成失败（status=%d msg=%q）", status, env.Msg)
	}
}

// blockInsertNamed 与 blockInsert 同法，但触发器名由用例给（同一张表在同一次运行里要建两次时避免撞名）。
func blockInsertNamed(t *testing.T, table, name string) func() {
	t.Helper()
	if err := db.DB.Exec("CREATE TRIGGER " + name + " BEFORE INSERT ON " + table +
		" BEGIN SELECT RAISE(ABORT, 'probe: 插入被挡'); END").Error; err != nil {
		t.Fatalf("建触发器挡住 %s 的插入失败: %v", table, err)
	}
	return func() {
		if e := db.DB.Exec("DROP TRIGGER IF EXISTS " + name).Error; e != nil {
			t.Errorf("撤触发器失败: %v", e)
		}
	}
}
