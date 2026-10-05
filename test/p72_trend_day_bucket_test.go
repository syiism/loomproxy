package test

// 待办清单 P72：趋势图的**分桶**不能由库侧判日界。
//
// 症状（旧实现只在 sqlite 上暴露）：写一行北京 04:40 的明细，`date(created_at)` 给的是**前一天**——
// SQLite 的日期函数把带 +08 偏移的串先归一到 UTC 再取日；MySQL（生产 `loc=Local`）给的是当天。
// 于是同一张图上，日标轴按平台时区（P71 收口的那一处）、柱高按库侧口径，
// 开发/测试库里北京 00:00–08:00 这段量会静静挂到前一天的柱子上。
//
// 这条用例钉的是"两个口径必须合成一个"，而它自己也要能判断有没有牙齿：
// 如果当场算出来平台日与 UTC 日是**同一个字符串**（TZ_OFFSET_HOURS=0 那种部署），
// 两种写法给出同一个答案，断言就是空转——那种情况下直接 skip 并说明原因，不留一条假绿。

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"loomproxy/db"
	"loomproxy/models"
	"loomproxy/utils"
)

func TestMonitorTrendBucketsOnPlatformDay(t *testing.T) {
	srv := newTestServer(t)
	admin := authHeader(adminToken(t, srv))

	zone := utils.PlatformZone()
	now := time.Now().In(zone)
	yesterday := now.AddDate(0, 0, -1)
	// 平台时区的昨天 04:40：UTC 侧看是前天 20:40——这正是旧实现会归错的那几个小时
	at := time.Date(yesterday.Year(), yesterday.Month(), yesterday.Day(), 4, 40, 0, 0, zone)
	dayPlat := at.Format("2006-01-02")
	dayUTC := at.UTC().Format("2006-01-02")
	if dayPlat == dayUTC {
		t.Skipf("平台日与 UTC 日相同（%s）：这条用例区分不了两种口径，跳过而不是留一条假绿（TZ_OFFSET_HOURS=%v）",
			dayPlat, utils.PlatformZone().String())
	}

	const src = "trend_p72"
	row := models.ApiCallLog{Source: src, Action: "search", Status: 200, Username: "p72", CreatedAt: at}
	if err := db.DB.Create(&row).Error; err != nil {
		t.Fatalf("插入明细失败: %v", err)
	}
	// 前提自检：库里那一行真的是这个时刻（GORM 不许把 CreatedAt 改写成 now，否则测的是别的东西）
	var back models.ApiCallLog
	if err := db.DB.First(&back, row.ID).Error; err != nil {
		t.Fatalf("读回明细失败: %v", err)
	}
	if got := back.CreatedAt.In(zone).Format("2006-01-02 15:04"); got != at.In(zone).Format("2006-01-02 15:04") {
		t.Fatalf("明细时刻被改写: got=%s want=%s——用例前提不成立", got, at.In(zone).Format("2006-01-02 15:04"))
	}

	status, env := doJSON(t, srv, http.MethodGet, "/admin/monitor/trend", nil, admin)
	if status != http.StatusOK {
		t.Fatalf("GET /admin/monitor/trend = %d (%s)", status, env.Msg)
	}
	buf, err := json.Marshal(env.Data)
	if err != nil {
		t.Fatalf("响应结构异常: %v", err)
	}
	var got struct {
		Days []string `json:"days"`
		Rows []struct {
			Day    string `json:"day"`
			Source string `json:"source"`
			Total  int64  `json:"total"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(buf, &got); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}

	var hits []string
	for _, r := range got.Rows {
		if r.Source == src {
			hits = append(hits, r.Day)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("source=%s 的桶有 %d 个（%v），want 1——同一条明细被分到两天就是第二处日界在起作用", src, len(hits), hits)
	}
	if hits[0] != dayPlat {
		t.Errorf("这条明细被归到 %s，want 平台日的 %s（UTC 日是 %s）——柱高还在按库侧口径判日界",
			hits[0], dayPlat, dayUTC)
	}
	// 反向自检：日标轴必须包含那一天，否则"归对日"也是空转（面板按日标取数，取不到的行会静默消失）
	found := false
	for _, d := range got.Days {
		if d == dayPlat {
			found = true
		}
	}
	if !found {
		t.Errorf("days 序列里没有 %s：那一天的桶在面板上无处可去（旧形状里它就静静消失）", dayPlat)
	}
}
