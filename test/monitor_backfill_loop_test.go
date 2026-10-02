package test

// 名称回填从「管理员点一次才跑一次」变成默认行为（待办清单 P20）。
//
// 缺名的成因是结构性的：正文与目录调用只带标识，名称要靠命名缓存反查，而重启后那第一批调用
// 撞上的是空缓存。等这些书再被搜索/详情命中，映射回来了、行本可以补上，只是没人记得去点。
// 这里钉两件事：循环体确实补得上，以及**超出单批上限时留在窗口外的一定是最旧的行**——
// 榜与覆盖率读的都是近窗口，补不到的必须是没人再看的旧行；旧代码按 id 升序捞，配上定时循环
// 就等于让一批永远反查不到的陈旧行长期霸占整个扫描窗口，新行一条也进不来。

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"loomproxy/app"
	"loomproxy/base"
	"loomproxy/db"
	"loomproxy/handlers/admin"
	"loomproxy/models"
)

// seedBackfillable 造 count 行「有标识、无名称」的明细；标识由 prefix 编号，便于只对某一批预热缓存。
// 批量插一次：逐行 Create 在这个量级上会把测试拖成分钟级。
func seedBackfillable(t *testing.T, source, identPrefix string, count int) {
	t.Helper()
	rows := make([]models.ApiCallLog, 0, count)
	for i := 0; i < count; i++ {
		rows = append(rows, models.ApiCallLog{
			Username: "bfl_u", IP: "127.0.0.2", Source: source, Action: "content",
			Status: http.StatusOK, LatencyMs: 8, CreatedAt: time.Now(),
			BookIdent: fmt.Sprintf("%s-%d", identPrefix, i),
			Media:     base.MediaNovel, ResultCount: 1,
		})
	}
	if err := db.DB.CreateInBatches(&rows, 500).Error; err != nil {
		t.Fatalf("批量写入明细失败: %v", err)
	}
}

func TestBackfillTickFillsMissingNames(t *testing.T) {
	newTestServer(t)
	base.ResetMetrics()

	base.RememberBook("fake_a", "bk-loop", "回填循环测试书", base.MediaNovel)
	base.RememberChapter("fake_a", "ch-loop", "回填循环测试章")
	insertIdentRow(t, "fake_a", "bk-loop", "", "ch-loop", "")
	insertIdentRow(t, "fake_a", "bk-loop", "", "ch-loop", "")

	app.RunSubjectBackfillTick()

	var left int64
	if err := db.DB.Model(&models.ApiCallLog{}).
		Where("book_ident = ? AND (book_name IS NULL OR book_name = '')", "bk-loop").Count(&left).Error; err != nil {
		t.Fatalf("统计剩余缺名行失败: %v", err)
	}
	if left != 0 {
		t.Errorf("循环一轮后仍缺名 %d 行，期望 0", left)
	}
	var titled int64
	db.DB.Model(&models.ApiCallLog{}).
		Where("chapter_ident = ? AND chapter_title = ?", "ch-loop", "回填循环测试章").Count(&titled)
	if titled != 2 {
		t.Errorf("章节名应补到 2 行，实得 %d", titled)
	}

	// 幂等：再跑一轮不该重复计数（补好的行不再进入扫描集）
	app.RunSubjectBackfillTick()
	summary, err := admin.RunSubjectBackfill(admin.BackfillDefaultDays)
	if err != nil {
		t.Fatalf("再跑一轮失败: %v", err)
	}
	if summary.Scanned != 0 || summary.BookFilled != 0 || summary.ChapterFilled != 0 {
		t.Errorf("重复执行应为空操作，实得 %+v", summary)
	}
}

func TestBackfillWindowKeepsNewestRows(t *testing.T) {
	newTestServer(t)
	base.ResetMetrics()

	// 比单批上限多一行：这行 id 最小（最先插入），按倒序捞时它被丢在窗口外
	total := admin.BackfillScanCap + 1
	seedBackfillable(t, "fake_a", "bk-win", total)
	// 只有被丢出去的那一行预热了映射：如果扫描按升序（旧行为），补到的正是它、新行全留空
	var oldest models.ApiCallLog
	if err := db.DB.Model(&models.ApiCallLog{}).
		Where("book_ident = ?", "bk-win-0").Order("id asc").First(&oldest).Error; err != nil {
		t.Fatalf("找不到最早写入的那行: %v", err)
	}
	base.RememberBook("fake_a", "bk-win-0", "最早那本书", base.MediaNovel)

	summary, err := admin.RunSubjectBackfill(admin.BackfillDefaultDays)
	if err != nil {
		t.Fatalf("回填失败: %v", err)
	}
	if !summary.Truncated {
		t.Errorf("超出单批上限时应报 truncated（循环据此接着捞），实得 %+v", summary)
	}
	if summary.Scanned != admin.BackfillScanCap {
		t.Errorf("单批扫描行数 = %d, want %d", summary.Scanned, admin.BackfillScanCap)
	}
	db.DB.First(&oldest, oldest.ID)
	if oldest.BookName != "" {
		t.Errorf("最旧的行被捞到了（补成 %q）：扫描顺序还是升序，陈旧行会长期霸占窗口", oldest.BookName)
	}

	// 循环认「补不动就停」：这批新行缓存里没有映射，一轮里最多捞 backfillRoundsPerTick 批后收手
	app.RunSubjectBackfillTick()
	var stillEmpty int64
	db.DB.Model(&models.ApiCallLog{}).
		Where("book_ident = ?", "bk-win-0").Count(&stillEmpty)
	if stillEmpty != 1 {
		t.Errorf("补不动的行不该被动到，实得 %d 行", stillEmpty)
	}
}
