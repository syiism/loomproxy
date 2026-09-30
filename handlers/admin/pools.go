package admin

import (
	"github.com/gin-gonic/gin"

	"loomproxy/base/pool"
	"loomproxy/handlers/auth"
)

// ListPools 号池状态快照：各数据源登记的号池（活跃/冷备/周期耗尽/死号 + 脱敏号明细）。
// 底座项目无数据源时返回空列表。
func ListPools(c *gin.Context) {
	auth.Ok(c, pool.StatusAll())
}
