package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"loomproxy/db"
	"loomproxy/handlers/auth"
	"loomproxy/models"
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
	"json":   true,
}

// normalizeJSONValue 校验并压缩 JSON 型设置值：留空合法（表示该能力未启用），
// 非空必须是合法 JSON，落库前压成单行。
//
// 用 json.Compact 而不是 Unmarshal+Marshal：它只吃掉 token 之间的空白，
// 字符串内容与转义原样保留——不会把中文转成 \uXXXX，也不会重排键序，
// 因此「压缩后的值」与用户写的值语义完全一致。
// 它同时是这道校验的意义所在：字符串里出现裸换行（多行粘贴最容易产生这种值）
// 会被判非法并拒绝，避免坏值一路滑到下游才暴露。
func normalizeJSONValue(v string) (string, error) {
	trimmed := strings.TrimSpace(v)
	if trimmed == "" {
		return "", nil
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(trimmed)); err != nil {
		return "", fmt.Errorf("不是合法 JSON：%s（字符串内的换行要写成 \\\\n，整段建议压成单行）", err.Error())
	}
	return buf.String(), nil
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
		auth.Fail(c, http.StatusBadRequest, "type 必须是 string/bool/number/json 之一")
		return
	}
	if settingType == "json" {
		normalized, err := normalizeJSONValue(req.Value)
		if err != nil {
			auth.Fail(c, http.StatusBadRequest, err.Error())
			return
		}
		req.Value = normalized
	}

	var count int64
	if err := db.DB.Model(&models.SystemSetting{}).Where(map[string]interface{}{"key": req.Key}).Count(&count).Error; err != nil {
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

	result := db.DB.Where(map[string]interface{}{"key": key}).Delete(&models.SystemSetting{})
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
