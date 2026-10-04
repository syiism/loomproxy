package admin

import (
	"log"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"loomproxy/base"
	"loomproxy/db"
	"loomproxy/handlers/auth"
	"loomproxy/models"
	"loomproxy/utils"
)

// 回填一次的规模上限：明细表可能有几十万行，逐行反查+更新不能把请求线程占死。
// 超出的部分留给下一轮（回填本身幂等，定时循环会自动接着捞）。
const (
	BackfillScanCap     = 5000
	BackfillMaxDays     = 90
	BackfillDefaultDays = 30
)

// BackfillSummary 一轮回填的产出——HTTP 响应与定时循环的日志共用同一个形状。
type BackfillSummary struct {
	Days          int   `json:"days"`
	Scanned       int   `json:"scanned"`
	BookFilled    int64 `json:"book_filled"`
	ChapterFilled int64 `json:"chapter_filled"`
	// Truncated 扫到了单次上限：库里还有没捞完的行，下一轮接着捞
	Truncated bool `json:"truncated"`
}

// RunSubjectBackfill 回填的主体（幂等，可反复调用）：HTTP 手动入口与 app 侧的定时循环共用它。
//
// 存在的原因：正文与目录请求只带 bookId/itemId，名称要靠「标识 → 名称」缓存反查
// （search/detail/chapter 响应才会把映射灌进去）。缓存接了 Redis 才跨重启，而**重启之后**
// 第一批只带标识的调用仍然只记下标识、名称为空。等这本书的名字被别的调用重新带回缓存，
// 这些行是可以补上的——本函数就是做这件事。
//
// 补不到就留着：缓存里没有的映射不会凭空造出来，也不去猜。
// 只处理标识列存在之后的行：拿标识与空串比较时，NULL 那一边是 unknown，
// 升级前的旧行（标识列为 NULL）因此天然被排除在外，不需要另写 IS NOT NULL。
//
// 扫描按 id **倒序**（取最近的上限行）：榜、覆盖率、明细页读的都是近窗口，补不到的一定要是
// 没人再看的旧行，而不是新行——旧代码的 `id ASC` 配上定时循环，等于让一批永远反查不到的
// 陈旧行长期霸占整个扫描窗口，新行一条也补不进去。
func RunSubjectBackfill(days int) (BackfillSummary, error) {
	summary := BackfillSummary{Days: days}
	if days < 1 || days > BackfillMaxDays {
		summary.Days = BackfillDefaultDays
		days = BackfillDefaultDays
	}
	from := utils.DayStart(days)

	type row struct {
		ID           uint
		Source       string
		BookIdent    string
		ChapterIdent string
	}
	var rows []row
	if err := db.DB.Model(&models.ApiCallLog{}).
		Select("id, source, book_ident, chapter_ident").
		Where("created_at >= ?", from).
		Where("(book_ident <> '' AND (book_name IS NULL OR book_name = '')) OR " +
			"(chapter_ident <> '' AND (chapter_title IS NULL OR chapter_title = ''))").
		Order("id DESC").Limit(BackfillScanCap).
		Scan(&rows).Error; err != nil {
		return summary, err
	}

	// 按 (source, 标识, 名称) 归并再批量更新：同一本书往往缺名几十上百行，逐行 UPDATE 太碎
	type key struct{ source, ident, name string }
	bookIDs := map[key][]uint{}
	chapterIDs := map[key][]uint{}
	for _, r := range rows {
		if r.BookIdent != "" { // 标识在 CallSubject.Normalize 里已截到 512，这里不再复查长度
			if name, _ := base.LookupBook(r.Source, r.BookIdent); name != "" {
				k := key{r.Source, r.BookIdent, name}
				bookIDs[k] = append(bookIDs[k], r.ID)
			}
		}
		if r.ChapterIdent != "" {
			if title := base.LookupChapter(r.Source, r.ChapterIdent); title != "" {
				k := key{r.Source, r.ChapterIdent, title}
				chapterIDs[k] = append(chapterIDs[k], r.ID)
			}
		}
	}

	err := db.DB.Transaction(func(tx *gorm.DB) error {
		for k, ids := range bookIDs {
			res := tx.Model(&models.ApiCallLog{}).Where("id IN ? AND (book_name IS NULL OR book_name = '')", ids).
				Update("book_name", k.name)
			if res.Error != nil {
				return res.Error
			}
			summary.BookFilled += res.RowsAffected
		}
		for k, ids := range chapterIDs {
			res := tx.Model(&models.ApiCallLog{}).Where("id IN ? AND (chapter_title IS NULL OR chapter_title = '')", ids).
				Update("chapter_title", k.name)
			if res.Error != nil {
				return res.Error
			}
			summary.ChapterFilled += res.RowsAffected
		}
		return nil
	})
	summary.Scanned = len(rows)
	summary.Truncated = len(rows) >= BackfillScanCap
	return summary, err
}

// BackfillSubjectNames 手动入口（`POST /admin/monitor/backfill-subjects?days=`）。
// 定时循环才是默认路径（`MONITOR_BACKFILL_SEC`），这个入口留给「刚导完缓存想立刻补一次」的场合。
func BackfillSubjectNames(c *gin.Context) {
	days, _ := strconv.Atoi(c.DefaultQuery("days", strconv.Itoa(BackfillDefaultDays)))
	summary, err := RunSubjectBackfill(days)
	if err != nil {
		log.Printf("ERROR: 回填调用明细名称失败: %v", err)
		auth.Fail(c, http.StatusInternalServerError, "回填失败")
		return
	}
	log.Printf("回填调用明细名称：窗口 %d 天，扫描 %d 行，补书名 %d 处、补章节名 %d 处%s",
		summary.Days, summary.Scanned, summary.BookFilled, summary.ChapterFilled,
		map[bool]string{true: "（达单次上限，下一轮接着捞）", false: ""}[summary.Truncated])
	auth.Ok(c, summary)
}
