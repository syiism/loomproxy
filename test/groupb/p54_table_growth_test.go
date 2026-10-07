package groupb

// 待办清单 P54：只增不减的表要有"长多快"的读数。
//
// 这一条不是修任何东西——拍板的就是「只加读数、清理策略一字不动」。
// 用例钉的是读数的**形状与诚实**：
//   - 四张表一张都不能少（少一张就是"这张表没人管"的下一个版本）；
//   - 计数与窗口口径对得上（插入多少就报多少，日增 = 窗口新增 ÷ 窗口天数）；
//   - 保留窗口那一格不许撒谎：只有 `api_call_logs` 有窗口这个概念，另外三张写"无"。
//
// 读数不可用时报 -1 而不是 0，这条判据在代码里（同 P35② 的 soft_deleted）；
// 这里能验的是"计数成功时它是个真数"，失败路径要靠打断查询——那需要改测试夹具里的表名，
// 而扫描器（`ledger-write-check` 之类）之外再造一个坏夹具不值一轮，所以**这一格没验到**，写在文末。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"loomproxy/db"
	"loomproxy/models"
)

func TestAdminStatsTableGrowth(t *testing.T) {
	srv := newTestServer(t)
	admin := authHeader(adminToken(t, srv))

	// 铺样本：一张表两条（窗口内），一张表一条在窗口外
	inside := time.Now().Add(-2 * time.Hour)
	outside := time.Now().Add(-90 * 24 * time.Hour)
	logs := []models.ApiCallLog{
		{Source: "growth_a", Action: "search", Status: 200, CreatedAt: inside},
		{Source: "growth_b", Action: "search", Status: 200, CreatedAt: inside},
		{Source: "growth_c", Action: "search", Status: 200, CreatedAt: outside},
	}
	if err := db.DB.Create(&logs).Error; err != nil {
		t.Fatalf("插入明细失败: %v", err)
	}
	// 前提自检：窗口外那条真的在窗口外（90 天 > 任何窗口），否则"窗口内新增"这一格是空转
	if d := time.Until(outside); d > -72*time.Hour {
		t.Fatalf("样本前提不成立: outside 距 now 只有 %v", -d)
	}

	status, env := doJSON(t, srv, http.MethodGet, "/admin/stats", nil, admin)
	if status != http.StatusOK {
		t.Fatalf("GET /admin/stats = %d (%s)", status, env.Msg)
	}
	buf, err := json.Marshal(env.Data)
	if err != nil {
		t.Fatalf("响应结构异常: %v", err)
	}
	var got struct {
		Growth struct {
			WindowDays int `json:"window_days"`
			Tables     []struct {
				Table         string `json:"table"`
				Rows          int64  `json:"rows"`
				Recent        int64  `json:"recent"`
				PerDay        int64  `json:"per_day"`
				RetentionDays int    `json:"retention_days"`
				HasRetention  bool   `json:"has_retention"`
				Note          string `json:"note"`
			} `json:"tables"`
			Scope string `json:"scope"`
		} `json:"table_growth"`
	}
	if err := json.Unmarshal(buf, &got); err != nil {
		t.Fatalf("解析 table_growth 失败: %v", err)
	}

	if got.Growth.WindowDays <= 0 {
		t.Errorf("window_days = %d：窗口天数必须由响应下发，面板不许自己抄（P46 同一条判据）", got.Growth.WindowDays)
	}
	want := []string{"api_call_logs", "quota_usage_logs", "auth_sessions", "redemption_logs"}
	if len(got.Growth.Tables) != len(want) {
		t.Fatalf("表 = %d 张，want %d 张（%v）——少一张就是下一轮「没人管」的那张",
			len(got.Growth.Tables), len(want), want)
	}
	byName := map[string]int{}
	for i, tb := range got.Growth.Tables {
		if tb.Table != want[i] {
			t.Errorf("第 %d 张表 = %q, want %q（顺序是代码里的白名单，面板不重排）", i, tb.Table, want[i])
		}
		byName[tb.Table] = i
	}

	// api_call_logs：读数必须是当场数出来的真数，且日增 = 窗口新增 ÷ 窗口
	i := byName["api_call_logs"]
	call := got.Growth.Tables[i]
	if call.Rows < 0 {
		t.Fatalf("api_call_logs 报 %d：样本都插不进去说明读数没在工作", call.Rows)
	}
	if call.Recent < 2 {
		t.Errorf("窗口内新增 = %d, want ≥2（刚插了两条）", call.Recent)
	}
	if call.PerDay != call.Recent/int64(got.Growth.WindowDays) {
		t.Errorf("per_day = %d 与 recent(%d)/window(%d) 对不上", call.PerDay, call.Recent, got.Growth.WindowDays)
	}
	if !call.HasRetention {
		t.Errorf("api_call_logs 的保留窗口该报出来（retention=%d）——这一格就是 P54 说的那句「还默认关着」", call.RetentionDays)
	}

	// 另外三张：明确"没有保留窗口"，不许把 0 渲染成"永久保留 0 天"这种自相矛盾
	for _, name := range []string{"quota_usage_logs", "auth_sessions", "redemption_logs"} {
		tb := got.Growth.Tables[byName[name]]
		if tb.HasRetention {
			t.Errorf("%s 被报成有保留窗口——它的清理策略这一轮根本没拍（P54 选 A）", name)
		}
		if tb.Rows < 0 {
			t.Errorf("%s 读数不可用（-1）：这张表本来就在库里，报不出来是查询失败", name)
		}
		if tb.Note == "" {
			t.Errorf("%s 缺一句「为什么要单独过」——留存口径的提醒不能只存在于提交信息里", name)
		}
	}

	// scope 那句必须明说"这不是清理建议"，否则下一轮有人照着这格动手删表
	if got.Growth.Scope == "" || !strings.Contains(got.Growth.Scope, "清理") {
		t.Errorf("scope 没写清边界: %q", got.Growth.Scope)
	}
}
