package groupb

// 待办清单 P48：上限类设置填了不生效的值时，系统必须说话。
//
// 这条用例钉的是**两件事**，缺一件都算没过：
//  1. **语义没变**——`max_active_sessions=0` 今天仍然按默认 5 走。0 该表示"一台都不许多出来"
//     还是"不适用"是产品口径，维护者还没拍（P48 的待拍部分），所以这里断言的是"维持现状"，
//     而不是"0 被采纳"。哪天拍了，改这条断言就是那次改动的验收标准。
//  2. **替换不再静默**——日志里要有一条说"填的是 0、实际按 5 走"，而且**同一种替换只喊一次**
//     （读设置在登录与管理端读数的热路径上，每请求一条会把真正该看的读数泡坏）。

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"loomproxy/db"
	"loomproxy/handlers/auth"
	"loomproxy/models"
)

const noticeKey = "max_active_sessions"

// noticeDefault 是 auth.defaultMaxActiveSessions 的值（未导出，此处按 seed 与兜底共同的 5 断言）
const noticeDefault = 5

func writeMaxSessions(t *testing.T, value string) {
	t.Helper()
	res := db.DB.Model(&models.SystemSetting{}).
		Where(map[string]interface{}{"key": noticeKey}).Update("value", value)
	if res.Error != nil {
		t.Fatalf("改 %s 失败: %v", noticeKey, res.Error)
	}
	if res.RowsAffected == 0 { // 第 6 步把整行删了：补一行回来，否则这次写是空操作，读到的还是"没填"
		if err := db.DB.Create(&models.SystemSetting{Key: noticeKey, Value: value, Type: "number"}).Error; err != nil {
			t.Fatalf("补 %s 失败: %v", noticeKey, err)
		}
	}
	db.InvalidateSettingCache(noticeKey)
}

func TestSettingFallbackSpeaksOncePerChange(t *testing.T) {
	newTestServer(t)

	var buf bytes.Buffer
	oldOut := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(oldOut) })

	// 数的是**这个键**的出声：日志里同一时刻可能有别的设置键也在被读，混进来计数就会假红/假绿
	spoken := func() int { return strings.Count(buf.String(), noticeKey+" 填的值没生效") }
	// wantSpeak 断言"这一步新出几声"。写成增量而不是累计，是为了让每一步的因果只属于自己的输入
	wantSpeak := func(t *testing.T, step string, before, want int) {
		t.Helper()
		if got := spoken() - before; got != want {
			t.Errorf("%s：新增出声 %d 条, want %d（累计 %d）", step, got, want, spoken())
		}
	}
	wantValue := func(t *testing.T, step string, want int) {
		t.Helper()
		if got := auth.MaxActiveSessions(); got != want {
			t.Errorf("%s：生效值 = %d, want %d", step, got, want)
		}
	}

	// 前提：合法值不吭声（否则下面所有"出了一声"的断言都分不清是这一步的还是别处的）
	writeMaxSessions(t, "5")
	before := spoken()
	wantValue(t, "填 5（合法，等于默认）", 5)
	wantSpeak(t, "填 5", before, 0)

	// 1) P48 的原案：填 0 想要"一台都不许多出来"，实际按 5 走。语义不变，但必须说出来
	writeMaxSessions(t, "0")
	before = spoken()
	wantValue(t, "填 0", noticeDefault)
	wantSpeak(t, "填 0", before, 1)
	if line := buf.String(); !strings.Contains(line, `"0"`) || !strings.Contains(line, `实际按 "5" 走`) {
		t.Errorf("出声内容没把话说清（要含填的值与实际值）：\n%s", line)
	}

	// 2) 重复读：同一种替换只喊一次。热路径上每请求一条就是泡坏日志（号池那条同样的教训）
	before = spoken()
	for i := 0; i < 3; i++ {
		wantValue(t, "重复读 0", noticeDefault)
	}
	wantSpeak(t, "重复读 0 三次", before, 0)

	// 3) 负数：又一个"填了非正数"，值变了就是新事件，该再喊一次
	writeMaxSessions(t, "-1")
	before = spoken()
	wantValue(t, "填 -1", noticeDefault)
	wantSpeak(t, "填 -1", before, 1)

	// 4) 根本不是数字
	writeMaxSessions(t, "五个")
	before = spoken()
	wantValue(t, "填「五个」", noticeDefault)
	wantSpeak(t, "填「五个」", before, 1)

	// 5) 首尾带空格的合法值：这本来就是能用的值，不该被当成非法（改前 `" 3 "` 会被 Atoi 拒掉回默认）
	writeMaxSessions(t, " 3 ")
	before = spoken()
	wantValue(t, `填 " 3 "`, 3)
	wantSpeak(t, `填 " 3 "`, before, 0)

	// 6) 设置行整个没了（升级/误删）：也出声，因为"面板上没有这一行"与"填了但没生效"在管理员看来是一回事。
	//    用 Unscoped 硬删：这张表带软删位，只软删的话行还在（第 7 步补行时会撞 uniqueIndex），
	//    而"读到空串"这条通路两种删法都一样——差别只在库里到底有没有这一行。
	if err := db.DB.Unscoped().Where(map[string]interface{}{"key": noticeKey}).
		Delete(&models.SystemSetting{}).Error; err != nil {
		t.Fatalf("删设置行失败: %v", err)
	}
	db.InvalidateSettingCache(noticeKey)
	before = spoken()
	wantValue(t, "设置行不存在", noticeDefault)
	wantSpeak(t, "设置行不存在", before, 1)

	// 7) 填回合法值：安静下来（不喊不等于没修——一直喊才是另一种泡坏读数）
	writeMaxSessions(t, "8")
	before = spoken()
	wantValue(t, "填 8", 8)
	wantSpeak(t, "填 8", before, 0)
}
