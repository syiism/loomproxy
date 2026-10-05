package userconfig

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"

	"loomproxy/db"
	"loomproxy/gate"
	"loomproxy/handlers/auth"
	"loomproxy/handlers/catalog"
	"loomproxy/models"
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
				allowedMap := gate.PlanAllowedSourceIDs(plan.ID)
				if len(allowedMap) > 0 {
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
		// 这个端点的用途就是那一次写库，写没了却回「已更新」等于让用户以为地址改好了
		// （实际还在打旧的 base_url）。同族的写入口（生成密钥 / 加黑名单 / 存别名）都是失败就 500，
		// 这里过去是唯一一个不看结果的。Delete 删 0 行不算错误，所以不会有假失败。
		q := db.DB.Where("user_id = ? AND source_name = ?", uid, cfg.SourceName)
		var err error
		if cfg.BaseURL == "" {
			err = q.Delete(&models.UserSourceConfig{}).Error
		} else {
			err = q.Assign(models.UserSourceConfig{UserID: uid, SourceName: cfg.SourceName, BaseURL: cfg.BaseURL}).
				FirstOrCreate(&models.UserSourceConfig{}).Error
		}
		if err != nil {
			log.Printf("ERROR: 用户 %d 的数据源配置写入失败（源 %s）：%v", uid, cfg.SourceName, err)
			auth.Fail(c, http.StatusInternalServerError, "保存失败")
			return
		}
	}

	auth.Ok(c, gin.H{"message": "已更新"})
}

// GetImportConfig 返回面向登录用户的导入配置：书源文件由静态托管（/data/shuyuan/bookSource.json），
// 这里只给相对路径与「是否就位」。绝对地址由前端用 window.location.origin 拼——浏览器地址栏才是
// 用户真实到达的域，服务端转发的 X-Forwarded-Host 可能被伪造，用它拼链接等于把用户交给那个域。
func GetImportConfig(c *gin.Context) {
	auth.Ok(c, gin.H{
		"book_source_path": catalog.BookSourcePath(),
		"ready":            catalog.BookSourceReady(),
	})
}

func RegisterRoutes(r *gin.Engine) {
	g := r.Group("/user", auth.AuthOrKeyRequired())
	{
		g.GET("/source-configs", ListSourceConfigs)
		g.PUT("/source-configs", UpdateSourceConfigs)
		g.GET("/import-config", GetImportConfig)
		g.POST("/redeem", Redeem)
	}
}
