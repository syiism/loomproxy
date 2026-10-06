package quota

// 额度转移的**本人侧入口**（待办清单 P97 落地）。管理端那一个在 handlers/admin，
// 两个入口共用 `gate.TransferQuota`——四条边界只在一个地方判（§9「判定只有一个入口」）。

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"

	"loomproxy/gate"
	"loomproxy/handlers/auth"
)

type transferRequest struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Amount int64  `json:"amount"`
}

func transferResponse(c *gin.Context, res *gate.TransferResult) {
	auth.Ok(c, gin.H{
		"amount": res.Amount,
		"from":   gin.H{"code": res.FromCode, "before": res.FromBefore, "after": res.FromAfter},
		"to":     gin.H{"code": res.ToCode, "before": res.ToBefore, "after": res.ToAfter},
		"scope":  "转移的是当日限额本身；已用量与流水不回改，新限额从下一次调用起生效",
	})
}

// TransferMyQuota 本人在自己的两个源之间转移额度。
//
// **只认会话**（挂在一个不带 apiKey 的独立注册上，不是 `/quota` 那组）：
// 理由与 P106 的清零模式同一句——长期密钥不该改动额度的分布形态。
// 转移不跨账户：覆盖行的键是 `(user_id, 源)`，`user_id` 恒取会话里的本人，请求体里没有"目标用户"这一格。
func TransferMyQuota(c *gin.Context) {
	var req transferRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		log.Printf("参数绑定失败（%s）: %v", c.Request.URL.Path, err)
		auth.Fail(c, http.StatusBadRequest, "参数错误")
		return
	}
	userID, _ := c.Get("user_id")
	uid, _ := userID.(uint)
	res, err := gate.TransferQuota(uid, uid, req.From, req.To, req.Amount, "self")
	if err != nil {
		status, msg := gate.TransferErrorStatus(err)
		if status == http.StatusInternalServerError {
			log.Printf("ERROR: 额度转移失败 user=%d %s→%s n=%d: %v", uid, req.From, req.To, req.Amount, err)
		} else {
			log.Printf("WARN: 额度转移被拒 user=%d %s→%s n=%d: %v", uid, req.From, req.To, req.Amount, err)
		}
		auth.Fail(c, status, msg)
		return
	}
	log.Printf("额度转移（本人）user=%d %s→%s n=%d", uid, req.From, req.To, req.Amount)
	transferResponse(c, res)
}
