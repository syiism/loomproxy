package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
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
	// json_object：合法 JSON 之外还要求「字符串到字符串的对象」。
	// 加这一档是因为 `verify_http_headers` 的**消费方**要的就是这个形状（`map[string]string`），
	// 而写入端原来只判"是不是合法 JSON"，中间没人负责——数组、裸字符串、值写成数字都能存进去，
	// 到发送端才被丢掉（P94）。type 是行为声明，所以判据要能对得上读的那一方。
	"json_object": true,
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

// normalizeJSONObjectValue 在合法 JSON 之上再要求「字符串到字符串的对象」（P94）。
//
// 判据为什么必须与消费方一致：`handlers/verify/sender.go` 把这个设置解成 `map[string]string`，
// 而数组 / 裸字符串 / 值写成数字或嵌套，**都能通过 `json.Compact` 却在解的时候失败**——
// 原实现没有失败分支，解不开就当没这个设置，请求带着空请求头照样打出去。
// 症状于是是「发码平台返回 401」（说的是令牌不对），而不是「你那段 JSON 我们没用上」。
//
// 留空合法：`{}` 与空串都表示"没有额外请求头"，这是 P62 那条"面板从此不能靠留空清空"里写明的合法形态。
func normalizeJSONObjectValue(v string) (string, error) {
	normalized, err := normalizeJSONValue(v)
	if err != nil || normalized == "" {
		return normalized, err
	}
	var kv map[string]string
	if err := json.Unmarshal([]byte(normalized), &kv); err != nil {
		// 刻意**不回显原值**：这一档当前只用在 `verify_http_headers` 上，而它是 P62 名单里的敏感键——
		// 把管理员贴进去的令牌抄进 400 响应，等于自己开一个绕开"只写不回显"的口子（响应会被面板渲染进 DOM）。
		// 按 P67 那对镜像：对外的句子不泄，给运维的归因必须有声——解析器原文进日志，不进响应。
		log.Printf("ERROR: 设置项值不是字符串到字符串的 JSON 对象，已拒绝保存：%v", err)
		return "", fmt.Errorf("必须是字符串到字符串的 JSON 对象（如 {\"authorization\":\"Bearer xx\"}）：" +
			"数组、裸字符串、值写成数字/布尔/嵌套都会被发送端丢掉请求头；本次未保存")
	}
	return normalized, nil
}

type createSettingRequest struct {
	Key         string `json:"key" binding:"required,max=64"`
	Value       string `json:"value" binding:"required"`
	Type        string `json:"type"`
	Description string `json:"description"`
}

// normalizeSettingValue 按**声明的 type** 校验并归一设置值。
//
// 原来只有 `json` 型有这道校验，其余类型是"存什么就是什么"：
// `bool` 能存进 `1`/`True`/`yes`，而读取侧那时还有两套判据（一处 `ToLower` 比较、几处精确 `== "true"`），
// 于是同一个值在两个开关上给出相反结果；`number` 能存进 `abc`，要到运行时才被解析器吞掉
// （待办清单 P60、P48 同族）。坏值留在库里只会等到运行时才炸——所以当场拒。
//
// `number` 这里**刻意不做范围判断**：0 与负数都是有意义的合法值
// （`jwt_expire_hours=-1` 是"永不过期"，限额类的 0 语义还等拍，见 P48②），
// 校验形态不校验语义，语义留给各键的读取端。
func normalizeSettingValue(settingType, raw string) (string, error) {
	switch settingType {
	case "json":
		return normalizeJSONValue(raw)
	case "json_object":
		return normalizeJSONObjectValue(raw)
	case "bool":
		v := strings.ToLower(strings.TrimSpace(raw))
		switch v {
		case "true", "false":
			return v, nil
		}
		return "", fmt.Errorf("bool 型设置只接受 true / false（当前值 %q）；`1`、`yes`、`on` 这类写法不会被读成「开启」", raw)
	case "number":
		v := strings.TrimSpace(raw)
		if _, err := strconv.Atoi(v); err != nil {
			return "", fmt.Errorf("number 型设置必须是整数（当前值 %q）", raw)
		}
		return v, nil
	default: // string：原样
		return raw, nil
	}
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
	// 值按声明的 type 校验与归一（`json` 在这一条里委托给 `normalizeJSONValue`，判据不变）
	normalized, err := normalizeSettingValue(settingType, req.Value)
	if err != nil {
		auth.Fail(c, http.StatusBadRequest, err.Error())
		return
	}
	req.Value = normalized

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
