package test

// 待办清单 P61：上限类设置的读取原来有**三份**，三份的静默面各不相同。
//
// 本用例钉的是另外两条通路（`handlers/auth` 那条由 `setting_fallback_notice_test.go` 钉）：
// 收口如果只收名字、用例还挂在原来那一族上，那两份静默的照样没人管——所以断言必须走在
// verify 与 ipblock 自己的通路上。
//
// 钉三件事：
//  1. **带首尾空白的合法值现在按填的走**（改前 verify 那份手写解析把它当非法、ipblock 那份 `Atoi` 也拒，
//     两处都静默回默认——"明明填对了却不生效"最难查的那种）；
//  2. **值没生效要出声**，且同一键的几种成因各自算一次事件；
//  3. **超长数字串不再溢出**成没人认识的数（改前手写的 `n*10+digit` 会 wrap 出一个任意值并当真使用）。

import (
	"bytes"
	"log"
	"net/http"
	"strings"
	"testing"
	"time"

	"loomproxy/db"
	"loomproxy/middleware/ipblock"
	"loomproxy/models"
)

const (
	settingFamilyTTLKey  = "verify_code_ttl_sec"
	settingFamilyTTLDef  = 600
	settingFamilyThrKey  = "auto_block_threshold"
	settingFamilyThrDef  = 30
	settingFamilyWinKey  = "auto_block_window_sec"
	settingFamilyEnabKey = "auto_block_enabled"
)

// putFamilySetting 直接写库。**刻意不走管理端**：P60 之后 number 型的坏值在写入端就被 400 拒了，
// 而这条要治的形态正是"库里已经躺着那一行"（人手改的、脚本写的、比本仓更旧的升级残留）。
func putFamilySetting(t *testing.T, key, value string) {
	t.Helper()
	res := db.DB.Model(&models.SystemSetting{}).
		Where(map[string]interface{}{"key": key}).Update("value", value)
	if res.Error != nil {
		t.Fatalf("改 %s 失败: %v", key, res.Error)
	}
	if res.RowsAffected == 0 {
		if err := db.DB.Create(&models.SystemSetting{Key: key, Value: value, Type: "number"}).Error; err != nil {
			t.Fatalf("补 %s 失败: %v", key, err)
		}
	}
	db.InvalidateSettingCache(key)
}

