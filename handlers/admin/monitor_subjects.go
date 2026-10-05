package admin

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"loomproxy/base"
	"loomproxy/handlers/auth"
	"loomproxy/handlers/subjectrank"
)

// likeEscapeChar 是 LIKE 的转义字符。**刻意选 '!' 而不是反斜杠**（实测两方言，见待办清单 P43 那条判据）：
//
//	mysql/mariadb：  ... ESCAPE '\'   → ERROR 1064（字符串字面量里的 '\' 吞掉收尾引号）
//	postgres       ： 标准模式不处理反斜杠，'\' 是两个字符 → ESCAPE 参数不是单字符
//	sqlite         ： 接受 '\\' 但报错 "ESCAPE expression must be a single character"
//
// 也就是说：**不存在一种反斜杠写法能同时在三家成立**，换字符是唯一解。
// '!' 自身必须一起转义，否则用户搜「!%」时会把 '%' 转义掉，语义就变了。
const likeEscapeChar = '!'

// likeESCAPE 生成「列 LIKE ? ESCAPE '!'」片段：转义符只在这里出现一次，
// 各调用点不再手抄 ESCAPE 子句——手抄的下一句就是「MySQL 报 1064、SQLite 绿着过」。
func likeESCAPE(column string) string {
	return column + " LIKE ? ESCAPE '" + string(likeEscapeChar) + "'"
}

// likeESCAPEGroup 把若干列拼成**一整组带括号的** OR 条件（列数=占位符数，调用方按序传值）。
//
// SQL 里 AND 比 OR 结合得紧，而管理面的关键词总是和别的筛选条件串在同一条查询里：
// 裸写 `a LIKE ? OR b LIKE ?` 再接 `AND status = 1`，字面真意是 `a OR (b AND status=1)`。
// **今天没有 bug**——GORM 会把每个 Where 的裸表达式自己包一层括号（实测拼出
// `WHERE (username LIKE ? OR email LIKE ?) AND users.status = ?`），
// 所以这里加括号不是修缺陷，而是**不把正确性押在拼接细节上**：
// 同一条查询里将来可能出现手写 `Expr`/`Unscoped` 组合、或有人把两段拼成一个字符串（那时就真漏了）。
func likeESCAPEGroup(columns ...string) string {
	parts := make([]string, 0, len(columns))
	for _, col := range columns {
		parts = append(parts, likeESCAPE(col))
	}
	return "(" + strings.Join(parts, " OR ") + ")"
}

// escapeLike 转义 LIKE 模式里的通配符：用户搜「100%」不该把全表匹配进来。
func escapeLike(s string) string {
	r := strings.NewReplacer(
		string(likeEscapeChar), string(likeEscapeChar)+string(likeEscapeChar),
		`%`, string(likeEscapeChar)+`%`,
		`_`, string(likeEscapeChar)+`_`,
	)
	return r.Replace(s)
}

// GetMonitorSubjects 内容维度榜单（管理端）：某个搜索词/书名/章节/媒介在窗口内被调用得怎么样。
// 聚合逻辑在 handlers/subjectrank（与公开榜 /rank/boards 共用一份），这里只做参数解析、
// 权限（挂在 admin 组下，后端 AdminRequired 是唯一信任边界）与管理视角的全字段输出。
// 面向普通用户的公开榜只回名称与两个维度，见 handlers/rank。
func GetMonitorSubjects(c *gin.Context) {
	dimKey := strings.ToLower(strings.TrimSpace(c.Query("dim")))
	if !subjectrank.ValidDim(dimKey) {
		auth.Fail(c, http.StatusBadRequest, "dim 只接受 keyword/book/chapter/media")
		return
	}
	n, _ := strconv.Atoi(c.DefaultQuery("days", "7"))
	days := subjectrank.NormalizeDays(n)
	sourceFilter := c.Query("source")
	// 媒介筛选与公开端点同一份枚举（/admin/monitor/history 那边叫 media_type，口径一致）
	mediaFilter := strings.TrimSpace(c.Query("media"))
	if mediaFilter != "" && !base.IsMediaValue(mediaFilter) {
		auth.Fail(c, http.StatusBadRequest, "media 不是合法媒介（novel/audio/comic/video）")
		return
	}

	// 管理端上限 50 条，并允许按数据源与媒介筛
	items, err := subjectrank.Query(dimKey, days, sourceFilter, mediaFilter, 50, nil) // 管理端不限源
	if err != nil {
		auth.Fail(c, http.StatusInternalServerError, "数据库错误: "+err.Error())
		return
	}

	// 自我观测：哪一格没采到，本该由系统说，而不是靠人肉写 SQL 去猜
	coverage, covErr := subjectrank.Coverage(days, sourceFilter, nil)

	books, chapters := base.NameCacheStats()
	cache := gin.H{"books": books, "chapters": chapters}
	// 写侧体检：接了 Redis 才报 persist（纯内存没有持久化可丢）
	if queued, dropped, failed, ok := base.NameCachePersistHealth(); ok {
		cache["persist"] = gin.H{"queued": queued, "dropped": dropped, "failed": failed}
	}
	auth.Ok(c, gin.H{
		"dim":   dimKey,
		"days":  days,
		"from":  subjectrank.WindowStart(days),
		"items": items,
		// 覆盖率统计失败不该让整页挂掉：榜单本身还能看，这里只回一条说明
		"coverage":       coverage,
		"coverage_error": covErr != nil,
		"name_cache":     cache,
	})
}
