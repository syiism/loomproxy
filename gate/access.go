package gate

// 访问控制：数据源是否启用、用户生效套餐是否包含该数据源。
// 「套餐包含」现在就是一行限额（`gate/grant.go`，待办清单 P34）——不再有第二张关联表。
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
// 2. 用户套餐是否包含该数据源（= 有没有一行 scope=source 的限额，见 grant.go）
// 管理员跳过检查
// AccessReason 一次数据源访问判定的结论（空串=放行）
type AccessReason string

const (
	AccessOK        AccessReason = ""
	AccessNotFound  AccessReason = "not_found"  // 库里没有这个源（已下线/拼错）
	AccessDisabled  AccessReason = "disabled"   // 管理员禁用
	AccessNeedLogin AccessReason = "need_login" // 匿名，且免费套餐不含该源
	AccessNoPlan    AccessReason = "no_plan"    // 有用户但取不到生效套餐
	AccessUngranted AccessReason = "ungranted"  // 套餐不含该源（P34：没有那行限额就是没权限）
)

// SourceAccessVerdict 判定「这个调用者能不能用这个源」——**访问控制中间件与聚合搜索的扇出共用这一个口**。
//
// 为什么要抽出来：聚合请求在 handler 内部调别的源，那条路不经过中间件链，
// access/billing/ratelimit 三道闸门对它是盲的。如果扇出侧另写一遍判定，
// 就有了两份「谁能用哪个源」的事实——这正是判据页「同一份事实记在两处」那条的形状，
// 而且这里的偏差是安全偏差（扇出侧写松一点 = 绕过套餐）。
//
// user 为 nil 表示匿名（无 token / env 静态键），走免费套餐那一档。
func SourceAccessVerdict(ds *models.DataSource, user *models.User) AccessReason {
	if ds == nil {
		return AccessNotFound
	}
	if ds.Status != 1 {
		return AccessDisabled
	}
	if user == nil {
		if FreePlanAllowsSource(ds.Name) {
			return AccessOK
		}
		return AccessNeedLogin
	}
	if user.HasRole("admin") {
		return AccessOK
	}
	plan := ResolvePlan(user)
	if plan.ID == 0 {
		return AccessNoPlan
	}
	if !PlanHasSource(plan.ID, ds.Name) {
		return AccessUngranted
	}
	return AccessOK
}

// SourceAccessByName 按源码判定（扇出侧用：它只有名字，没有中间件那份已加载的行）
func SourceAccessByName(sourceName string, user *models.User) AccessReason {
	var ds models.DataSource
	if err := db.DB.Where("name = ?", sourceName).First(&ds).Error; err != nil {
		return AccessNotFound
	}
	return SourceAccessVerdict(&ds, user)
}

// accessMessage 判定结论对应的对外文案与状态码：中间件用它渲染，
// 文案与改动前逐字一致（这条重构不许顺手改对外的话术）
func accessMessage(reason AccessReason) (int, string) {
	switch reason {
	case AccessDisabled:
		return http.StatusForbidden, "该数据源已被管理员禁用"
	case AccessNeedLogin:
		return http.StatusForbidden, "请登录后访问该数据源"
	case AccessNoPlan:
		return http.StatusForbidden, "无可用套餐，请联系管理员"
	case AccessUngranted:
		return http.StatusForbidden, "当前套餐不包含该数据源，请升级套餐"
	default:
		return http.StatusNotFound, "数据源不存在"
	}
}

func DataSourceAccessMiddleware(sourceName string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 跳过非数据源路由（如 /datasources, /data, /panel 等）
		if sourceName == "" || sourceName == "datasources" || sourceName == "data" || sourceName == "panel" {
			c.Next()
			return
		}

		var ds models.DataSource
		if err := db.DB.Where(map[string]interface{}{"name": sourceName}).First(&ds).Error; err != nil {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{
				"code": conf.Config.ErrorCode,
				"msg":  "数据源不存在",
			})
			return
		}

		// 解析调用者：context 里的 user_id 优先（apiauth 已解 JWT），
		// 没有就自行从请求取 token。取不到用户就是匿名，**不是**「跳过判定」——
		// 改动前坏 token 会直接 c.Next() 放行（实际被 apiauth 在前面拦住了，走不到这里），
		// 现在按匿名判定，少一条「跳过闸门」的分支。
		user := accessCaller(c)
		if user != nil {
			// 已经查出来了就顺手挂给链上的后来者（监控读同意位、聚合搜索读套餐授权），
			// 别让同一条读路径再各查一遍
			c.Set(middleware.CtxCurrentUser, user)
		}

		if reason := SourceAccessVerdict(&ds, user); reason != AccessOK {
			status, msg := accessMessage(reason)
			c.AbortWithStatusJSON(status, gin.H{"code": conf.Config.ErrorCode, "msg": msg})
			return
		}
		c.Next()
	}
}

// accessCaller 取当前请求的调用者用户（含角色与套餐）。
// 三种「拿不到人」要分开：匿名或 user_id 形态非法 → nil（按匿名判定）；
// 有 id 但库里查不到 → 就地 401「用户不存在」并返回 nil（这条与改动前逐字一致）。
func accessCaller(c *gin.Context) *models.User {
	var userID uint
	if uid, exists := c.Get("user_id"); exists {
		id, ok := uid.(uint)
		if !ok {
			return nil
		}
		userID = id
	} else if tokenStr := utils.TokenFromRequest(c); tokenStr != "" {
		if claims, err := utils.ParseToken(tokenStr); err == nil {
			userID = claims.UserID
		}
	}
	if userID == 0 {
		return nil
	}
	u, err := loadUserForAccess(userID)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"code": conf.Config.ErrorCode,
			"msg":  "用户不存在",
		})
		return nil
	}
	return u
}

func loadUserForAccess(userID uint) (*models.User, error) {
	var user models.User
	if err := db.DB.Preload("Roles").Preload("Plan").First(&user, userID).Error; err != nil {
		return nil, err
	}
	return &user, nil
}
