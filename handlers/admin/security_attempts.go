package admin

// 登录防爆破的当前状态：只读快照 + 定向解锁（待办清单 P40）。
//
// 为什么需要这两个端点：限频器（`handlers/auth` 的 ipAttemptLimiter）是纯进程内存，库里没有对应的表，
// 所以在它之前「谁被锁了、锁到几点」只能靠重启进程来回答——而重启会连带清掉所有人的锁并中断所有在途请求，
// 为了放行一个被误锁的 NAT 出口付出这些不划算。
//
// 这一对端点**不新增任何留存**：读的是内存，清的也是内存，重启后两边都归零。
// 所以响应里必须带上「读数只反映当前进程」这句话，否则空列表会被读成「最近没人爆破过」。

import (
	"net"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"loomproxy/handlers/auth"
)

// ListSecurityAttempts GET /admin/security/attempts —— 登录与找回密码限频器的当前条目，
// 外加一份**只读**的账号侧观察（`accounts`）。
//
// 为什么把两样东西放同一个端点：它们回答的是同一个问题的两半——
// 「这个 IP 现在的闸门状态」与「哪些账号被试得多、来自多少个不同 IP」。
// 后者是待办清单 P40① 唯一缺的数据（现网读数是 536 次失败/222 个 IP，按 IP 那道闸**分不出**
// "很多人各输错几次"与"分布式慢速爆破"），而它刻意**不带任何处置**：账号侧要不要闸门还没拍，
// 这里先把问句要的材料摆出来。
func ListSecurityAttempts(c *gin.Context) {
	auth.Ok(c, gin.H{
		"items":    auth.SecurityAttemptSnapshot(),
		"accounts": auth.LoginAccountWatch(),
		// 标黄阈值取**已有那一条定义**（`suspect_distinct_ips`：窗口内不同 IP 数达到它就算"分散"，只标红不处置）。
		// 面板不许自己抄一个 5——那是 P46/P53 反复在防的「两处写同一份值」，本轮新写的这一列不能刚写就犯。
		"accounts_meta": gin.H{"multi_ip_yellow": auth.SuspectDistinctIPs()},
		"note": "限频状态只存于当前进程内存：重启即清零，不入库；清空这里不等于解除 IP 黑名单（那在 /admin/blocked-ips）。" +
			"accounts 是**只读观察**：它不锁人、不减速、也不参与上面这些条目的判定——账号侧闸门还没拍（待办清单 P40①）。" +
			"「失败多」不等于「被攻击」：一个正常用户连错三次和一个脚本各错一次在这一列里长得一样，判定要连同 distinct_ips 一起看。",
	})
}

type resetAttemptRequest struct {
	IP string `json:"ip" binding:"required"`
}

// ResetSecurityAttempts POST /admin/security/attempts/reset —— 清掉某个 IP 的限频锁与失败连击。
//
// 用 POST 而不是 GET：这条有副作用。也不与 /admin/blocked-ips 的 DELETE 共用形状：
// 那是「从库里删一行」，这是「清进程内状态」，两个按钮的语义必须让读面板的人分得开。
func ResetSecurityAttempts(c *gin.Context) {
	var req resetAttemptRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		auth.Fail(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}
	ip := strings.TrimSpace(req.IP)
	if net.ParseIP(ip) == nil {
		auth.Fail(c, http.StatusBadRequest, "IP 地址不合法")
		return
	}
	auth.Ok(c, gin.H{
		"ip":      ip,
		"cleared": auth.ResetAttemptLock(ip),
	})
}
