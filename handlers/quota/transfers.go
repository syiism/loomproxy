package quota

// 转移历史的只读查看口（待办清单 P97 的「人也要能查」那一半）。
//
// 为什么需要它：转移改的是**永久**的日限额增量，不随每日刷新回退——
// 一个人如果把某个源全额转空，那之后**每天都是 0**，直到有人转回来。
// 半年后回来的人看到的症状是"这个源用不了"，最容易怀疑的是源坏了而不是自己当年的操作。
// 所以记录不能只写不读。

import (
	"log"
	"net/http"
	"time"

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
	// 本人视角只看"没被自己清掉"的那些（P119 ④：清除是软删，行仍在库里、管理端仍可查）。
	// `IS NULL` 三种方言都认，不写反引号（判据页那条 PostgreSQL 只认双引号）。
	q := db.DB.Model(&models.QuotaTransferLog{}).
		Where("user_id = ? AND cleared_at IS NULL", uid)
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

// ClearMyQuotaTransfers 把**本人**的转移记录一次性标记为已清除（`DELETE /quota/transfers`）。
//
// 三条口径写在这里而不是散在调用方：
//   - **只认会话**：这条挂在 `/quota` 那组外面（与 POST /quota/transfer 同一形状，§7 那两张脸的教训）。
//     长期密钥不该能销毁审计面留下的痕迹——它能读，不能清。
//   - **软删**：`UPDATE ... SET cleared_at = now`，行不删。审计面（管理端列表）不过滤，
//     所以"清除"的效果是"本人不再看见"，不是"这件事没发生过"。
//   - **只动自己的行**：`user_id = ?` 是唯一的归属条件，越权的形状在这里应当是 0 行而不是报错。
//
// 返回清除的笔数：0 是合法读数（已经清过、或本来就没有），不报错也不假装成功做了什么。
func ClearMyQuotaTransfers(c *gin.Context) {
	userID, _ := c.Get("user_id")
	uid, _ := userID.(uint)
	if uid == 0 {
		auth.Fail(c, http.StatusUnauthorized, "未登录")
		return
	}
	res := db.DB.Model(&models.QuotaTransferLog{}).
		Where("user_id = ? AND cleared_at IS NULL", uid).
		Updates(map[string]interface{}{"cleared_at": time.Now()})
	if res.Error != nil {
		// 给用户固定句、给服务端完整原因（§10 那条镜像：给用户的不泄，给运维的有声）
		log.Printf("ERROR: 清除本人转移记录失败 user=%d: %v", uid, res.Error)
		auth.Fail(c, http.StatusInternalServerError, "清除失败")
		return
	}
	auth.Ok(c, gin.H{"cleared": res.RowsAffected})
}
