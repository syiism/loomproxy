package quota

// 转移历史的只读查看口（待办清单 P97 的「人也要能查」那一半）。
//
// 为什么需要它：转移改的是**永久**的日限额增量，不随每日刷新回退——
// 一个人如果把某个源全额转空，那之后**每天都是 0**，直到有人转回来。
// 半年后回来的人看到的症状是"这个源用不了"，最容易怀疑的是源坏了而不是自己当年的操作。
// 所以记录不能只写不读。

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"loomproxy/db"
	"loomproxy/handlers/auth"
	"loomproxy/models"
	"loomproxy/utils"
)

// MyQuotaTransfers 本人的转移历史。挂 in `/quota` 那组（三形态统一可用）：
// **只读**端点带 apiKey 不改变任何事实，与 dashboard / usage-logs 同一条口径。
//
// 不回 `operator_id` 的身份：本人需要知道的是"这次是我自己挪的还是管理员动的"，
// `via` 那一格就够了；把管理员账号名发给所有用户不是这件事的必要信息。
func MyQuotaTransfers(c *gin.Context) {
	userID, _ := c.Get("user_id")
	uid, _ := userID.(uint)

	page, pageSize := utils.Paginate(c.DefaultQuery("page", "1"), c.DefaultQuery("page_size", "10"), 10)
	q := db.DB.Model(&models.QuotaTransferLog{}).Where("user_id = ?", uid)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "读取失败")
		return
	}
	var rows []models.QuotaTransferLog
	if err := q.Order("id desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "读取失败")
		return
	}
	list := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		list = append(list, gin.H{
			"id":          r.ID,
			"from":        r.FromCode,
			"to":          r.ToCode,
			"amount":      r.Amount,
			"from_before": r.FromBefore,
			"from_after":  r.FromAfter,
			"to_before":   r.ToBefore,
			"to_after":    r.ToAfter,
			"via":         r.Via,
			"created_at":  r.CreatedAt,
		})
	}
	auth.Ok(c, gin.H{
		"list":      list,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"scope":     "转移改的是日限额的覆盖增量，**不随每日刷新回退**；要恢复只能再转回来",
	})
}
