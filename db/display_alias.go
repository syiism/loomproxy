package db

// 显示别名的读取侧（待办清单 P43）。放在 db 是因为 models 不能反向依赖 db（会成环），
// 而 `models.User.Public()` 需要拿到别名才能算出显示名——所以由调用方读出来再传进去。

import (
	"log"

	"loomproxy/models"
)

// DisplayAliasesFor 某用户的全部显示别名，键格式见 `models.DisplayAliasKey`。
// 读失败返回空表：**别名是锦上添花，缺了退回默认名就是正确行为**，不能因为它读不到就不返回用户。
func DisplayAliasesFor(userID uint) map[string]string {
	m := map[string]string{}
	var rows []models.UserDisplayAlias
	if err := DB.Where("user_id = ?", userID).Find(&rows).Error; err != nil {
		log.Printf("ERROR: 读取显示别名失败 user_id=%d: %v", userID, err)
		return m
	}
	for _, r := range rows {
		m[models.DisplayAliasKey(r.Kind, r.TargetID)] = r.Alias
	}
	return m
}

// DisplayAliasesForUsers 批量版：管理员用户列表一页几十个用户，逐个查就是 N+1。
// 返回 userID → (别名键 → 别名)；没有别名的用户不出现在结果里（调用方按缺省处理）。
func DisplayAliasesForUsers(userIDs []uint) map[uint]map[string]string {
	out := map[uint]map[string]string{}
	if len(userIDs) == 0 {
		return out
	}
	var rows []models.UserDisplayAlias
	if err := DB.Where("user_id IN ?", userIDs).Find(&rows).Error; err != nil {
		log.Printf("ERROR: 批量读取显示别名失败（%d 个用户）: %v", len(userIDs), err)
		return out
	}
	for _, r := range rows {
		if out[r.UserID] == nil {
			out[r.UserID] = map[string]string{}
		}
		out[r.UserID][models.DisplayAliasKey(r.Kind, r.TargetID)] = r.Alias
	}
	return out
}
