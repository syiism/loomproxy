package test

// 额度扣减冷却（待办清单 P25）：同一用户对「同一源 + 同一接口 + 同一内容」在窗口内只扣一次。
// 触发用的真实形态是「第一条正文 10 秒才回来，客户端等不及重试同一篇」——两次都 200、各扣 1 点。
// 断言故意盯住三件事：同内容不重复扣、**换内容必须照扣**（防止把去重写成「一天只扣一次」）、
// 关掉开关要退回按请求扣。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"loomproxy/conf"
	"loomproxy/db"
	"loomproxy/models"
)

// setContentCost 显式配 fake_a/content 的单价并清掉速率字段。
// 播种出来的 content 本来就是 1（见 TestSeededCostsOnlyContentIsBillable），这里重配是为了让本文件
// 的三个用例各自拿到确定的 cost，不去依赖「seed 恰好给了 1」这条别的用例也在验的事实。
func setContentCost(t *testing.T) {
	t.Helper()
	var n int64
	db.DB.Model(&models.QuotaCost{}).Where("group_code = ? AND interface = ?", "fake_a", "content").Count(&n)
	if n == 0 {
		if err := db.DB.Create(&models.QuotaCost{
			GroupCode: "fake_a", Interface: "content", Cost: 1, Status: 1,
		}).Error; err != nil {
			t.Fatalf("写入 fake_a/content 单价失败: %v", err)
		}
	} else if err := db.DB.Model(&models.QuotaCost{}).
		Where("group_code = ? AND interface = ?", "fake_a", "content").
		// interval/limit 一起清零：本用例要连打三次，不能被速率限制先挡下来
		// （那会让第 2、3 次根本进不到扣减分支，测到的是限流不是冷却）
		Updates(map[string]interface{}{"cost": 1, "status": 1, "interval": 0, "limit_count": 0, "window_sec": 0}).Error; err != nil {
		t.Fatalf("改 fake_a/content 单价失败: %v", err)
	}
	delCostCache("fake_a", "content")
}

// contentWindow 假上游 + 单价 + 冷却窗口，返回清理函数
func contentWindow(t *testing.T, srv *httptest.Server, windowSec int) (token string, uid uint) {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(upstream.Close)
	setAUpstream(t, upstream.URL)
	setContentCost(t)

	// 窗口是本项目的构建期配置，用例里直接改运行期值并注册还原（不为此加测试专用入口）
	prev := conf.Config.BillingDedupeSec
	conf.Config.BillingDedupeSec = windowSec
	t.Cleanup(func() { conf.Config.BillingDedupeSec = prev })

	// 用户名唯一化：本文件的三个用例共用一个进程，而冷却表是进程级的
	name := fmt.Sprintf("dedup_%s_%d", windowLabel(windowSec), dedupSeq.Add(1))
	token = registerUser(t, srv, name, name+"@example.com", "pass1234")
	return token, userIDByName(t, name)
}

var dedupSeq atomic.Int64

func windowLabel(sec int) string {
	switch {
	case sec <= 0:
		return "off"
	case sec <= 1:
		return "short"
	default:
		return "wide"
	}
}

func contentWith(t *testing.T, srv *httptest.Server, token, bookID, itemID string) int {
	t.Helper()
	status, _ := doRaw(t, srv, http.MethodGet, "/fake_a/content"+buildQuery(map[string]string{
		"bookId": bookID, "itemId": itemID,
	}), nil, authHeader(token))
	return status
}

func deductCount(t *testing.T, uid uint) int64 {
	t.Helper()
	var count int64
	if err := db.DB.Model(&models.QuotaUsageLog{}).
		Where("user_id = ? AND group_code = ? AND interface = ?", uid, "fake_a", "content").
		Count(&count).Error; err != nil {
		t.Fatalf("查扣减流水失败: %v", err)
	}
	return count
}

