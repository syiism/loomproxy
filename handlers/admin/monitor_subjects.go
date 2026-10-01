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

// escapeLike 转义 LIKE 模式里的通配符：用户搜「100%」不该把全表匹配进来。
// 转义字符用反斜杠（SQLite/MySQL/PostgreSQL 的 ESCAPE '\' 语义一致）
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
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

	// 管理端上限 50 条，并允许按数据源筛
	items, err := subjectrank.Query(dimKey, days, sourceFilter, 50, nil) // 管理端不限源
	if err != nil {
		auth.Fail(c, http.StatusInternalServerError, "数据库错误: "+err.Error())
		return
	}

	books, chapters := base.NameCacheStats()
	cache := gin.H{"books": books, "chapters": chapters}
	// 写侧体检：接了 Redis 才报 persist（纯内存没有持久化可丢）
	if queued, dropped, failed, ok := base.NameCachePersistHealth(); ok {
		cache["persist"] = gin.H{"queued": queued, "dropped": dropped, "failed": failed}
	}
	auth.Ok(c, gin.H{
		"dim":        dimKey,
		"days":       days,
		"from":       subjectrank.WindowStart(days),
		"items":      items,
		"name_cache": cache,
	})
}
