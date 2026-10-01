package quota

import (
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"loomproxy/base"
	"loomproxy/db"
	"loomproxy/gate"
	"loomproxy/handlers/auth"
	"loomproxy/models"
)

type InterfaceCost struct {
	ID         uint   `json:"id"`
	Name       string `json:"name"`
	Cost       int64  `json:"cost"`
	Interval   int64  `json:"interval"`
	LimitCount int64  `json:"limit_count"` // 窗口计数限流：窗口内最多 N 次，0=不限
	WindowSec  int64  `json:"window_sec"`  // 窗口长度(秒)
	Enabled    bool   `json:"enabled"`
}

type DashboardSource struct {
	SourceCode     string          `json:"source_code"`
	Name           string          `json:"name"`
	ID             uint            `json:"id"`
	GroupID        uint            `json:"group_id,omitempty"`
	GroupName      string          `json:"group_name,omitempty"` // 所属分组名（未分组或组已停用为空）
	Override       string          `json:"override"`
	UsedToday      int64           `json:"used_today"`   // 普通用户=自己的消耗；管理员=全站消耗
	ActiveUsers    int64           `json:"active_users"` // 管理员：当日有消耗的去重用户数
	EffectiveTotal int64           `json:"effective_total"`
	Remaining      int64           `json:"remaining"`
	UsagePct       int             `json:"usage_pct"`
	NextReset      string          `json:"next_reset"`
	Handlers       []string        `json:"handlers"`
	Interfaces     []InterfaceCost `json:"interfaces"`
}

// DashboardGroup 首页筛选栏的分组项：只包含在本次可见源里至少有一个成员的组
type DashboardGroup struct {
	ID        uint   `json:"id"`
	Name      string `json:"name"`
	SortOrder int    `json:"sort_order"`
	Count     int64  `json:"count"`
}

type DashboardResponse struct {
	Sources        []DashboardSource `json:"sources"`
	Groups         []DashboardGroup  `json:"groups"`          // 首页筛选栏的分组项（含计数）
	UngroupedCount int64             `json:"ungrouped_count"` // 未分组（或所属组已停用）的可见源数
	IsAdmin        bool              `json:"is_admin"`
	PlanName       string            `json:"plan_name"`
	PlanCode       string            `json:"plan_code"`
	PlanExpireAt   *time.Time        `json:"plan_expire_at"` // 套餐到期时间（null=永久/免费）
	ActiveUsers    int64             `json:"active_users"`   // 管理员：当日全站去重活跃用户数
	CallCount      int64             `json:"call_count"`     // 管理员：当日全站调用次数（接口监控口径，含未计费调用）
}

func nextResetTime() string {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Now().In(loc)
	next := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	if now.Hour() > 0 || now.Minute() > 0 || now.Second() > 0 {
		next = next.Add(24 * time.Hour)
	}
	return next.Format("2006/1/2 15:04:05")
}

func RegisterRoutes(r *gin.Engine) {
	g := r.Group("/quota", auth.AuthRequired())
	{
		g.GET("/dashboard", Dashboard)
		g.GET("/usage-logs", MyUsageLogs)
	}
}

