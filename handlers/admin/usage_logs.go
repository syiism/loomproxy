package admin

import (
	"github.com/gin-gonic/gin"

	"loomproxy/db"
	"loomproxy/handlers/auth"
	"loomproxy/models"
	"loomproxy/utils"
)

// ListUsageLogs 额度使用流水（只读，按 id 倒序分页）
func ListUsageLogs(c *gin.Context) {
	page, pageSize := utils.Paginate(c.DefaultQuery("page", "1"), c.DefaultQuery("page_size", "20"), 20)

	q := db.DB.Model(&models.QuotaUsageLog{})
	if username := c.Query("username"); username != "" {
		q = q.Joins("JOIN users ON users.id = quota_usage_logs.user_id AND "+likeESCAPE("users.username"), "%"+escapeLike(username)+"%")
	}
	if group := c.Query("group"); group != "" {
		q = q.Where("group_code = ?", group)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.LogReadFail("admin_usage_total:quota_usage_logs", err)
	}

	var logs []models.QuotaUsageLog
	if err := q.Order("id DESC").
		Offset((page - 1) * pageSize).Limit(pageSize).Find(&logs).Error; err != nil {
		db.LogReadFail("admin_usage_list:quota_usage_logs", err)
	}

	// 批量取用户名，避免逐条查询
	userIDs := make([]uint, 0, len(logs))
	seen := make(map[uint]bool)
	for _, l := range logs {
		if !seen[l.UserID] {
			seen[l.UserID] = true
			userIDs = append(userIDs, l.UserID)
		}
	}
	usernames := make(map[uint]string)
	if len(userIDs) > 0 {
		var users []models.User
		if err := db.DB.Select("id", "username").Where("id IN ?", userIDs).Find(&users).Error; err != nil {
			db.LogReadFail("admin_usage_usernames:users", err)
		}
		for _, u := range users {
			usernames[u.ID] = u.Username
		}
	}

	list := make([]gin.H, 0, len(logs))
	for _, l := range logs {
		list = append(list, gin.H{
			"id":         l.ID,
			"user_id":    l.UserID,
			"username":   usernames[l.UserID],
			"group_code": l.GroupCode,
			"interface":  l.Interface,
			"cost":       l.Cost,
			"created_at": l.CreatedAt,
		})
	}

	// 收集数据源标识去重列表（从数据源表获取，不依赖流水）
	var dsList []models.DataSource
	if err := db.DB.Select("name").Where("status = 1").Find(&dsList).Error; err != nil {
		db.LogReadFail("admin_usage_sources:data_sources", err)
	}
	sourceCodes := make([]string, 0, len(dsList))
	for _, ds := range dsList {
		sourceCodes = append(sourceCodes, ds.Name)
	}

	auth.Ok(c, gin.H{
		"list":         list,
		"total":        total,
		"page":         page,
		"page_size":    pageSize,
		"source_codes": sourceCodes,
	})
}