// TestBillingDedupeSameContent 同内容连打三次只扣一次；换一个 itemId 必须再扣一次
func TestBillingDedupeSameContent(t *testing.T) {
	srv := newTestServer(t)
	token, uid := contentWindow(t, srv, 60)

	for i := 1; i <= 3; i++ {
		if st := contentWith(t, srv, token, "1", "2"); st != http.StatusOK {
			t.Fatalf("第 %d 次同内容请求 status = %d, want 200（冷却不能改变放行与状态码）", i, st)
		}
	}
	if got := deductCount(t, uid); got != 1 {
		t.Fatalf("同内容三次的扣减条数 = %d, want 1（待办清单 P25：重试不该重复扣额度）", got)
	}

	if st := contentWith(t, srv, token, "1", "3"); st != http.StatusOK {
		t.Fatalf("换章节请求 status = %d, want 200", st)
	}
	if got := deductCount(t, uid); got != 2 {
		t.Fatalf("换一个 itemId 之后的扣减条数 = %d, want 2（冷却只认同一篇内容，不能变成「一天一次」）", got)
	}

	// 自省读数要从管理端读得到：没有这一格，「冷却到底挡没挡」只能靠人肉比流水（P19 的同一课）
	st, env := doJSON(t, srv, http.MethodGet, "/admin/stats", nil, authHeader(adminToken(t, srv)))
	if st != http.StatusOK {
		t.Fatalf("GET /admin/stats status = %d, want 200", st)
	}
	bd, _ := env.dataMap(t)["billing_dedupe"].(map[string]interface{})
	if bd == nil {
		t.Fatalf("/admin/stats 没有 billing_dedupe 字段（冷却在跑但没人能看见它）")
	}
	if bd["window_sec"] != float64(60) || bd["enabled"] != true {
		t.Fatalf("billing_dedupe = %v, want window_sec=60 且 enabled=true", bd)
	}
	if skipped, _ := bd["skipped"].(float64); skipped < 1 {
		t.Fatalf("billing_dedupe.skipped = %v, want >=1（挡掉的是扣减次数，不是请求次数）", bd["skipped"])
	}
}

// TestBillingDedupeDisabled BILLING_DEDUPE_SEC=0 时退回「每个成功请求都扣」
func TestBillingDedupeDisabled(t *testing.T) {
	srv := newTestServer(t)
	token, uid := contentWindow(t, srv, 0)

	for i := 1; i <= 3; i++ {
		if st := contentWith(t, srv, token, "9", "9"); st != http.StatusOK {
			t.Fatalf("关闭冷却后第 %d 次请求 status = %d, want 200", i, st)
		}
	}
	if got := deductCount(t, uid); got != 3 {
		t.Fatalf("窗口=0 时扣减条数 = %d, want 3（关掉开关必须回到按请求扣）", got)
	}
}

// TestBillingDedupeWindowExpiry 窗口过期后同一篇内容要能再扣——冷却不是免费券
func TestBillingDedupeWindowExpiry(t *testing.T) {
	srv := newTestServer(t)
	token, uid := contentWindow(t, srv, 1)

	if st := contentWith(t, srv, token, "7", "7"); st != http.StatusOK {
		t.Fatalf("第一次请求 status = %d, want 200", st)
	}
	if st := contentWith(t, srv, token, "7", "7"); st != http.StatusOK {
		t.Fatalf("窗口内第二次请求 status = %d, want 200", st)
	}
	if got := deductCount(t, uid); got != 1 {
		t.Fatalf("窗口内扣减条数 = %d, want 1", got)
	}

	time.Sleep(1300 * time.Millisecond)
	if st := contentWith(t, srv, token, "7", "7"); st != http.StatusOK {
		t.Fatalf("窗口外请求 status = %d, want 200", st)
	}
	if got := deductCount(t, uid); got != 2 {
		t.Fatalf("窗口过期后的扣减条数 = %d, want 2（过期必须重新扣，否则等于送了免费额度）", got)
	}
}
