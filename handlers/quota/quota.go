package quota

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"loomproxy/base"
	"loomproxy/db"
	"loomproxy/gate"
	"loomproxy/handlers/auth"
	"loomproxy/models"
	"loomproxy/utils"
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
	SourceCode string `json:"source_code"`
	Name       string `json:"name"`
	ID         uint   `json:"id"`
	GroupID    uint   `json:"group_id,omitempty"`
	GroupName  string `json:"group_name,omitempty"` // 所属分组名（未分组或组已停用为空）
	// Override 是这个源上的**永久调整**（`user_quota_overrides` 的增量，带符号；无覆盖显示「未设置」）。
	// 这一格以前是**硬编码的常量**——面板没读它，于是它永远说同一句话。P97 之后必须说真话：
	// 转移不随每日刷新回退，被转空的源第二天仍然是 0，没有这一格那种源看起来就像源坏了。
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
	// QuotaCycleMode 与 NextCycleAt 是**本人口径的清零规则**：面板显示与切换入口都读这两格，
	// 不自己复制"什么模式下钟点怎么算"这条规则（规则在 gate.UsageSince / utils.DailyAnchorStart）。
	QuotaCycleMode string `json:"quota_cycle_mode"`
	// QuotaCycleAnchor 是 `subscription` 模式下实际用的清零钟点（"14:32" 这种形状，平台时区）。
	// 取的是注册时刻——**面板不许自己拿 created_at 算**：算错一个时区，显示的就不是判定读的那一格。
	QuotaCycleAnchor string `json:"quota_cycle_anchor,omitempty"`
	// SwitchAllowedAt 是下一次允许切换的时刻；NULL=现在就能切。限频数字由后端下发，不让面板写死。
	SwitchAllowedAt *time.Time `json:"switch_allowed_at"`
}

// overrideLabel 把带符号的增量写成 "+20" / "-20" / "未设置"。
// 空与 0 同义（这张表上 0 的语义就是"没有覆盖"，见 gate.TransferQuota 那句），所以都归到「未设置」。
func overrideLabel(v int64) string {
	if v == 0 {
		return "未设置"
	}
	if v > 0 {
		return "+" + strconv.FormatInt(v, 10)
	}
	return strconv.FormatInt(v, 10)
}

func nextResetTime(user *models.User) string {
	// 下一次清零的时刻：`day` 模式取「下一个平台时区零点」，`subscription` 模式取「下一个注册钟点」。
	// 日界的算法都在 utils 那一处（DayStart / DailyAnchorStart），这里只加一天。
	// （写成 DayStart(2) 是错的——那个函数是**往前**推，得到的是昨天零点；用例把这条钉着。）
	if user != nil && user.EffectiveQuotaCycleMode() == models.QuotaCycleSubscription && !user.CreatedAt.IsZero() {
		return utils.DailyAnchorStart(user.CreatedAt).Add(24 * time.Hour).Format("2006/1/2 15:04:05")
	}
	return utils.DayStart(1).Add(24 * time.Hour).Format("2006/1/2 15:04:05")
}

func RegisterRoutes(r *gin.Engine) {
	g := r.Group("/quota", auth.AuthOrKeyRequired())
	{
		g.GET("/dashboard", Dashboard)
		g.GET("/usage-logs", MyUsageLogs)
		g.GET("/transfers", MyQuotaTransfers)
	}
	// 转移单独挂在组**外面**：`/quota` 那组是 AuthOrKeyRequired（三形态统一可用，§7），
	// 而这条只认会话——挂在组里再叠一层 AuthRequired 会出现"网关先放行了 apiKey、
	// 再由端点拒绝"的两张脸，日志里看到的是端点拒绝、审计里看到的却是另一个人。
	// 一条端点一个守卫，谁的口径谁负责。
	r.POST("/quota/transfer", auth.AuthRequired(), TransferMyQuota)
	// 清除记录同样只认会话（理由与上一行一样，见 handlers/quota/transfers.go 的 ClearMyQuotaTransfers）
	r.DELETE("/quota/transfers", auth.AuthRequired(), ClearMyQuotaTransfers)
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
	if err := db.DB.Where("status = 1").Order("sort_order ASC, id ASC").Find(&dataSources).Error; err != nil {
		db.LogReadFail("quota_dashboard:data_sources", err)
	}

	// 如果用户非管理员，按套餐过滤：判据就一个——套餐有没有这个源的限额行（待办清单 P34）。
	// 过去这里是「EffectiveSourceLimit != 0 且关联表有记录」两个条件叠着，
	// 中间还留着一句「此处逻辑需确认」的注释：override 把额度打到 0 的源会被当成"没授权"显示出来，
	// 而真正的授权判据在另一张表里。合并成一张表之后没有这个歧义了。
	if !isAdmin {
		dataSources = gate.FilterByPlan(dataSources, planID)
	}

	planLimits := gate.PlanSourceLimits(planID)

	// 停用的组不参与分节/计数：其成员按未分组渲染（分组只是展示视图，不是准入开关）
	var enabledGroups []models.SourceGroup
	if err := db.DB.Where("status = 1").Order("sort_order ASC, id ASC").Find(&enabledGroups).Error; err != nil {
		db.LogReadFail("quota_dashboard:source_groups", err)
	}
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
					Cost:      db.DefaultInterfaceCost(h), // 与播种同一份默认值，不在这里各写一遍
					Status:    1,
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
						Cost:    db.DefaultInterfaceCost(h),
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
			used = gate.UsedToday(&user, ds.Name)
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
			Override:       overrideLabel(gate.UserSourceOverride(user.ID, ds.Name)),
			UsedToday:      used,
			ActiveUsers:    activeUsers,
			EffectiveTotal: quota,
			Remaining:      remaining,
			UsagePct:       usagePct,
			NextReset:      nextResetTime(&user),
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
		PlanName:       models.DisplayAlias(db.DisplayAliasesFor(user.ID), models.DisplayKindPlan, plan.ID, plan.Name),
		PlanCode:       plan.Code,
		PlanExpireAt:   user.PlanExpireAt,
	}
	// 清零口径随响应一起下发（同 P70 那条「档位数字由响应下发，面板不自己抄」的做法）
	resp.QuotaCycleMode = user.EffectiveQuotaCycleMode()
	resp.QuotaCycleAnchor = utils.ClockHHMM(user.CreatedAt)
	resp.SwitchAllowedAt = user.QuotaCycleNextSwitchAt()
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
	if err := db.DB.Model(&models.ApiCallLog{}).Where("created_at >= ?", gate.StartOfDay()).Count(&persisted).Error; err != nil {
		db.LogReadFail("monitor_calls_today:api_call_logs", err)
	}
	return persisted + base.CallsRecordedSince(gate.StartOfDay())
}
