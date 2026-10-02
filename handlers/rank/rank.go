// Package rank 面向登录用户的榜单端点：把「调用明细的内容维度」聚合成两张榜（搜索热词、阅读）
// 只回名称与次数，供面板排行榜页使用。
//
// 与 /admin/monitor/subjects 物理错开（不同包、不同路径、不同字段集），原因：
//   - 明细含用户阅读行为（搜索词/书名/章节名/调用时间/IP），属敏感数据，管理端那张榜带
//     success_rate、latency、sources、last_called_at 等运维字段，等于把排障视角整包交出去；
//   - 这里只给 name + total（阅读榜另带 book_id，见 boardEntry），且维度锁死在 keyword 与 book
//     （章节标题颗粒度太细，配合时间能反推出"谁在读哪本书的哪一章"，不对普通用户开放）。
//
// 是否对普通用户开放由管理员在系统设置里决定（rank_public_enabled，默认关闭）；
// 关闭时非管理员一律 403，管理员自己始终能看（否则开关一关，管理页也跟着空白）。
package rank

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"loomproxy/base"
	"loomproxy/db"
	"loomproxy/handlers/auth"
	"loomproxy/handlers/subjectrank"
	"loomproxy/models"
)

// settingPublicSources 源级白名单：逗号分隔的数据源码，**留空=不对普通用户开放任何榜单**。
// 比一个总开关多一层，因为榜是跨源合并计数的——只允许看番茄红果时，
// 不传 source 的那次"全部数据源"统计也必须只数它，否则未授权源的热度照样漏出去。
const settingPublicSources = "rank_public_sources"

// 公开榜的维度与条数上限：维度白名单在这里也钉一次，
// 免得以后有人往 boards 里加 chapter/media 就当"只是多个字段"
var boards = []struct {
	Dim    string `json:"-"`
	Title  string `json:"title"`
	Unit   string // 排名数字的单位（人 / 次）——两张榜口径不同，不标出来就会被统一读成「热度」
	Metric string
}{
	{Dim: "keyword", Title: "搜索热词榜", Unit: "次", Metric: "搜索次数"},
	{Dim: "book", Title: "阅读榜", Unit: "人", Metric: "访问人数（同数据源内按用户去重）"},
}

const boardLimit = 20

// sourceOption 数据源筛选项：code 用于查询、name 用于展示。
// 候选只取启用中的数据源——已下线的源即便窗口里还剩历史明细，也不该出现在下拉里，
// 否则用户会筛出一个永远点不开的选项。
type sourceOption struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type boardEntry struct {
	Name  string `json:"name"`
	Total int64  `json:"total"`
	// BookId 只有阅读榜有值（搜索榜没有书目标识可言）：让看到榜的人能**直接按 id 去检索这本书**，
	// 不必拿书名去撞重名。这不构成新的泄露面——同一批 bookId 本来就出现在登录用户可读的
	// 数据面响应里（搜索/详情结果的每条书目项自带 `bookId`），榜单只是把同一个标识提前给出来。
	// 名称与标识之外一律不给（没有 last_called_at、没有 sources、没有成功率和耗时），
	// 那几样才是能从明细反推到「谁在读」的东西。
	BookId string `json:"book_id,omitempty"`
}

