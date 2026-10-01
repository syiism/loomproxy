package admin

import (
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"loomproxy/base"
	"loomproxy/db"
	"loomproxy/handlers/auth"
	"loomproxy/models"
)

// 回填一次的规模上限：明细表可能有几十万行，逐行反查+更新不能把请求线程占死。
// 超出的部分留给下一次（管理员多点一次即可，回填本身幂等）。
const (
	backfillScanCap = 5000
	backfillMaxDays = 90
	backfillDefDays = 30
)

// BackfillSubjectNames 按 (source, 标识) 用命名缓存回填调用明细里缺失的书名/章节名。
//
// 存在的原因：正文与目录请求只带 bookId/itemId，名称要靠进程内的「标识 → 名称」缓存反查
// （search/detail/chapter 响应才会把映射灌进去）。服务重启即清空缓存，那之后的正文调用
// 就只能记下标识、名称为空。等这本书的名字被别的调用重新带回缓存，这些行是可以补上的——
// 本端点就是做这件事，所以它是**幂等**的、可以反复跑。
//
// 补不到就留着：缓存里没有的映射不会凭空造出来，也不去猜。
// 只处理标识列存在之后的行（升级前的旧行标识与名称都是 NULL，无从回填）。
func BackfillSubjectNames(c *gin.Context) {
	days, _ := strconv.Atoi(c.DefaultQuery("days", strconv.Itoa(backfillDefDays)))
	if days < 1 || days > backfillMaxDays {
		days = backfillDefDays
	}
	from := time.Date(time.Now().Year(), time.Now().Month(), time.Now().Day(), 0, 0, 0, 0, time.Local).
		AddDate(0, 0, -(days - 1))

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
		Order("id ASC").Limit(backfillScanCap).
		Scan(&rows).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "数据库错误")
		return
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

	bookFilled, chapterFilled := int64(0), int64(0)
	err := db.DB.Transaction(func(tx *gorm.DB) error {
		for k, ids := range bookIDs {
			res := tx.Model(&models.ApiCallLog{}).Where("id IN ? AND (book_name IS NULL OR book_name = '')", ids).
				Update("book_name", k.name)
			if res.Error != nil {
				return res.Error
			}
			bookFilled += res.RowsAffected
		}
		for k, ids := range chapterIDs {
			res := tx.Model(&models.ApiCallLog{}).Where("id IN ? AND (chapter_title IS NULL OR chapter_title = '')", ids).
				Update("chapter_title", k.name)
			if res.Error != nil {
				return res.Error
			}
			chapterFilled += res.RowsAffected
		}
		return nil
	})
	if err != nil {
		log.Printf("ERROR: 回填调用明细名称失败: %v", err)
		auth.Fail(c, http.StatusInternalServerError, "回填失败")
		return
	}

	scanned := len(rows)
	log.Printf("回填调用明细名称：窗口 %d 天，扫描 %d 行，补书名 %d 处、补章节名 %d 处%s",
		days, scanned, bookFilled, chapterFilled,
		map[bool]string{true: "（达单次上限，可再跑一次）", false: ""}[scanned >= backfillScanCap])
	auth.Ok(c, gin.H{
		"days": days, "scanned": scanned,
		"book_filled": bookFilled, "chapter_filled": chapterFilled,
		"truncated": scanned >= backfillScanCap,
	})
}
