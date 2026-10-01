package admin

// 公开榜单的按源放行名单。存储刻意留在系统设置 rank_public_sources（逗号分隔的数据源码）：
// handlers/rank 与设置页都只认这个 key，数据源列表里的逐源开关只是把它拆成更好按的按钮，
// 不新增第二份真相。名单里源码的先后顺序沿用设置项原值，新放行的追加在末尾。

import (
	"errors"
	"strings"

	"gorm.io/gorm"

	"loomproxy/db"
	"loomproxy/models"
)

const rankPublicSettingKey = "rank_public_sources"

// rankPublicSet 当前放行名单（源码 → 是否可见）
func rankPublicSet() map[string]bool {
	out := map[string]bool{}
	for _, part := range strings.Split(db.GetSetting(rankPublicSettingKey), ",") {
		if code := strings.TrimSpace(part); code != "" {
			out[code] = true
		}
	}
	return out
}

// setSourceRankPublic 增删名单里的一个源码，返回写回后的整串值
func setSourceRankPublic(code string, enable bool) (string, error) {
	var setting models.SystemSetting
	err := db.DB.Where(map[string]interface{}{"key": rankPublicSettingKey}).First(&setting).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", err
	}

	var kept []string
	seen := map[string]bool{}
	for _, part := range strings.Split(setting.Value, ",") {
		c := strings.TrimSpace(part)
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		if c == code && !enable {
			continue
		}
		kept = append(kept, c)
	}
	if enable && !seen[code] {
		kept = append(kept, code)
	}
	value := strings.Join(kept, ",")

	if errors.Is(err, gorm.ErrRecordNotFound) {
		// seed 一定播种过这个 key；走到这里说明是被人工删掉了，补一行回来
		setting = models.SystemSetting{
			Key:         rankPublicSettingKey,
			Value:       value,
			Type:        "string",
			Description: "允许普通用户查看排行榜的数据源，逗号分隔；留空=不对普通用户开放任何榜单。管理员不受此名单限制",
		}
		if err := db.DB.Create(&setting).Error; err != nil {
			return "", err
		}
	} else if err := db.DB.Model(&models.SystemSetting{}).
		Where(map[string]interface{}{"key": rankPublicSettingKey}).
		Update("value", value).Error; err != nil {
		return "", err
	}
	db.InvalidateSettingCache(rankPublicSettingKey)
	return value, nil
}
