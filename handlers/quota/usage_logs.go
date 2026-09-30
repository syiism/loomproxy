package quota

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"loomproxy/db"
	"loomproxy/handlers/auth"
	"loomproxy/models"
)

// MyUsageLogs 当前登录用户自己的额度使用流水（按 id 倒序分页）
func MyUsageLogs(c *gin.Context) {
	userID, _ := c.Get("user_id")

	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "10"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 10
	}

	q := db.DB.Model(&models.QuotaUsageLog{}).Where("user_id = ?", userID)
	if group := c.Query("group"); group != "" {
		q = q.Where("group_code = ?", group)
	}

	var total int64
	q.Count(&total)

	var logs []models.QuotaUsageLog
	q.Order("id DESC").
		Offset((page - 1) * pageSize).Limit(pageSize).Find(&logs)

	list := make([]gin.H, 0, len(logs))
	for _, l := range logs {
		list = append(list, gin.H{
			"id":         l.ID,
			"group_code": l.GroupCode,
			"interface":  l.Interface,
			"cost":       l.Cost,
			"created_at": l.CreatedAt,
		})
	}

	auth.Ok(c, gin.H{
		"list":      list,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}
