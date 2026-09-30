package admin

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"loomproxy-go/db"
	"loomproxy-go/handlers/auth"
	"loomproxy-go/models"
)

// 关键设置项不允许删除
var protectedSettingKeys = map[string]bool{
	"register_enabled":   true,
	"default_role":       true,
	"default_quota_plan": true,
}

var settingTypes = map[string]bool{
	"string": true,
	"bool":   true,
	"number": true,
}

type createSettingRequest struct {
	Key         string `json:"key" binding:"required,max=64"`
	Value       string `json:"value" binding:"required"`
	Type        string `json:"type"`
	Description string `json:"description"`
}

// CreateSetting 创建设置项
func CreateSetting(c *gin.Context) {
	var req createSettingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		auth.Fail(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}

	settingType := req.Type
	if settingType == "" {
		settingType = "string"
	}
	if !settingTypes[settingType] {
		auth.Fail(c, http.StatusBadRequest, "type 必须是 string/bool/number 之一")
		return
	}

	var count int64
	if err := db.DB.Model(&models.SystemSetting{}).Where("`key` = ?", req.Key).Count(&count).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}
	if count > 0 {
		auth.Fail(c, http.StatusConflict, "设置项已存在")
		return
	}

	setting := models.SystemSetting{
		Key:         req.Key,
		Value:       req.Value,
		Type:        settingType,
		Description: req.Description,
	}
	if err := db.DB.Create(&setting).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "创建设置项失败")
		return
	}
	db.InvalidateSettingCache(req.Key)
	auth.Ok(c, setting)
}

// DeleteSetting 删除设置项（软删除，关键设置项禁止删除）
func DeleteSetting(c *gin.Context) {
	key := c.Param("key")

	if protectedSettingKeys[key] {
		auth.Fail(c, http.StatusBadRequest, "关键设置项不可删除")
		return
	}

	result := db.DB.Where("`key` = ?", key).Delete(&models.SystemSetting{})
	if result.Error != nil {
		auth.Fail(c, http.StatusInternalServerError, "删除失败")
		return
	}
	if result.RowsAffected == 0 {
		auth.Fail(c, http.StatusNotFound, "设置项不存在")
		return
	}
	db.InvalidateSettingCache(key)
	auth.Ok(c, gin.H{"message": "已删除"})
}