type boardPayload struct {
	Dim    string       `json:"dim"`
	Title  string       `json:"title"`
	Metric string       `json:"metric"` // 口径写在数据里：面板换文案不用改端点，前端也不用猜
	Unit   string       `json:"unit"`   // 排名数字的单位（人 / 次），与 Metric 同义但短到能贴在数字后面
	Rows   []boardEntry `json:"rows"`
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
	admin := isAdmin(c)
	var allow []string // nil = 不限（管理员）；非 nil = 只统计这些源
	if !admin {
		allow = splitList(db.GetSetting(settingPublicSources))
		if len(allow) == 0 {
			auth.Fail(c, http.StatusForbidden, "榜单未对普通用户开放，可请管理员在「系统设置 · 站点」按数据源勾选")
			return
		}
	}

	days := subjectrank.NormalizeDays(queryInt(c, "days", 7))
	// 公开榜的时间窗口只允许 1/7/30 天：窗口越短，稀有词条越容易反推到具体某一个人
	switch days {
	case 1, 7, 30:
	default:
		days = 7
	}

	// 数据源筛选：同一个关键词在番茄小说与番茄听书里热度不同（fq_hg 是一站五形态），
	// 不给筛就没法判断该用哪个源去检索
	sources, err := enabledSources()
	if err != nil {
		auth.Fail(c, http.StatusInternalServerError, "数据源列表读取失败")
		return
	}
	sourceFilter := strings.TrimSpace(c.Query("source"))
	if sourceFilter != "" {
		known := false
		for _, o := range sources {
			if o.Code == sourceFilter {
				known = true
				break
			}
		}
		if !known {
			auth.Fail(c, http.StatusBadRequest, "source 不是启用中的数据源")
			return
		}
		if !sourceIn(allow, sourceFilter) {
			auth.Fail(c, http.StatusForbidden, "该数据源的榜单未开放")
			return
		}
	}
	// 媒介筛选：一站多形态（同一源的 Novel/Audio/Comic/Video）在榜上算同一个源，
	// 不给按媒介筛就只能看合并数。白名单取 base 的枚举——IsValidMedia 认空值，不能拿来校验入参
	mediaFilter := strings.TrimSpace(c.Query("media"))
	if mediaFilter != "" && !base.IsMediaValue(mediaFilter) {
		auth.Fail(c, http.StatusBadRequest, "media 不是合法媒介（novel/audio/comic/video）")
		return
	}

	// 非管理员看到的候选也只剩被开放的源——列未开放的源本身就是信息
	if allow != nil {
		filtered := make([]sourceOption, 0, len(allow))
		for _, o := range sources {
			if sourceIn(allow, o.Code) {
				filtered = append(filtered, o)
			}
		}
		sources = filtered
	}

	out := make([]boardPayload, 0, len(boards))
	for _, b := range boards {
		items, err := subjectrank.Query(b.Dim, days, sourceFilter, mediaFilter, boardLimit, allow)
		if err != nil {
			auth.Fail(c, http.StatusInternalServerError, "榜单统计失败")
			return
		}
		rows := make([]boardEntry, 0, len(items))
		for _, it := range items {
			if it.Name == "" {
				continue
			}
			rows = append(rows, boardEntry{Name: it.Name, Total: it.Total, BookId: it.BookID})
		}
		out = append(out, boardPayload{Dim: b.Dim, Title: b.Title, Metric: b.Metric, Unit: b.Unit, Rows: rows})
	}
	auth.Ok(c, gin.H{
		"days": days, "boards": out, "limit": boardLimit,
		"source": sourceFilter, "sources": sources, "unrestricted": admin,
		"media": mediaFilter, "medias": base.MediaCandidates(),
	})
}

// splitList 逗号分隔列表 → 去空去重切片；返回 nil 仅当输入为空（区分"不限"与"零个"由调用方决定）
func splitList(raw string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

func sourceIn(allow []string, code string) bool {
	if allow == nil {
		return true // 管理员不限
	}
	for _, a := range allow {
		if a == code {
			return true
		}
	}
	return false
}

// enabledSources 启用中的数据源（按 sort_order 排），供前端筛选下拉
func enabledSources() ([]sourceOption, error) {
	var rows []models.DataSource
	if err := db.DB.Where("status = ?", 1).Order("sort_order ASC, name ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]sourceOption, 0, len(rows))
	for _, r := range rows {
		out = append(out, sourceOption{Code: r.Name, Name: r.DisplayName})
	}
	return out, nil
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

// RegisterRoutes 挂载 /rank 路由（JWT 会话保护；放行范围由 rank_public_sources 按源决定）
func RegisterRoutes(r *gin.Engine) {
	g := r.Group("/rank")
	g.Use(auth.AuthRequired())
	g.GET("/boards", GetBoards)
}
