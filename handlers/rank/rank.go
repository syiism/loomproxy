// Package rank 面向登录用户的榜单端点：把「调用明细的内容维度」聚合成两张榜（搜索热词、阅读）
// 只回名称与次数，供面板排行榜页使用。
//
// 与 /admin/monitor/subjects 物理错开（不同包、不同路径、不同字段集），原因：
//   - 明细含用户阅读行为（搜索词/书名/章节名/调用时间/IP），属敏感数据，管理端那张榜带
//     success_rate、latency、sources、last_called_at 等运维字段，等于把排障视角整包交出去；
//   - 这里只给 name + total，且维度锁死在 keyword 与 book（章节标题颗粒度太细，
//     配合时间能反推出"谁在读哪本书的哪一章"，不对普通用户开放）。
//
// 是否对普通用户开放由管理员在系统设置里决定（rank_public_enabled，默认关闭）；
// 关闭时非管理员一律 403，管理员自己始终能看（否则开关一关，管理页也跟着空白）。
package rank

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"loomproxy/db"
	"loomproxy/handlers/auth"
	"loomproxy/handlers/subjectrank"
	"loomproxy/models"
)

const settingKeyPublic = "rank_public_enabled"

// 公开榜的维度与条数上限：维度白名单在这里也钉一次，
// 免得以后有人往 boards 里加 chapter/media 就当"只是多个字段"
var boards = []struct {
	Dim   string `json:"-"`
	Title string `json:"title"`
}{
	{Dim: "keyword", Title: "搜索热词榜"},
	{Dim: "book", Title: "阅读榜"},
}

const boardLimit = 20

type boardEntry struct {
	Name  string `json:"name"`
	Total int64  `json:"total"`
}

type boardPayload struct {
	Dim   string       `json:"dim"`
	Title string       `json:"title"`
	Rows  []boardEntry `json:"rows"`
}

// isAdmin 当前会话是否管理员：查库取角色（与 auth.AdminRequired 同一口径，
// 不信 JWT 里的角色声明——角色可能已被改，后端是唯一信任边界）
func isAdmin(c *gin.Context) bool {
	v, exists := c.Get("user_id")
	uid, ok := v.(uint)
	if !exists || !ok || uid == 0 {
		return false
	}
	var u models.User
	if err := db.DB.Preload("Roles").First(&u, uid).Error; err != nil {
		return false
	}
	return u.IsAdmin()
}

// GetBoards 返回两张榜的聚合条目（只含名称与次数）
func GetBoards(c *gin.Context) {
	if db.GetSetting(settingKeyPublic) != "true" && !isAdmin(c) {
		auth.Fail(c, http.StatusForbidden, "榜单尚未对普通用户开放，可请管理员在「系统设置 · 站点」开启")
		return
	}

	days := subjectrank.NormalizeDays(queryInt(c, "days", 7))
	// 公开榜的时间窗口只允许 1/7/30 天：窗口越短，稀有词条越容易反推到具体某一个人
	switch days {
	case 1, 7, 30:
	default:
		days = 7
	}

	out := make([]boardPayload, 0, len(boards))
	for _, b := range boards {
		items, err := subjectrank.Query(b.Dim, days, "", boardLimit)
		if err != nil {
			auth.Fail(c, http.StatusInternalServerError, "榜单统计失败")
			return
		}
		rows := make([]boardEntry, 0, len(items))
		for _, it := range items {
			if it.Name == "" {
				continue
			}
			rows = append(rows, boardEntry{Name: it.Name, Total: it.Total})
		}
		out = append(out, boardPayload{Dim: b.Dim, Title: b.Title, Rows: rows})
	}
	auth.Ok(c, gin.H{"days": days, "boards": out, "limit": boardLimit})
}

func queryInt(c *gin.Context, key string, def int) int {
	raw := c.Query(key)
	if raw == "" {
		return def
	}
	n := 0
	for _, ch := range raw {
		if ch < '0' || ch > '9' {
			return def
		}
		n = n*10 + int(ch-'0')
	}
	if n == 0 {
		return def
	}
	return n
}

// RegisterRoutes 挂载 /rank 路由（JWT 会话保护；是否放行由 rank_public_enabled 决定）
func RegisterRoutes(r *gin.Engine) {
	g := r.Group("/rank")
	g.Use(auth.AuthRequired())
	g.GET("/boards", GetBoards)
}
