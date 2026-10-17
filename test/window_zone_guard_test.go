package test

// 窗口谓词的时区无关性守卫（待办清单 P129）。
//
// 这条断言钉的不是"当日额度算得准不准"（那有各自的业务用例），而是一条不变式：
// **同一份数据在任意进程时区下必须读出同一个数**。
// SQLite 把时间列存成文本、比较走逐字符序，而写入侧带的偏移与边界侧带的偏移常常不同——
// 于是"文本序 ≠ 时刻序"（P129：当日已用量读成 0，超额不拦、429 变 200）。
//
// 两处形状是刻意选的，别改回"看起来更自然"的写法：
//   ① **行钉在边界两侧，而不是 `time.Now()` 附近**：行取 `since ± 分钟`。
//     按 now 造"窗口内"的行，能不能咬住取决于跑到几点——写这条时就被骗过一次
//     （变异没红，实为那一刻 UTC 与 +8 恰好同日）。钉在边界两侧之后与墙上时间无关。
//   ② **父进程比较各 TZ 的读数**，而不只是各自断言一个常量：
//     任何一个 TZ 单独看"恰好对"，但彼此不同也算红。
//     用子进程而不是 `os.Setenv`：`utils.PlatformZone()` 是 sync.Once 缓存的，
//     而进程时区是整个测试二进制共享的状态——那正是"单跑红、整包绿"的配方。

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"loomproxy/db"
	"loomproxy/gate"
	"loomproxy/models"
	"loomproxy/utils"
)

const windowChildEnv = "LOOMPROXY_WINDOW_CHILD"

// 各子进程的读数都应当是这两个值：窗口内两行 3+4 计入、钉在边界之前那行 100 不计入；会话 1 条有效。
const (
	wantUsed     = 7
	wantSessions = 1
)

func TestWindowPredicateIsZoneIndependent(t *testing.T) {
	if os.Getenv(windowChildEnv) == "1" {
		used := probeQuotaWindow(t)
		sessions := probeSessionValidity(t)
		// 父进程按这条机器可读的行取值，格式别改
		fmt.Printf("WINDOW-READINGS used=%d sessions=%d zone=%s\n", used, sessions, time.Now().Format("MST"))
		return
	}

	zones := []string{"UTC", "Asia/Shanghai", "Etc/GMT-14", "Etc/GMT-11"}
	usedByZone := make(map[string]int, len(zones))
	for _, tz := range zones {
		out := runWindowChild(t, tz)
		used, sessions := parseWindowReadings(t, out)
		if used != wantUsed {
			t.Errorf("TZ=%s：当日已用量 = %d, want %d（窗口内 3+4 应计入、边界之前那行 100 不该计入）", tz, used, wantUsed)
		}
		if sessions != wantSessions {
			t.Errorf("TZ=%s：活跃会话 = %d, want %d（已过期那行不该因为文本序被当成有效）", tz, sessions, wantSessions)
		}
		usedByZone[tz] = used
	}
	var firstTZ string
	var firstVal int
	for _, tz := range zones {
		v := usedByZone[tz]
		if firstTZ == "" {
			firstTZ, firstVal = tz, v
			continue
		}
		if v != firstVal {
			t.Errorf("各 TZ 读数不一致：%s 给 %d，%s 给 %d——同一份数据不该读出两个数", firstTZ, firstVal, tz, v)
		}
	}
}

func runWindowChild(t *testing.T, tz string) string {
	t.Helper()
	cmd := exec.Command(os.Args[0],
		"-test.run", "TestWindowPredicateIsZoneIndependent",
		"-test.count", "1",
		"-test.timeout", "120s",
	)
	cmd.Env = append(os.Environ(), windowChildEnv+"=1", "TZ="+tz)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("TZ=%s 的子进程退出非零：%v\n输出末尾：\n%s", tz, err, strings.Join(tail(string(out), 15), "\n"))
	}
	return string(out)
}

var readingsRE = regexp.MustCompile(`WINDOW-READINGS used=(\d+) sessions=(\d+)`)

func parseWindowReadings(t *testing.T, out string) (int, int) {
	t.Helper()
	m := readingsRE.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("子进程没有报出读数（标记行 WINDOW-READINGS 不见了）——输出末尾：\n%s",
			strings.Join(tail(string(out), 15), "\n"))
	}
	used, _ := strconv.Atoi(m[1])
	sessions, _ := strconv.Atoi(m[2])
	return used, sessions
}

// probeQuotaWindow 造三行用量：两行落在窗口**之内**（边界之后 5 分钟与 2 小时，各带一种偏移写法），
// 一行落在窗口**之前** 5 分钟。返回 UsedToday 的读数。
func probeQuotaWindow(t *testing.T) int64 {
	newTestServer(t)

	const uid = 4200
	user := &models.User{ID: uid}
	since := gate.UsageSince(user)
	// 行用固定区写，让它们与边界来自不同的表示——这正是现网里"同一列混着几种形状"的来源
	plus14 := time.FixedZone("PLUS14", 14*3600)
	minus11 := time.FixedZone("MINUS11", -11*3600)

	rows := []struct {
		at   time.Time
		cost int64
	}{
		{since.Add(5 * time.Minute).In(plus14), 3},
		{since.Add(2 * time.Hour).In(minus11), 4},
		{since.Add(-5 * time.Minute).In(utils.PlatformZone()), 100},
	}
	for i, r := range rows {
		row := models.QuotaUsageLog{UserID: uid, GroupCode: "fake_a", Interface: "search", Cost: r.cost, CreatedAt: r.at}
		if err := db.DB.Create(&row).Error; err != nil {
			t.Fatalf("插入第 %d 行用量失败: %v", i+1, err)
		}
	}
	return gate.UsedToday(user, "fake_a")
}

// probeSessionValidity 同一条不变式的另一头：过期就是过期，
// 不因为 `expires_at` 当初用哪个区写进去而改变。
func probeSessionValidity(t *testing.T) int {
	newTestServer(t)

	const uid = 4300
	now := time.Now()
	plus14 := time.FixedZone("PLUS14", 14*3600)
	minus11 := time.FixedZone("MINUS11", -11*3600)

	live := models.AuthSession{SessionID: "win-guard-live", UserID: uid, IP: "127.0.0.1",
		LastActiveAt: now, ExpiresAt: now.Add(time.Hour).In(plus14), CreatedAt: now.In(plus14)}
	exp := models.AuthSession{SessionID: "win-guard-exp", UserID: uid, IP: "127.0.0.1",
		LastActiveAt: now, ExpiresAt: now.Add(-time.Hour).In(minus11), CreatedAt: now.In(minus11)}
	for _, s := range []models.AuthSession{live, exp} {
		row := s
		if err := db.DB.Create(&row).Error; err != nil {
			t.Fatalf("插入会话 %s 失败: %v", row.SessionID, err)
		}
	}
	return len(db.ListActiveSessions(uid))
}

func tail(s string, n int) []string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}