func Dashboard(c *gin.Context) {
	userID, _ := c.Get("user_id")
	uid, _ := userID.(uint)

	var user models.User
	isAdmin := false
	if err := db.DB.Preload("Roles").Preload("Plan").First(&user, uid).Error; err == nil {
		for _, r := range user.Roles {
			if r.Code == "admin" {
				isAdmin = true
				break
			}
		}
	}

	// 优先使用用户自己绑定的套餐，否则回退全局默认套餐
	plan := gate.ResolvePlan(&user)
	planID := plan.ID

	// 获取所有启用的数据源
	var dataSources []models.DataSource
	db.DB.Where("status = 1").Order("sort_order ASC, id ASC").Find(&dataSources)

	// 如果用户非管理员，按套餐过滤
	if !isAdmin {
		planLimits := gate.PlanSourceLimits(planID)
		filtered := make([]models.DataSource, 0, len(dataSources))
		for _, ds := range dataSources {
			limit := gate.EffectiveSourceLimit(&user, ds.Name, planLimits)
			if limit != 0 { // limit == 0 表示该套餐未包含该数据源（通过 override=0 表示不限制，但此处逻辑需确认）
				// 实际上 effectiveSourceLimit 返回 -1 表示不限制，>=0 表示具体限额
				// 套餐包含的数据源在 QuotaPlanDataSource 中有记录
				var count int64
				db.DB.Model(&models.QuotaPlanDataSource{}).
					Where("plan_id = ? AND data_source_id = ?", plan.ID, ds.ID).
					Count(&count)
				if count > 0 {
					filtered = append(filtered, ds)
				}
			} else {
				// 管理员或无限制套餐
				filtered = append(filtered, ds)
			}
		}
		dataSources = filtered
	}

	planLimits := gate.PlanSourceLimits(planID)

	// 停用的组不参与分节/计数：其成员按未分组渲染（分组只是展示视图，不是准入开关）
	var enabledGroups []models.SourceGroup
	db.DB.Where("status = 1").Order("sort_order ASC, id ASC").Find(&enabledGroups)
	groupNames := make(map[uint]string, len(enabledGroups))
	for _, g := range enabledGroups {
		groupNames[g.ID] = g.Name
	}

	sources := make([]DashboardSource, 0, len(dataSources))
	for _, ds := range dataSources {
		// 该数据源的 handlers（从注册表获取实际注册的 actions）
		handlers := []string{}
		routes := base.Routes()
		for path := range routes {
			if strings.HasPrefix(path, "/"+ds.Name+"/") {
				action := strings.TrimPrefix(path, "/"+ds.Name+"/")
				handlers = append(handlers, action)
			}
		}
		sort.Strings(handlers)

		// interfaces 同 handlers（每个 action 对应一个接口）
		ifaces := make([]InterfaceCost, len(handlers))
		for i, h := range handlers {
			// 查找对应的 QuotaCost（group_code 列存数据源码）
			var cost models.QuotaCost
			err := db.DB.Where("group_code = ? AND interface = ?", ds.Name, h).First(&cost).Error
			if err == nil {
				ifaces[i] = InterfaceCost{
					ID:         cost.ID,
					Name:       cost.Interface,
					Cost:       cost.Cost,
					Interval:   cost.Interval,
					LimitCount: cost.LimitCount,
					WindowSec:  cost.WindowSec,
					Enabled:    cost.Status == 1,
				}
			} else {
				// 不存在时自动创建默认记录（支持新接口无需手动建记录）
				cost2 := models.QuotaCost{
					GroupCode: ds.Name,
					Interface: h,
					Cost:      1,
					Status:    1,
				}
				if h == "recommend" || h == "front" || h == "landing" {
					cost2.Cost = 0
				}
				if err2 := db.DB.Create(&cost2).Error; err2 == nil {
					ifaces[i] = InterfaceCost{
						ID:         cost2.ID,
						Name:       cost2.Interface,
						Cost:       cost2.Cost,
						Interval:   cost2.Interval,
						LimitCount: cost2.LimitCount,
						WindowSec:  cost2.WindowSec,
						Enabled:    cost2.Status == 1,
					}
				} else {
					ifaces[i] = InterfaceCost{
						Name:    h,
						Cost:    1,
						Enabled: true,
					}
				}
			}
		}
		if ifaces == nil {
			ifaces = []InterfaceCost{}
		}

		// 计算额度
		quota := int64(-1)
		if !isAdmin {
			quota = gate.EffectiveSourceLimit(&user, ds.Name, planLimits)
		}

		// 当日真实用量
		used := int64(0)
		activeUsers := int64(0)
		if isAdmin {
			used = gate.UsedTodayAll(ds.Name)
			activeUsers = gate.ActiveUsersToday(ds.Name)
		} else {
			used = gate.UsedToday(uid, ds.Name)
		}

		remaining := quota
		usagePct := 0
		if quota >= 0 {
			remaining = quota - used
			if remaining < 0 {
				remaining = 0
			}
			if quota > 0 {
				usagePct = int(used * 100 / quota)
				if usagePct > 100 {
					usagePct = 100
				}
			}
		}

		var groupID uint
		var groupName string
		if ds.GroupID != nil {
			if n, ok := groupNames[*ds.GroupID]; ok {
				groupID = *ds.GroupID
				groupName = n
			}
		}

		sources = append(sources, DashboardSource{
			ID:             ds.ID,
			SourceCode:     ds.Name,
			Name:           ds.DisplayName,
			GroupID:        groupID,
			GroupName:      groupName,
			Override:       "未设置",
			UsedToday:      used,
			ActiveUsers:    activeUsers,
			EffectiveTotal: quota,
			Remaining:      remaining,
			UsagePct:       usagePct,
			NextReset:      nextResetTime(),
			Handlers:       handlers,
			Interfaces:     ifaces,
		})
	}

	// 分组筛选栏：按 enabledGroups 的顺序出，计数为 0 的组不出现（前端「全部」按钮恒在，无需后端补）
	counted := make(map[uint]int64, len(enabledGroups))
	var ungrouped int64
	for _, s := range sources {
		if s.GroupID == 0 {
			ungrouped++
			continue
		}
		counted[s.GroupID]++
	}
	groupViews := make([]DashboardGroup, 0, len(enabledGroups))
	for _, g := range enabledGroups {
		if n := counted[g.ID]; n > 0 {
			groupViews = append(groupViews, DashboardGroup{ID: g.ID, Name: g.Name, SortOrder: g.SortOrder, Count: n})
		}
	}

	resp := DashboardResponse{
		Sources:        sources,
		Groups:         groupViews,
		UngroupedCount: ungrouped,
		IsAdmin:        isAdmin,
		PlanName:       plan.Name,
		PlanCode:       plan.Code,
		PlanExpireAt:   user.PlanExpireAt,
	}
	if isAdmin {
		resp.ActiveUsers = gate.ActiveUsersToday("")
		// 今日调用次数取接口监控口径（今日落库明细 + 内存环中今日记录），
		// 覆盖全部调用；原流水行数只统计计费调用，口径过窄。
		// 归档表只含已过保留期的历史数据（未设保留期时为空），与今日无关
		resp.CallCount = monitorCallsToday()
	}

	auth.Ok(c, resp)
}

// monitorCallsToday 当日全部数据源接口调用次数（接口监控口径）：
// 今日已落库明细 + 内存环形缓冲中今日的调用记录
func monitorCallsToday() int64 {
	var persisted int64
	db.DB.Model(&models.ApiCallLog{}).Where("created_at >= ?", gate.StartOfDay()).Count(&persisted)
	return persisted + base.CallsRecordedSince(gate.StartOfDay())
}