func TestSettingIntFamilySpeaks(t *testing.T) {
	srv := newTestServer(t)

	// 收尾把动过的键还原成 seed 默认值：这几条通路是**进程内全局**，
	// 留给后面的用例一个"阈值 30 但开关还开着"的库，比失败更难查
	t.Cleanup(func() {
		for k, v := range map[string]string{
			settingFamilyTTLKey:  "600",
			settingFamilyThrKey:  "30",
			settingFamilyWinKey:  "60",
			settingFamilyEnabKey: "false",
		} {
			putFamilySetting(t, k, v)
		}
		db.DB.Where("source = ?", "auto").Delete(&models.BlockedIP{})
	})

	var buf bytes.Buffer
	oldOut := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(oldOut) })

	spoken := func(key string) int { return strings.Count(buf.String(), key+" 填的值没生效") }

	// ---- A) verify 通路：GET /verify/config 把 ttl 原样回显出来，是这一族唯一能黑盒读到的生效值 ----
	configTTL := func() int {
		t.Helper()
		_, env := doJSON(t, srv, http.MethodGet, "/verify/config", nil, nil)
		n, ok := env.dataMap(t)["ttl"].(float64)
		if !ok {
			t.Fatalf("/verify/config 的 ttl 不是数字: %#v", env.dataMap(t)["ttl"])
		}
		return int(n)
	}

	// A1 带空白的合法值：按填的走，且不吭声
	putFamilySetting(t, settingFamilyTTLKey, " 900 ")
	before := spoken(settingFamilyTTLKey)
	if got := configTTL(); got != 900 {
		t.Errorf("填 \" 900 \" 的生效 ttl = %d, want 900（回 %d 说明首尾空白仍被当非法而静默取默认）", got, settingFamilyTTLDef)
	}
	if n := spoken(settingFamilyTTLKey) - before; n != 0 {
		t.Errorf("填 \" 900 \" 出声 %d 条, want 0（生效了还喊就是造噪声）", n)
	}

	// A2 填 0：仍按默认（0 的语义是 P48② 没拍的事），但必须说一声
	putFamilySetting(t, settingFamilyTTLKey, "0")
	before = spoken(settingFamilyTTLKey)
	if got := configTTL(); got != settingFamilyTTLDef {
		t.Errorf("填 0 的生效 ttl = %d, want %d（语义不该被这条用例改动）", got, settingFamilyTTLDef)
	}
	if n := spoken(settingFamilyTTLKey) - before; n != 1 {
		t.Errorf("填 0 出声 %d 条, want 1（0 就是原来那份静默替换）", n)
	}

	// A3 超长数字串：按默认 + 出声。改前手写解析会 `n*10+digit` 溢出成一个任意值并被当真使用，
	// 症状是"有效期变成几十亿秒"而库里那一行看起来只是个笔误
	putFamilySetting(t, settingFamilyTTLKey, "99999999999999999999999")
	before = spoken(settingFamilyTTLKey)
	if got := configTTL(); got != settingFamilyTTLDef {
		t.Errorf("填超长数字串的生效 ttl = %d, want %d（溢出值被采纳了）", got, settingFamilyTTLDef)
	}
	if n := spoken(settingFamilyTTLKey) - before; n != 1 {
		t.Errorf("填超长数字串出声 %d 条, want 1", n)
	}

	// ---- B) ipblock 通路：RecordFailure 每次都读阈值与窗口，生效值体现为"第几次拉黑" ----
	setFamilyBlocked := func(ip string) bool {
		t.Helper()
		var count int64
		if err := db.DB.Model(&models.BlockedIP{}).Where("ip = ?", ip).Count(&count).Error; err != nil {
			t.Fatalf("查 %s 拉黑状态失败: %v", ip, err)
		}
		return count > 0
	}
	putFamilySetting(t, settingFamilyEnabKey, "true")
	putFamilySetting(t, settingFamilyWinKey, "60")

	// B1 阈值填 " 5 "：第 5 次 403 就该拉黑。改前 `Atoi(" 5 ")` 报错 → 悄悄按 30，
	// 于是"我把阈值调到 5 了"这件事在现网一个都不生效，而且没有任何地方说
	putFamilySetting(t, settingFamilyThrKey, " 5 ")
	before = spoken(settingFamilyThrKey)
	ipA := "203.0.113.61"
	for i := 0; i < 4; i++ {
		ipblock.RecordFailure(ipA, http.StatusForbidden)
	}
	if setFamilyBlocked(ipA) {
		t.Errorf("阈值 5 时第 4 次就拉黑了（说明空白没被去掉、阈值取成了别的数？）")
	}
	ipblock.RecordFailure(ipA, http.StatusTooManyRequests)
	if !setFamilyBlocked(ipA) {
		t.Errorf("阈值填 \" 5 \"，5 次 403/429 后没拉黑——带空白的合法值仍被当非法（回默认 30）")
	}
	if n := spoken(settingFamilyThrKey) - before; n != 0 {
		t.Errorf("阈值填 \" 5 \" 出声 %d 条, want 0（它生效了）", n)
	}

	// B2 阈值填汉字：按默认 30 走 + 出声。用新 IP，免得 B1 的累计与拉黑记录混进来
	putFamilySetting(t, settingFamilyThrKey, "两个")
	before = spoken(settingFamilyThrKey)
	ipB := "203.0.113.62"
	for i := 0; i < 6; i++ {
		ipblock.RecordFailure(ipB, http.StatusForbidden)
	}
	if setFamilyBlocked(ipB) {
		t.Errorf("坏阈值下 6 次就拉黑，说明默认值 30 没生效（阈值被读成了别的数）")
	}
	if n := spoken(settingFamilyThrKey) - before; n != 1 {
		t.Errorf("阈值填「两个」出声 %d 条, want 1（这一族原来完全静默）", n)
	}

	// B3 重复读：同一种替换只喊一次（这两条通路都在请求热路径上）
	before = spoken(settingFamilyThrKey)
	ipC := "203.0.113.63"
	for i := 0; i < 3; i++ {
		ipblock.RecordFailure(ipC, http.StatusForbidden)
	}
	if n := spoken(settingFamilyThrKey) - before; n != 0 {
		t.Errorf("同一种坏值重复读又出声 %d 条, want 0（去重没做成，会把读数泡坏）", n)
	}

	// 前提复核：窗口键本身没被这条用例搞坏（它若静默回默认，上面那组"第 5 次拉黑"就不可信）
	if d := time.Duration(db.SettingInt(settingFamilyWinKey, 60)) * time.Second; d != 60*time.Second {
		t.Errorf("窗口生效值 = %v, want 1m0s（坏掉的话上面那组「第 5 次拉黑」不是这条通路测出来的）", d)
	}
}
