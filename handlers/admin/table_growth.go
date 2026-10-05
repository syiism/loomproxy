package admin

// 表增长读数（待办清单 P54，2026-10-06 拍板选 A：**只加读数，清理策略一字不动**）。
//
// 为什么要这一格：四张只增不减的表里只有 `api_call_logs` 有保留窗口，而 `MONITOR_RETENTION_DAYS`
// 默认 0 = 永久保留，等于现在没在清。"哪张表在长、长多快"过去在面板与读数里一个字都没有，
// 将来谁要加清理只能像 P35 那次一样先猜一个数——这一格的作用就是"真成事故时不用从头查起"。
//
// 三条纪律：
//   - 表名是这里的**白名单常量**，不接受任何入参：面板不许自己填表名，那是替管理员拼一条 SQL。
//   - 读不到就报 `-1` 并说明读数不可用，**不报 0**。判据取自 P35② 的 `soft_deleted`：
//     把一次查询失败说成 0，就是撒一个让人放心的谎——而"没有堆积"正是这格要被读出来的结论。
//   - 窗口天数随响应下发（`window_days`），面板不自己抄数字（同 P46 的 `filters_meta`）。
//
// 刻意不算的那几格，写在理由里而不是留着让人以为算过：
//   - 「表内最早一行」不做：`MIN(created_at)` 扫出来跨方言有三种形状（MySQL 给 time、
//     SQLite 给文本、Postgres 给 timestamp），而"长多快"这个问题不需要它——
//     这正是本仓反复付学费的"在 SQL 里写死方言"那一族，能不碰就不碰。
//   - 清理建议/预计到达某阈值的天数不做：留存口径没拍（`quota_usage_logs` 是判定源、
//     `redemption_logs` 存卡码原文），给一个基于未拍口径的数字等于替系统说假话。

import (
	"log"
	"time"

	"loomproxy/conf"
	"loomproxy/db"
	"loomproxy/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// growthWindowDays 日增速率的取样窗口：7 天与趋势图同一个跨度，读数之间才可比。
const growthWindowDays = 7

type growthSpec struct {
	table string
	// model 只用来让 GORM 推出表名与软删作用域，不参与任何拼接
	model     interface{}
	retention int // 该表的保留天数；0 = 永久（含"这张表根本没有保留窗口"这一档）
	hasWindow bool
	note      string
}

// growthSpecs 就是 P54 当场数过的那四张表（按 information_schema 行数排名 ↔ 代码里的清理路径逐张对）。
// 顺序固定，面板照这个顺序显示，不再二次排序。
func growthSpecs() []growthSpec {
	return []growthSpec{
		{
			table: "api_call_logs", model: &models.ApiCallLog{},
			retention: conf.Config.MonitorRetentionDays, hasWindow: true,
			// 读的是**同一个来源**：`app/monitor.go` 的 `monitorRetention()` 也从 `conf.Config` 取这一项，
			// 所以这一格报的 0 就是"清理循环确实不动"的那个 0，不是面板另算了一份口径（同 P41 的判据：
			// 判定与读数共用一处定义）。
			note: "唯一有保留窗口的表；retention_days=0 就是永久保留（现网默认），清理循环在跑但没东西可清",
		},
		{
			table: "quota_usage_logs", model: &models.QuotaUsageLog{},
			note: "当日用量的判定源（gate.UsageSince 读它）——删哪一段等于改判定口径，不在本轮拍",
		},
		{
			table: "auth_sessions", model: &models.AuthSession{},
			note: "已吊销/已过期的行不参与任何判定（db.ActiveSessionCond 把它们挡在外面），但行还占着表",
		},
		{
			table: "redemption_logs", model: &models.RedemptionLog{},
			note: "存卡码原文（失败的尝试里可能含当时有效的卡码），它的保留期是留存口径不是磁盘问题",
		},
	}
}

// TableGrowth 生成读数。任一表查询失败只影响那一行（报 -1），不整格消失——
// "读数缺席"与"读数为零"必须分得开，否则面板上一片空白会被读成"都没在长"。
func TableGrowth() gin.H {
	since := time.Now().AddDate(0, 0, -growthWindowDays)
	out := make([]gin.H, 0, 4)
	for _, s := range growthSpecs() {
		row := gin.H{
			"table":          s.table,
			"rows":           int64(-1),
			"recent":         int64(-1),
			"per_day":        int64(-1),
			"retention_days": s.retention,
			"has_retention":  s.hasWindow,
			"note":           s.note,
		}
		var total, recent int64
		if err := countRows(s.model, nil, &total); err != nil {
			log.Printf("ERROR: 表增长读数失败（table=%s 全量计数）：%v", s.table, err)
			out = append(out, row)
			continue
		}
		if err := countRows(s.model, func(q *gorm.DB) *gorm.DB {
			return q.Where("created_at >= ?", since)
		}, &recent); err != nil {
			log.Printf("ERROR: 表增长读数失败（table=%s 窗口计数）：%v", s.table, err)
			out = append(out, row)
			continue
		}
		row["rows"] = total
		row["recent"] = recent
		// 整数除法即可：这格回答"量级"，不是精确速率；小数会把"日增 0.4"渲染成像是精确数
		row["per_day"] = recent / growthWindowDays
		out = append(out, row)
	}
	return gin.H{
		"window_days": growthWindowDays,
		"tables":      out,
		// 明写这一句，免得有人把这一格读成"系统在建议清理"
		"scope": "只是读数：清理策略与保留期一字未动，拍板记录见 docs/规范/待办清单.md 的 P54",
	}
}

// countRows 计数走 GORM 的模型作用域（自带软删过滤），条件由调用方以闭包给出，
// 表名不经任何字符串拼接。
func countRows(model interface{}, with func(*gorm.DB) *gorm.DB, into *int64) error {
	q := db.DB.Model(model)
	if with != nil {
		q = with(q)
	}
	return q.Count(into).Error
}
