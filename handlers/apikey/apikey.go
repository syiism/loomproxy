// Package apikey 用户自助 API 密钥管理（/apikey，JWT 会话保护）。
// 密钥用于网关数据源接口的程序化访问：请求归属创建者（计费/配额/监控按用户统计），
// 不适用于面板会话类接口（/auth/*、/admin/* 仍仅 JWT）。明文仅创建时返回一次。
package apikey

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"loomproxy/db"
	"loomproxy/handlers/auth"
	"loomproxy/models"
	"loomproxy/utils"
)

const maxKeysPerUser = 10

func ok(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, gin.H{"code": 0, "msg": "ok", "data": data})
}

func errResp(c *gin.Context, status int, msg string) {
	c.JSON(status, gin.H{"code": -1, "msg": msg})
}

func uidOf(c *gin.Context) uint {
	if v, exists := c.Get("user_id"); exists {
		if id, isUint := v.(uint); isUint {
			return id
		}
	}
	return 0
}

// Create 创建密钥：{name}，明文仅本次响应返回一次。
func Create(c *gin.Context) {
	uid := uidOf(c)
	name := strings.TrimSpace(c.PostForm("name"))
	if name == "" {
		var body struct {
			Name string `json:"name"`
		}
		if err := c.ShouldBindJSON(&body); err == nil {
			name = strings.TrimSpace(body.Name)
		}
	}
	if name == "" {
		name = "default"
	}
	if len([]rune(name)) > 64 {
		errResp(c, http.StatusBadRequest, "备注最长 64 字符")
		return
	}

	var count int64
	if err := db.DB.Model(&models.ApiKey{}).Where("user_id = ?", uid).Count(&count).Error; err != nil {
		errResp(c, http.StatusInternalServerError, "查询密钥数量失败")
		return
	}
	if count >= maxKeysPerUser {
		errResp(c, http.StatusBadRequest, "每个用户最多创建 10 个 API 密钥")
		return
	}

	plain, err := utils.GenerateApiKey()
	if err != nil {
		errResp(c, http.StatusInternalServerError, "生成密钥失败")
		return
	}
	ak := models.ApiKey{UserID: uid, Key: plain, Name: name}
	if err := db.DB.Create(&ak).Error; err != nil {
		errResp(c, http.StatusInternalServerError, "保存密钥失败")
		return
	}
	ok(c, gin.H{
		"id":   ak.ID,
		"name": ak.Name,
		"key":  plain,
		"hint": "密钥可随时在列表中查看；泄露请撤销后重新创建",
	})
}

// List 当前用户的密钥列表（掩码展示，无明文）。
func List(c *gin.Context) {
	var keys []models.ApiKey
	if err := db.DB.Where("user_id = ?", uidOf(c)).Order("created_at DESC").Find(&keys).Error; err != nil {
		errResp(c, http.StatusInternalServerError, "查询密钥失败")
		return
	}
	if keys == nil {
		keys = []models.ApiKey{}
	}
	ok(c, keys)
}

// Revoke 撤销（物理删除，仅限本人）。
func Revoke(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		errResp(c, http.StatusBadRequest, "无效的密钥 ID")
		return
	}
	var ak models.ApiKey
	if err := db.DB.Where("id = ? AND user_id = ?", id, uidOf(c)).First(&ak).Error; err != nil {
		errResp(c, http.StatusNotFound, "密钥不存在")
		return
	}
	if err := db.DB.Delete(&ak).Error; err != nil {
		errResp(c, http.StatusInternalServerError, "撤销失败")
		return
	}
	utils.InvalidateApiKeyCache(ak.Key)
	ok(c, gin.H{"revoked": true})
}

// RegisterRoutes 注册 /apikey 路由（JWT 会话保护，面板自助管理）。
func RegisterRoutes(r *gin.Engine) {
	g := r.Group("/apikey")
	g.Use(auth.AuthRequired())
	g.POST("", Create)
	g.GET("", List)
	g.DELETE("/:id", Revoke)
}
