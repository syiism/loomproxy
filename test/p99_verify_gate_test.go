package test

// 待办清单 P99：发码的三道闸（同目标冷却 / 同目标每日 / 同 IP 每日）都是**读** `verification_codes` 算出来的，
// 而改前三处读库都不取结果——于是「查询失败」和「这个目标没发过码」长成同一个值：0。
// 一次数据库抖动的后果不是报错，是**三道闸一起消失**：冷却没了、日上限也没了，
// 而发码走的是要花钱的通道（P40 那族防爆破防的正是这个）。更糟的是它一路走到 `sender.Send`
// 才在落库那步失败，事后看日志是"发出去了却查不到码"（那条注释自己承认过这种状态）。
//
// 现在读失败 = 拒绝发码 + 出声；读不到行 = 照旧放行（那是合法状态，不是失败）。
// 用例用两种"读失败"分别够到两道闸：先摘掉 `created_at` 那列（`First` 仍可跑、`Count` 的谓词报错），
// 再 `DROP TABLE`（连冷却那一次读也报错）。第一条是变异牙齿所在——只用 DROP TABLE，
// 冷却那道闸会先把请求拦下，两道 `Count` 的守卫一次都没被执行过（断言空转）。

import (
	"bytes"
	"errors"
	"log"
	"regexp"
	"strings"
	"testing"

	"loomproxy/db"
	"loomproxy/handlers/verify"
)

// idxIdent 是标识符形状白名单：DDL 里的索引名不能走占位符，所以先验形状再拼。
var idxIdent = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

func TestVerifyIssueFailsClosedWhenGateUnreadable(t *testing.T) {
	newTestServer(t)
	writeSettingValue(t, "verify_code_scenes", "register")
	writeSettingValue(t, "verify_provider", "mock")
	writeSettingValue(t, "verify_send_interval_sec", "60")
	writeSettingValue(t, "verify_daily_send_limit", "10")

	var buf bytes.Buffer
	oldOut := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(oldOut) })
	sent := func() int { return strings.Count(buf.String(), "VERIFY [mock]") }

	// 1) 基线：表在的时候正常发码（这条同时给"没发码"那半句一个可对照的分母）
	before := sent()
	if _, err := verify.Issue("10.0.0.1", verify.SceneRegister, "gate-ok@example.com"); err != nil {
		t.Fatalf("正常路径发码失败: %v（日志=\n%s）", err, buf.String())
	}
	if n := sent() - before; n != 1 {
		t.Fatalf("基线没走到发码通道（mock 行 %d 条, want 1），后面的「没发码」就没有对照组", n)
	}

	// 2) **只弄坏计数那两道闸**：`First`（冷却）走的还是原表，而 `Count` 的谓词里那列没了。
	//    这一条是变异牙齿所在：只用 `DROP TABLE` 的话，第一道闸就先把请求拦下了，
	//    两道 `Count` 的守卫**一次都没被执行过**（第一轮变异正是没红才暴露这点——断言空转）。
	// 索引里带着 created_at 的那些会挡住 DROP COLUMN，而索引名来自模型 tag（会变），所以按 sqlite_master 现查现摘。
	// 名字是数据库自己给的标识符，只能拼接（DDL 不接受占位符）——这是用例、不是入口，值不来自外部。
	var idxNames []string
	if err := db.DB.Raw("SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = 'verification_codes' AND sql LIKE '%created_at%'").
		Scan(&idxNames).Error; err != nil {
		t.Fatalf("查覆盖 created_at 的索引失败: %v", err)
	}
	for _, n := range idxNames {
		if !idxIdent.MatchString(n) {
			t.Fatalf("索引名不合标识符形状，拒绝拼接执行: %q", n)
		}
		if err := db.DB.Exec("DROP INDEX IF EXISTS " + n).Error; err != nil {
			t.Fatalf("摘索引 %s 失败: %v", n, err)
		}
	}
	if len(idxNames) == 0 {
		t.Log("注意：这张表上没有覆盖 created_at 的索引，本用例少了一步前置（不影响断言）")
	}
	if err := db.DB.Exec("ALTER TABLE verification_codes DROP COLUMN created_at").Error; err != nil {
		t.Fatalf("去掉 created_at 列失败（SQLite 需 3.35+，且索引得先摘）: %v", err)
	}
	before = sent()
	_, err := verify.Issue("10.0.0.2", verify.SceneRegister, "gate-count@example.com")
	if !errors.Is(err, verify.ErrStorageUnavailable) {
		t.Fatalf("日上限计数读不动时应回 ErrStorageUnavailable，实得 %v", err)
	}
	if n := sent() - before; n != 0 {
		t.Errorf("计数闸门读不动却还是把码发出去了 %d 条, want 0——改前正是这个形状：钱花了、库里没行", n)
	}
	if msg := err.Error(); strings.Contains(msg, "no such column") || strings.Contains(msg, "verification_codes") {
		t.Errorf("对外的句子带了驱动原文/表名（P90 那条判据）：%q", msg)
	}
	line := buf.String()
	if !strings.Contains(line, "拒绝发码（失败关闭）") {
		t.Errorf("服务端没出声（失败关闭必须看得见，否则下一个人只会看到「没人发码」）：\n%s", line)
	}
	if strings.Contains(line, "gate-count@example.com") {
		t.Errorf("出声里带了收件目标（§11：日志里不留邮箱/手机号，P67 同判据）")
	}

	// 3) 整张表没了：第一道闸（冷却那一次 `First`）也读不动，同样失败关闭
	if err := db.DB.Exec("DROP TABLE verification_codes").Error; err != nil {
		t.Fatalf("DROP 表失败: %v", err)
	}
	before = sent()
	if _, err := verify.Issue("10.0.0.3", verify.SceneRegister, "gate-none@example.com"); !errors.Is(err, verify.ErrStorageUnavailable) {
		t.Fatalf("冷却查询失败时应回 ErrStorageUnavailable，实得 %v", err)
	}
	if n := sent() - before; n != 0 {
		t.Errorf("冷却读不动却还是发出了 %d 条, want 0", n)
	}
	// 出声要指明**是哪一道闸**：下面两条 Count 的守卫会在同一个形状下兜住返回值，
	// 所以只有这句能把"冷却闸读不动"与"日上限闸读不动"分开——运维查的是后者不是前者。
	if !strings.Contains(buf.String(), "验证码冷却查询失败") {
		t.Errorf("冷却闸的失败没被单独说出（日志=\n%s）", buf.String())
	}
}
