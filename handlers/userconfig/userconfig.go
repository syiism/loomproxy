package userconfig

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"loomproxy-go/db"
	"loomproxy-go/gate"
	"loomproxy-go/handlers/auth"
	"loomproxy-go/models"
)

type sourceConfigItem struct {
	SourceName    string `json:"source_name"`
	SourceDisplay string `json:"source_display"`
	BaseURL       string `json:"base_url"`
}

// ListSourceConfigs 返回当前用户可访问的数据源配置（仅套餐包含的，管理员显示全部）
func ListSourceConfigs(c *gin.Context) {
	userID, _ := c.Get("user_id")
	uid, _ := userID.(uint)

	var configs []models.UserSourceConfig
	db.DB.Where("user_id = ?", uid).Find(&configs)

	configMap := make(map[string]string)
	for _, cfg := range configs {
		configMap[cfg.SourceName] = cfg.BaseURL
	}

	// 1. 查询所有启用的数据源
	var dataSources []models.DataSource
	db.DB.Where("status = 1").Order("sort_order ASC, id ASC").Find(&dataSources)

	// 2. 管理员显示全部，普通用户根据套餐过滤
	var user models.User
	if err := db.DB.Preload("Roles").Preload("Plan").First(&user, uid).Error; err == nil {
		isAdmin := false
		for _, r := range user.Roles {
			if r.Code == "admin" {
				isAdmin = true
				break
			}
		}
		if !isAdmin {
			plan := gate.ResolvePlanForUser(&user)
			if plan.ID > 0 {
				var allowedSourceIDs []uint
				db.DB.Model(&models.QuotaPlanDataSource{}).
					Where("plan_id = ?", plan.ID).
					Pluck("data_source_id", &allowedSourceIDs)
				if len(allowedSourceIDs) > 0 {
					allowedMap := make(map[uint]bool)
					for _, id := range allowedSourceIDs {
						allowedMap[id] = true
					}
					filtered := make([]models.DataSource, 0, len(dataSources))
					for _, ds := range dataSources {
						if allowedMap[ds.ID] {
							filtered = append(filtered, ds)
						}
					}
					dataSources = filtered
				} else {
					dataSources = []models.DataSource{}
				}
			}
		}
	}

	items := make([]sourceConfigItem, 0, len(dataSources))
	for _, ds := range dataSources {
		items = append(items, sourceConfigItem{
			SourceName:    ds.Name,
			SourceDisplay: ds.DisplayName,
			BaseURL:       configMap[ds.Name],
		})
	}

	auth.Ok(c, items)
}

type updateSourceConfigsRequest struct {
	Configs []sourceConfigItem `json:"configs" binding:"required"`
}

// UpdateSourceConfigs 批量更新当前用户的数据源配置
func UpdateSourceConfigs(c *gin.Context) {
	userID, _ := c.Get("user_id")
	uid, _ := userID.(uint)

	var req updateSourceConfigsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		auth.Fail(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}

	for _, cfg := range req.Configs {
		if cfg.BaseURL == "" {
			db.DB.Where("user_id = ? AND source_name = ?", uid, cfg.SourceName).Delete(&models.UserSourceConfig{})
		} else {
			db.DB.Where("user_id = ? AND source_name = ?", uid, cfg.SourceName).
				Assign(models.UserSourceConfig{UserID: uid, SourceName: cfg.SourceName, BaseURL: cfg.BaseURL}).
				FirstOrCreate(&models.UserSourceConfig{})
		}
	}

	auth.Ok(c, gin.H{"message": "已更新"})
}

// GetImportConfig 返回面向登录用户的导入配置（书源直链等）
func GetImportConfig(c *gin.Context) {
	var setting models.SystemSetting
	url := ""
	if err := db.DB.Where("`key` = ?", "legado_import_url").First(&setting).Error; err == nil {
		url = setting.Value
	}
	auth.Ok(c, gin.H{"legado_import_url": url})
}

func RegisterRoutes(r *gin.Engine) {
	g := r.Group("/user", auth.AuthRequired())
	{
		g.GET("/source-configs", ListSourceConfigs)
		g.PUT("/source-configs", UpdateSourceConfigs)
		g.GET("/import-config", GetImportConfig)
		g.POST("/redeem", Redeem)
	}
}
