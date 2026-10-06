package admin

import (
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"loomproxy/db"
	"loomproxy/handlers/auth"
	"loomproxy/models"
)

// ListBlockedIPs IP 黑名单列表（按 id 倒序）
func ListBlockedIPs(c *gin.Context) {
	var list []models.BlockedIP
	if err := db.DB.Order("id DESC").Find(&list).Error; err != nil {
		db.LogReadFail("blocked_ip_list:blocked_ips", err)
	}
	if list == nil {
		list = []models.BlockedIP{}
	}
	auth.Ok(c, list)
}

type addBlockedIPRequest struct {
	IP   string `json:"ip" binding:"required"`
	Note string `json:"note"`
}

// AddBlockedIP 拉黑 IP（防自锁：不能拉黑当前请求的客户端 IP）
func AddBlockedIP(c *gin.Context) {
	var req addBlockedIPRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		auth.Fail(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}

	raw := strings.TrimSpace(req.IP)
	parsed := net.ParseIP(raw)
	if parsed == nil {
		auth.Fail(c, http.StatusBadRequest, "IP 格式不正确")
		return
	}
	ip := parsed.String()

	if ip == c.ClientIP() {
		auth.Fail(c, http.StatusBadRequest, "不能拉黑当前使用的 IP")
		return
	}

	var count int64
	if err := db.DB.Model(&models.BlockedIP{}).Where("ip = ?", ip).Count(&count).Error; err != nil {
		db.LogReadFail("blocked_ip_dupcheck:blocked_ips", err)
	}
	if count > 0 {
		auth.Fail(c, http.StatusConflict, "该 IP 已在黑名单中")
		return
	}

	entry := models.BlockedIP{
		IP:     ip,
		Note:   strings.TrimSpace(req.Note),
		Source: "manual",
	}
	if err := db.DB.Create(&entry).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "添加失败")
		return
	}
	db.InvalidateBlockedIPCache()
	auth.Ok(c, entry)
}

// RemoveBlockedIP 解除拉黑
func RemoveBlockedIP(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		auth.Fail(c, http.StatusBadRequest, "参数错误")
		return
	}

	result := db.DB.Delete(&models.BlockedIP{}, uint(id))
	if result.Error != nil {
		auth.Fail(c, http.StatusInternalServerError, "移除失败")
		return
	}
	if result.RowsAffected == 0 {
		auth.Fail(c, http.StatusNotFound, "记录不存在")
		return
	}
	db.InvalidateBlockedIPCache()
	auth.Ok(c, gin.H{"message": "已解除"})
}
