package auth

// 套餐名 / 角色名的**用户维度显示别名**（待办清单 P43）。
//
// 三条边界写在这里，改代码的人不必再去别处找：
//  1. 默认名那张全局表（`quota_plans.name` / `roles.name`，都带 uniqueIndex）一概不动——
//     就地改名是替所有人和每张卡密批次改称呼。这里只存「这个人希望它显示成什么」。
//  2. 目标必须是**该用户当前持有的**套餐/角色：不给「随便挑一个 plan_id 起名」留口子，
//     也让「换套餐后旧别名不被读到」自动成立（键里带着 target_id，见 models.DisplayAliasKey）。
//  3. 资格 = 绑定了非免费套餐（`gate.ResolvePlan` 判，含到期回退）。
//     免费档没什么可称呼，放开只会让面板上一片自起名。

import (
	"net/http"
	"unicode"

	"github.com/gin-gonic/gin"

	"loomproxy/db"
	"loomproxy/gate"
	"loomproxy/models"
)

type displayAliasRequest struct {
	Kind     string `json:"kind" binding:"required"` // plan / role
	TargetID uint   `json:"target_id" binding:"required"`
	// Alias 空串 = 清除这条覆盖（回到默认名）。不叫 "delete"：一个写入口一种语义，别长两个动作。
	Alias string `json:"alias"`
}

// cleanAlias 去首尾空白与控制字符，长度按 **rune** 判（中文一个字形 3 字节，按字节限长会误伤）。
// 列宽按最坏形态定在 models.UserDisplayAlias（32 rune → 96 字节），而 SQLite 不检查列宽、只有 MySQL 会拒
// ——所以长度必须在进 DB 之前判掉（§10「列宽按用户输入的最坏形态定」那条）。
//
// 返回的第二个值是「太长了」：**不能把超长静默清成空串**，空串在这条端点上是「清除覆盖」的语义，
// 于是一次手滑输入会把「我在改别名」变成「我把别名删了」，而两边都回 200。
func cleanAlias(raw string) (string, bool) {
	t := trimSpaceAndControls(raw)
	return t, len([]rune(t)) > models.AliasMaxRunes
}

func trimSpaceAndControls(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if unicode.IsControl(r) {
			continue
		}
		out = append(out, r)
	}
	return string(dropEdges(out))
}

func dropEdges(r []rune) []rune {
	start, end := 0, len(r)
	for start < end && (r[start] == ' ' || r[start] == '\t') {
		start++
	}
	for end > start && (r[end-1] == ' ' || r[end-1] == '\t') {
		end--
	}
	return r[start:end]
}

// UpdateDisplayAlias 设置或清除本人对某个套餐/角色的显示名。
//
// 只认会话（与 `/auth/privacy` 同一组）：别名是「这个人怎么被称呼」的自我表达，
// 让长期密钥能替它改，等于密钥泄露时攻击者可以改走受害者的显示名，而这件事在会话列表里看不见。
func UpdateDisplayAlias(c *gin.Context) {
	var req displayAliasRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}
	if req.Kind != models.DisplayKindPlan && req.Kind != models.DisplayKindRole {
		fail(c, http.StatusBadRequest, "kind 只支持 plan / role")
		return
	}

	userID, _ := c.Get("user_id")
	var user models.User
	if err := db.DB.Preload("Roles").Preload("Plan").First(&user, userID).Error; err != nil {
		fail(c, http.StatusNotFound, "用户不存在")
		return
	}

	plan := gate.ResolvePlan(&user)
	if plan.Code == "" || plan.Code == "free" {
		fail(c, http.StatusForbidden, "只有绑定了付费套餐的账号可以自定义显示名")
		return
	}

	if !userHolds(&user, req.Kind, req.TargetID) {
		fail(c, http.StatusBadRequest, "只能给当前自己的套餐或角色起别名")
		return
	}

	alias, tooLong := cleanAlias(req.Alias)
	if tooLong {
		fail(c, http.StatusBadRequest, "别名过长（上限 32 个字符）")
		return
	}

	if alias == "" {
		// 清除覆盖：没有这一行就是「没改过」，读侧退回默认名。删不到东西不算错误。
		if err := db.DB.Where("user_id = ? AND kind = ? AND target_id = ?", user.ID, req.Kind, req.TargetID).
			Delete(&models.UserDisplayAlias{}).Error; err != nil {
			fail(c, http.StatusInternalServerError, "清除失败")
			return
		}
		ok(c, gin.H{"kind": req.Kind, "target_id": req.TargetID, "alias": "", "cleared": true})
		return
	}

	row := models.UserDisplayAlias{UserID: user.ID, Kind: req.Kind, TargetID: req.TargetID, Alias: alias}
	if err := saveDisplayAlias(row); err != nil {
		fail(c, http.StatusInternalServerError, "保存失败")
		return
	}

	// 回读一遍再返回：写进去的是不是那句「你好，世界」，以库里的值为准，不以入参为准
	// （v66 那条 gorm 默认值吞零值的坑就是这么被抓出来的）
	var stored models.UserDisplayAlias
	if err := db.DB.Where("user_id = ? AND kind = ? AND target_id = ?", user.ID, req.Kind, req.TargetID).
		First(&stored).Error; err != nil {
		fail(c, http.StatusInternalServerError, "保存失败")
		return
	}
	ok(c, gin.H{"kind": req.Kind, "target_id": req.TargetID, "alias": stored.Alias})
}

// saveDisplayAlias 有则改、无则插（唯一键是 (user_id, kind, target_id)）
func saveDisplayAlias(row models.UserDisplayAlias) error {
	var existing models.UserDisplayAlias
	err := db.DB.Where("user_id = ? AND kind = ? AND target_id = ?", row.UserID, row.Kind, row.TargetID).
		First(&existing).Error
	if err == nil {
		return db.DB.Model(&existing).Update("alias", row.Alias).Error
	}
	return db.DB.Create(&row).Error
}

// userHolds 目标是不是这个用户当前持有的那份套餐 / 那批角色之一
func userHolds(user *models.User, kind string, targetID uint) bool {
	switch kind {
	case models.DisplayKindPlan:
		plan := gate.ResolvePlan(user)
		return plan.ID != 0 && plan.ID == targetID
	case models.DisplayKindRole:
		for _, r := range user.Roles {
			if r.ID == targetID {
				return true
			}
		}
	}
	return false
}
