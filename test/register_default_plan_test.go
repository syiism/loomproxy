package test

// 待办清单 P53：面板「新用户默认套餐」不能是一个没人读的装饰位。
//
// 改前的形状最坏：`handlers/auth/auth.go` 注册时硬编码查 `code = "free"`，
// 而面板把这个键渲染成可编辑文本框、后端还把它列进 `protectedSettingKeys`（不许删）。
// 一个"不许删、能编辑、但没人读"的配置，比一个显眼的未实现功能更难发现——
// 管理员填 vip 之后没有任何地方说"这不算"。现在它真的被读，且填错码时回落到 free 并出声。
//
// 回落方向刻意是 free 而不是留一行 NULL：读取侧 `gate.ResolvePlan` 虽然会把无套餐的人算成 free，
// 但那是"当场解释"；而一旦 `free` 这一档被删掉（P34 之后删套餐行是常规操作），
// `planID=0` 在授权判据上是**失败关闭**——那个人进得来、什么都不能用，而库里看不出为什么。

import (
	"bytes"
	"log"
	"loomproxy/db"
	"loomproxy/models"
	"loomproxy/testkit"
	"net/http/httptest"
	"strings"
	"testing"
)

func planIDOfPlanCode(t *testing.T, code string) uint {
	t.Helper()
	var p models.QuotaPlan
	if err := db.DB.Where("code = ?", code).First(&p).Error; err != nil {
		t.Fatalf("测试夹具缺套餐 %q: %v", code, err)
	}
	return p.ID
}

func registeredPlanID(t *testing.T, srv *httptest.Server, username string) *uint {
	t.Helper()
	registerUser(t, srv, username, username+"@example.com", "pass1234")
	var u models.User
	if err := db.DB.Where("username = ?", username).First(&u).Error; err != nil {
		t.Fatalf("查注册出来的用户 %s 失败: %v", username, err)
	}
	return u.PlanID
}

func writeSettingValue(t *testing.T, key, value string) {
	t.Helper()
	testkit.WriteSettingValue(t, key, value)
}

func TestRegisterUsesDefaultQuotaPlanSetting(t *testing.T) {
	srv := newTestServer(t)
	freeID, vipID := planIDOfPlanCode(t, "free"), planIDOfPlanCode(t, "vip")

	var buf bytes.Buffer
	oldOut := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(oldOut) })
	spoken := func() int { return strings.Count(buf.String(), "default_quota_plan 填的值没生效") }

	// 1) 默认值就是 free：注册结果与硬编码时代**逐字相同**（这条是回归护栏，不是新行为）
	writeSettingValue(t, "default_quota_plan", "free")
	before := spoken()
	if got := registeredPlanID(t, srv, "p53_default"); got == nil || *got != freeID {
		t.Fatalf("默认配置下注册的套餐 id = %v, want free(%d)——现网这一行就是 free，所以整条改动在生产上是 no-op", got, freeID)
	}
	if n := spoken() - before; n != 0 {
		t.Errorf("合法值不该出声，实得 %d 条", n)
	}

	// 2) 面板上填 vip：新人是 vip。**改前这一条必红**（硬编码只看 free），所以它就是这条用例的牙齿
	writeSettingValue(t, "default_quota_plan", "vip")
	if got := registeredPlanID(t, srv, "p53_vip"); got == nil || *got != vipID {
		t.Fatalf("default_quota_plan=vip 时注册的套餐 id = %v, want vip(%d)", got, vipID)
	}

	// 3) 填了一个不存在的码：回落 free，并且说一声
	writeSettingValue(t, "default_quota_plan", "vip-copy")
	before = spoken()
	if got := registeredPlanID(t, srv, "p53_typo"); got == nil || *got != freeID {
		t.Fatalf("非法套餐码回落后的套餐 id = %v, want free(%d)", got, freeID)
	}
	if n := spoken() - before; n != 1 {
		t.Errorf("非法套餐码的替换出声 %d 条, want 1", n)
	}
	if line := buf.String(); !strings.Contains(line, `"vip-copy"`) || !strings.Contains(line, "套餐码不存在") {
		t.Errorf("出声内容没把话说清（要含填的值与原因）：\n%s", line)
	}

	// 4) 同一种替换重复发生不再喊（注册不是每秒几百次的热路径，但一天几百次也够泡坏日志）
	before = spoken()
	if got := registeredPlanID(t, srv, "p53_typo2"); got == nil || *got != freeID {
		t.Fatalf("第二次非法套餐码回落后的套餐 id = %v, want free(%d)", got, freeID)
	}
	if n := spoken() - before; n != 0 {
		t.Errorf("同一种替换重复出声 %d 条, want 0", n)
	}

	// 5) free 这一档本身不在了（管理员把默认套餐删了）：仍然给一个空套餐而不是让注册失败——
	//    与改前一致，但这一档必须出声，因为「进得来、什么都不能用」是最难被发现的一种坏
	if err := db.DB.Where("code = ?", "free").Delete(&models.QuotaPlan{}).Error; err != nil {
		t.Fatalf("删掉 free 套餐失败: %v", err)
	}
	writeSettingValue(t, "default_quota_plan", "vip-copy")
	before = spoken()
	if got := registeredPlanID(t, srv, "p53_noplan"); got != nil {
		t.Errorf("free 不存在时注册的套餐 id = %v, want 空（改前也是空，这条钉的是「没变得更糟」）", *got)
	}
	if n := spoken() - before; n != 1 {
		t.Errorf("「回落目标也没了」的替换出声 %d 条, want 1（静默给一个无套餐账号是最坏形态）", n)
	}
}
