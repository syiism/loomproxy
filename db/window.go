package db

// 窗口谓词：「某时间列 与 一个时刻 比较」这类条件，在 SQLite 上**不能直接写**。
//
// 为什么（P129，读数全部来自当场探针，三种 TZ 下一致复现）：
//   - SQLite 没有真正的 DATETIME 类型，time.Time 落成**文本**；
//   - 文本的逐字符序不等于时刻的序，**只要两侧带的偏移不同**：
//     写入侧带的是"写入那一刻的值自己的区"（进程时区写的带 `Z` 或 `+14:00`，
//     平台时区算出来的时刻写的带 `+08:00`），而窗口边界来自 `utils.DayStart`（FixedZone +8）；
//   - 于是在 +08 主机上今天"看着对"，靠的是同日时 `'T'(0x54) > ' '(0x20)` 与同时刻同偏移的巧合；
//     进程日历日一旦落后平台日历日（UTC 主机在北京 00:00–07:59），下界直接判负——
//     「当日已用量」读成 0：超额不拦、429 变 200、当日读数与榜单偏小。
//     反向的错也实测过：把边界 `.UTC()` 只统一日期不统一形状，会把**窗口之前**的行算进窗口
//     （探针：窗口前一小时那一行 原样计入 0、`.UTC()` 计入 1）。
//
// 判据：**比较基准由库来归一，不由字符串形状碰运气**。SQLite 侧把两侧都交给 `datetime()`
// ——它解析偏移并归一到 UTC，因此混形状的历史行也能算对；MySQL/PostgreSQL 的列本来就是
// DATETIME/timestamptz、按值比较，**保持原生谓词**（那边套函数只会白丢索引）。
//
// 两条已知代价，写下来而不是当作没考虑：
//   - SQLite 侧谓词不可用时间段的索引（`(user_id, created_at)` 这类复合索引只剩等值段可用）；
//     长期解是定宽的平台日分桶列（`YYYYMMDD` 的字典序恰好等于时间序、跨方言安全），
//     那要加列 + 回填，本轮按维护者口径不做。
//   - 归一函数选 `julianday()` 而不是 `datetime()`：**`datetime()` 截到整秒，会吃掉判定所需的精度**。
//     这不是理论顾虑——测试当场拦下过一次：「刷新单日额度」把水印写在被刷那次请求之后的同一秒内
//     （`quota_refresh_test.go` 两条：刷新后仍 429、"已用应归 0 实得 1"），两侧截成同一刻，
//     `>=` 从"晚于"退化成"等于"，旧用量还是算进新窗口。`julianday()` 保留小数日（约 10 微秒量级），
//     同一秒内的先后关系因此不被抹平。

import (
	"strings"
	"time"

	"gorm.io/gorm/clause"
)

// SinceCond 是 `col >= t`，直接交给 `.Where(db.SinceCond(...))`。
func SinceCond(col string, t time.Time) clause.Expr {
	sql, vars := SinceSQL(col, t)
	return clause.Expr{SQL: sql, Vars: vars}
}

// UntilCond 是 `col < t`（半开区间 [from, to) 的上界那一头）。
func UntilCond(col string, t time.Time) clause.Expr {
	sql, vars := UntilSQL(col, t)
	return clause.Expr{SQL: sql, Vars: vars}
}

// AfterCond 是 `col > t`（严格大于；会话有效性用的是这一头）。
func AfterCond(col string, t time.Time) clause.Expr {
	sql, vars := AfterSQL(col, t)
	return clause.Expr{SQL: sql, Vars: vars}
}

// 下面三个是**片段形式**（SQL 串 + 参数），给那些把多个条件拼成一条字符串的调用方
// （`db/session.go` 与 `handlers/subjectrank` 那几处塞不进 clause.Expr）。
// 成对提供是为了别让调用方自己拼串——自己拼就会拼出第四种形状。

func SinceSQL(col string, t time.Time) (string, []interface{}) {
	return cmpSQL(col, ">=", t)
}

func UntilSQL(col string, t time.Time) (string, []interface{}) {
	return cmpSQL(col, "<", t)
}

func AfterSQL(col string, t time.Time) (string, []interface{}) {
	return cmpSQL(col, ">", t)
}

func AtOrBeforeSQL(col string, t time.Time) (string, []interface{}) {
	return cmpSQL(col, "<=", t)
}

func cmpSQL(col, op string, t time.Time) (string, []interface{}) {
	// 引号交给 dialector 的 QuoteTo：**不要在 SQL 里写死反引号**（PostgreSQL 只认双引号）
	quoted := quoteCol(col)
	if isSQLite() {
		return "julianday(" + quoted + ") " + op + " julianday(?)", []interface{}{t}
	}
	return quoted + " " + op + " ?", []interface{}{t}
}

// quoteCol 支持**表限定的列名**（`users.plan_expire_at`）：按 `.` 拆开逐段加引号再拼回点号。
// 整串交给 QuoteTo 会把它当成一个标识符引成 `"users.plan_expire_at"`——MySQL 下那就是一列不存在的名字。
func quoteCol(col string) string {
	var out strings.Builder
	for i, part := range strings.Split(col, ".") {
		if i > 0 {
			out.WriteByte('.')
		}
		DB.Dialector.QuoteTo(&out, part)
	}
	return out.String()
}

// AtOrBeforeCond 是 `col <= t`（与 AtOrBeforeSQL 同一条判据的 clause 形式）。
func AtOrBeforeCond(col string, t time.Time) clause.Expr {
	sql, vars := AtOrBeforeSQL(col, t)
	return clause.Expr{SQL: sql, Vars: vars}
}

func isSQLite() bool {
	n := DB.Dialector.Name()
	return n == "sqlite" || n == "sqlite3"
}
