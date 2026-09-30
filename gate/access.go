package gate

// 访问控制：数据源是否启用、用户生效套餐是否包含该数据源。
//
// 注册 Def：Name="access"，Scope=Route，Order=400。
// 三轴里最早的一位（先问「能不能用」，再问「值多少钱」「多快能用第二次」）；
// 晚于 monitor(200)，403 才会计入监控明细并喂给自动拉黑。
//
// 非数据源路由（/datasources、/data、/panel）的跳过判定留在函数体内，
// 与 Applies 的分工是：Applies 表达链结构（哪些路由挂这条），函数体表达业务规则。

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"loomproxy/conf"
	"loomproxy/db"
	"loomproxy/middleware"
	"loomproxy/models"
	"loomproxy/utils"
)

func init() {
	middleware.Register(middleware.Def{
		Name:  "access",
		Scope: middleware.Route,
		Order: middleware.OrderAccess,
		Build: func(s middleware.Spec) gin.HandlerFunc { return DataSourceAccessMiddleware(s.Source) },
	})
}

// DataSourceAccessMiddleware 检查数据源访问权限：
// 1. 数据源是否启用（DataSource.Status = 1）
// 2. 用户套餐是否包含该数据源（QuotaPlanDataSource 关联）
// 管理员跳过检查
func DataSourceAccessMiddleware(sourceName string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 跳过非数据源路由（如 /datasources, /data, /panel 等）
		if sourceName == "" || sourceName == "datasources" || sourceName == "data" || sourceName == "panel" {
			c.Next()
			return
		}

		// 1. 检查数据源是否存在且启用
		var ds models.DataSource
		if err := db.DB.Where("name = ?", sourceName).First(&ds).Error; err != nil {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{
				"code": conf.Config.ErrorCode,
				"msg":  "数据源不存在",
			})
			return
		}
		if ds.Status != 1 {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"code": conf.Config.ErrorCode,
				"msg":  "该数据源已被管理员禁用",
			})
			return
		}

		// 2. 获取用户身份（优先从 context 获取）
		var userID uint
		if uid, exists := c.Get("user_id"); exists {
			if id, ok := uid.(uint); ok {
				userID = id
			}
		} else {
			tokenStr := utils.TokenFromRequest(c)
			if tokenStr == "" {
				// 匿名用户：只允许访问免费套餐包含的数据源
				if !isDataSourceInFreePlan(sourceName) {
					c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
						"code": conf.Config.ErrorCode,
						"msg":  "请登录后访问该数据源",
					})
					return
				}
				c.Next()
				return
			}
			claims, err := utils.ParseToken(tokenStr)
			if err != nil {
				c.Next()
				return
			}
			userID = claims.UserID
		}

		if userID > 0 {
			var user models.User
			if err := db.DB.Preload("Roles").Preload("Plan").First(&user, userID).Error; err != nil {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
					"code": conf.Config.ErrorCode,
					"msg":  "用户不存在",
				})
				return
			}

			// 管理员跳过检查
			if user.HasRole("admin") {
				c.Next()
				return
			}

			// 3. 获取用户生效套餐
			plan := ResolvePlan(&user)
			if plan.ID == 0 {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
					"code": conf.Config.ErrorCode,
					"msg":  "无可用套餐，请联系管理员",
				})
				return
			}

			// 4. 检查套餐是否包含该数据源
			var count int64
			db.DB.Model(&models.QuotaPlanDataSource{}).
				Where("plan_id = ? AND data_source_id = ?", plan.ID, ds.ID).
				Count(&count)
			if count == 0 {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
					"code": conf.Config.ErrorCode,
					"msg":  "当前套餐不包含该数据源，请升级套餐",
				})
				return
			}
		}

		c.Next()
	}
}

// isDataSourceInFreePlan 检查数据源是否在免费套餐中
func isDataSourceInFreePlan(sourceName string) bool {
	var ds models.DataSource
	if err := db.DB.Where("name = ?", sourceName).First(&ds).Error; err != nil {
		return false
	}
	var freePlan models.QuotaPlan
	if err := db.DB.Where("code = ?", "free").First(&freePlan).Error; err != nil {
		return false
	}
	var count int64
	db.DB.Model(&models.QuotaPlanDataSource{}).
		Where("plan_id = ? AND data_source_id = ?", freePlan.ID, ds.ID).
		Count(&count)
	return count > 0
}

// userHasDataSourceAccess 检查用户套餐是否包含该数据源
func userHasDataSourceAccess(user *models.User, sourceName string) bool {
	var ds models.DataSource
	if err := db.DB.Where("name = ?", sourceName).First(&ds).Error; err != nil {
		return false
	}

	plan := ResolvePlan(user)
	if plan.ID == 0 {
		return false
	}

	var count int64
	db.DB.Model(&models.QuotaPlanDataSource{}).
		Where("plan_id = ? AND data_source_id = ?", plan.ID, ds.ID).
		Count(&count)
	return count > 0
}
