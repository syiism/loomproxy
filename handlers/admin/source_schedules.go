package admin

// 数据源定时启用：管理员指定「哪些源、哪个时段、启用多久」，后台循环到点把源置为启用、出窗口还原。
//
// 语义上刻意守死的三条：
//  1. **只在进出窗口的那一刻写库**（靠 Applied 标记），常规 tick 不产生写压力，也不会把管理员
//     在窗口内的手动改动反复覆盖掉——手动改了就是改了，下一次动作发生在窗口边界。
//  2. **出窗口还原成进窗口前的状态**（RestoreStatus），不是一律禁用。否则一个本来就手动启用着的源
//     会被定时任务关掉，而管理员看到的是"我没动它，它自己关了"。
//  3. 停用/删除一条**正处于生效中**的规则必须先还原，否则源会被永久留成启用——
//     规则行没了，循环再也看不见它，没有任何一步会把它收回来。
//  4. **同一个源可以有多条规则，窗口重叠时把它们当成同一层的叠顶**：源的真实原状态只由最早
//     进窗的那条记（后来者继承它，不能读已被顶起来的 status），出窗时只要还有别的规则生效就不动源。
//     否则重叠窗口会走出「A 出窗把正在窗口里的 B 关掉」→「B 出窗把自己记的 1 还原回去」→
//     源永久启用且没有任何 applied 规则来收回它（连删除都不还原，因为 applied 已经是 0）。

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"loomproxy/db"
	"loomproxy/handlers/auth"
	"loomproxy/handlers/catalog"
	"loomproxy/models"
	"loomproxy/utils"
)

// SourceScheduleWindow 判定某条规则在 now 这一刻是否处于窗口内，以及（一次性规则）窗口是否已过期。
// 导出是为了让用例能直接钉住窗口算法，而不必真的等到那个钟点。
//
// daily：以"最近一次到达起始钟点"为窗口起点（utils.DailyAnchorStartAt），长度 = 结束钟点 - 起始钟点
// 按 24h 取模。跨夜（22:00–06:00）与"今天的起始钟点还没到"（窗口从昨天算起）都由 anchor 语义自然覆盖，
// 不在这里另写分支——第二份日界算法迟早会和 utils 那一处算得不一样。
//
// once：窗口就是 [start_at, start_at+duration_min)。
func SourceScheduleWindow(r *models.SourceSchedule, now time.Time) (in bool, expired bool) {
	switch r.Mode {
	case models.ScheduleModeDaily:
		sh, sm, err := utils.ParseClockHHMM(r.StartClock)
		if err != nil {
			return false, false
		}
		eh, em, err := utils.ParseClockHHMM(r.EndClock)
		if err != nil {
			return false, false
		}
		startMin := sh*60 + sm
		endMin := eh*60 + em
		length := (endMin - startMin + 1440) % 1440
		if length == 0 {
			length = 1440 // 起止钟点相同 = 整天，不是"零长度窗口"
		}
		anchor := utils.DailyAnchorStartAt(now, time.Date(2000, 1, 1, sh, sm, 0, 0, utils.PlatformZone()))
		end := anchor.Add(time.Duration(length) * time.Minute)
		return !now.Before(anchor) && now.Before(end), false

	case models.ScheduleModeOnce:
		if r.StartAt == nil || r.DurationMin <= 0 {
			return false, false
		}
		start := *r.StartAt
		end := start.Add(time.Duration(r.DurationMin) * time.Minute)
		if now.Before(start) {
			return false, false // 还没到点
		}
		if now.Before(end) {
			return true, false
		}
		return false, true

	default:
		return false, false
	}
}

// validateScheduleFields 校验一条规则的形态：mode 必须是两种之一，且该模式需要的字段都在且合法。
// 两种模式共用一张表，字段按模式二选一——校验集中在这里，端点与更新共用同一份判据。
func validateScheduleFields(mode, startClock, endClock string, startAt *time.Time, durationMin int) string {
	switch mode {
	case models.ScheduleModeDaily:
		if _, _, err := utils.ParseClockHHMM(startClock); err != nil {
			return "起始钟点格式应为 HH:MM"
		}
		if _, _, err := utils.ParseClockHHMM(endClock); err != nil {
			return "结束钟点格式应为 HH:MM"
		}
		return ""
	case models.ScheduleModeOnce:
		if startAt == nil {
			return "一次性规则必须给出 start_at"
		}
		if durationMin <= 0 {
			return "一次性规则的 duration_min 必须大于 0"
		}
		return ""
	default:
		return "mode 必须是 daily 或 once"
	}
}

// enterScheduleWindow 进窗口：记下源当前的状态，再把它置为启用。
// 返回值「源的状态是否真的变了」——原状态本就是启用时没什么可失效的，不必惊动缓存。
// enterScheduleWindow 进窗口：把源顶成启用，记下进窗口前的原状态（RestoreStatus）。
// 在传入的 tx 上写——调用方要么传 db.DB（后台循环/单次判定），要么传自己开的事务
// （停用/删除时要把"还原"和"规则退场"放进同一笔事务）。不要在这里再开一层事务，
// SQLite 只允许一个写事务，嵌套 db.DB.Transaction 会死锁在 BEGIN。
func enterScheduleWindow(tx *gorm.DB, r models.SourceSchedule, now time.Time) (bool, error) {
	var ds models.DataSource
	if err := tx.First(&ds, r.DataSourceID).Error; err != nil {
		return false, err
	}
	// 源已经是启用态时分两种：管理员手动启用的（RestoreStatus 就该记 1），
	// 和**另一条规则替它顶起来的**（那时 ds.Status=1 是结果不是原状态）。
	// 后者照 ds.Status 记，最后出窗的那条规则就会把源还原成启用——再没有一步收得回来，
	// 所以从最早那条生效的规则继承原状态；查不到生效中的规则才说明这 1 真是管理员给的。
	restore := ds.Status
	if ds.Status == 1 {
		var prev models.SourceSchedule
		err := tx.Where("data_source_id = ? AND applied = 1 AND id <> ?", r.DataSourceID, r.ID).
			Order("id").First(&prev).Error
		if err == nil {
			restore = prev.RestoreStatus
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return false, err
		}
	}
	if ds.Status != 1 {
		if err := tx.Model(&models.DataSource{}).Where("id = ?", ds.ID).Update("status", 1).Error; err != nil {
			return false, err
		}
	}
	if err := tx.Model(&models.SourceSchedule{}).Where("id = ?", r.ID).
		Updates(map[string]interface{}{"restore_status": restore, "applied": 1, "applied_at": now}).Error; err != nil {
		return false, err
	}
	log.Printf("ADMIN(sched): 定时启用生效 数据源=%s(%d) 规则=%d 窗口内原状态=%d（出窗口时还原）",
		ds.Name, ds.ID, r.ID, restore)
	return ds.Status != 1, nil
}

// leaveScheduleWindow 出窗口：把源还原成进窗口前的状态。停用时也走它，见 releaseSchedule。
//
// 同源还有别的规则生效时**不写源状态**：那条规则的窗口还没到，源应当继续启用着；
// 原状态由留在栈上的那条持有，最后一出窗的那条负责还原。
func leaveScheduleWindow(tx *gorm.DB, r models.SourceSchedule) error {
	var others int64
	if err := tx.Model(&models.SourceSchedule{}).
		Where("data_source_id = ? AND applied = 1 AND id <> ?", r.DataSourceID, r.ID).Count(&others).Error; err != nil {
		return err
	}
	if others == 0 {
		if err := tx.Model(&models.DataSource{}).Where("id = ?", r.DataSourceID).
			Update("status", r.RestoreStatus).Error; err != nil {
			return err
		}
	}
	if err := tx.Model(&models.SourceSchedule{}).Where("id = ?", r.ID).
		Updates(map[string]interface{}{"applied": 0}).Error; err != nil {
		return err
	}
	if others == 0 {
		log.Printf("ADMIN(sched): 定时启用结束 数据源 id=%d 规则=%d 已还原为 %d", r.DataSourceID, r.ID, r.RestoreStatus)
	} else {
		log.Printf("ADMIN(sched): 定时启用结束 数据源 id=%d 规则=%d 还有 %d 条规则生效中，源状态不动",
			r.DataSourceID, r.ID, others)
	}
	return nil
}

// releaseSchedule 规则不再生效前必须调用：正处于生效中的规则直接停用/删除，
// 源会被永久留成启用（没有任何一步会把它收回来），所以这里先还原再让规则退场。
// tx 由调用方提供（与"规则退场"同一笔事务），不再自开事务——见 enterScheduleWindow 注释。
func releaseSchedule(tx *gorm.DB, r models.SourceSchedule) error {
	if r.Applied != 1 {
		return nil
	}
	return leaveScheduleWindow(tx, r)
}

// RunSourceScheduleTick 跑一轮定时启用判定。
//
// 时刻由调用方注入（不是自己 time.Now()）是刻意的：后台循环和测试打的是同一条判定路径，
// 用例不必 sleep 到那个钟点就能验证进出窗口的行为（与 admin.RunSubjectBackfill 同例）。
func RunSourceScheduleTick(now time.Time) {
	var rules []models.SourceSchedule
	if err := db.DB.Where("status = ?", 1).Find(&rules).Error; err != nil {
		db.LogReadFail("source_schedule:source_schedules", err)
		return
	}

	changed := false
	for i := range rules {
		r := rules[i]
		in, expired := SourceScheduleWindow(&r, now)

		if in && r.Applied == 0 {
			flipped, err := enterScheduleWindow(db.DB, r, now)
			if err != nil {
				log.Printf("ERROR: 定时启用生效失败 规则=%d: %v", r.ID, err)
				continue
			}
			if flipped {
				changed = true
			}
		} else if !in && r.Applied == 1 {
			if err := leaveScheduleWindow(db.DB, r); err != nil {
				log.Printf("ERROR: 定时启用还原失败 规则=%d: %v", r.ID, err)
				continue
			}
			changed = true
		}

		// 一次性规则窗口已过：规则置为不生效（行保留供查看，不物理删除）。
		// 与"还原"是两件事——没生效过的过期规则也要标记结束，否则列表里一直挂着一条永不触发的规则。
		if !in && expired && r.Status == 1 && r.Mode == models.ScheduleModeOnce {
			if err := db.DB.Model(&models.SourceSchedule{}).Where("id = ?", r.ID).
				Updates(map[string]interface{}{"status": 0}).Error; err != nil {
				log.Printf("ERROR: 标记一次性规则结束失败 规则=%d: %v", r.ID, err)
			}
		}
	}

	if changed {
		// 源的可用集合变了，/datasources 的缓存视图要跟着变
		catalog.InvalidateDatasourcesCache()
	}
}

type sourceScheduleView struct {
	models.SourceSchedule
	SourceName    string `json:"source_name"`    // 数据源码
	SourceDisplay string `json:"source_display"` // 显示名
	InWindow      bool   `json:"in_window"`      // 此刻是否在窗口内
	WindowText    string `json:"window_text"`    // 人读的窗口描述，如「每天 09:00–18:00」
}

// scheduleWindowText 把一条规则写成人读的一句话（列表与前端标签共用同一份口径）。
func scheduleWindowText(r models.SourceSchedule) string {
	switch r.Mode {
	case models.ScheduleModeDaily:
		return "每天 " + r.StartClock + "–" + r.EndClock
	case models.ScheduleModeOnce:
		if r.StartAt == nil {
			return "一次性（未设定起始时刻）"
		}
		return "一次性 " + r.StartAt.In(utils.PlatformZone()).Format("2006-01-02 15:04") + " 起 " + strconv.Itoa(r.DurationMin) + " 分钟"
	default:
		return r.Mode
	}
}

// ListSourceSchedules 列出全部定时启用规则（含源名与"此刻是否在窗口内"）。
func ListSourceSchedules(c *gin.Context) {
	var rules []models.SourceSchedule
	if err := db.DB.Order("id DESC").Find(&rules).Error; err != nil {
		db.LogReadFail("source_schedule:source_schedules", err)
		auth.Fail(c, http.StatusInternalServerError, "读取失败")
		return
	}
	names := map[uint]models.DataSource{}
	if len(rules) > 0 {
		ids := make([]uint, 0, len(rules))
		for _, r := range rules {
			ids = append(ids, r.DataSourceID)
		}
		var sources []models.DataSource
		if err := db.DB.Where("id IN ?", ids).Find(&sources).Error; err != nil {
			db.LogReadFail("source_schedule:data_sources", err)
		}
		for _, ds := range sources {
			names[ds.ID] = ds
		}
	}
	now := time.Now()
	views := make([]sourceScheduleView, 0, len(rules))
	for _, r := range rules {
		in, _ := SourceScheduleWindow(&r, now)
		views = append(views, sourceScheduleView{
			SourceSchedule: r,
			SourceName:     names[r.DataSourceID].Name,
			SourceDisplay:  names[r.DataSourceID].DisplayName,
			InWindow:       in,
			WindowText:     scheduleWindowText(r),
		})
	}
	auth.Ok(c, views)
}

type createSourceScheduleRequest struct {
	SourceIDs   []uint     `json:"source_ids" binding:"required"` // 一次为多个源各建一行（与套餐批量授权同形）
	Mode        string     `json:"mode" binding:"required"`
	StartClock  string     `json:"start_clock"`
	EndClock    string     `json:"end_clock"`
	StartAt     *time.Time `json:"start_at"`
	DurationMin int        `json:"duration_min"`
}

// CreateSourceSchedules 新建定时启用规则：同一份窗口配置一次落到多个源上，每个源一行。
func CreateSourceSchedules(c *gin.Context) {
	var req createSourceScheduleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		auth.Fail(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}
	if len(req.SourceIDs) == 0 {
		auth.Fail(c, http.StatusBadRequest, "source_ids 不能为空")
		return
	}
	if msg := validateScheduleFields(req.Mode, req.StartClock, req.EndClock, req.StartAt, req.DurationMin); msg != "" {
		auth.Fail(c, http.StatusBadRequest, msg)
		return
	}

	var n int64
	if err := db.DB.Model(&models.DataSource{}).Where("id IN ?", req.SourceIDs).Count(&n).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}
	if int(n) != len(req.SourceIDs) {
		auth.Fail(c, http.StatusBadRequest, "部分数据源不存在")
		return
	}

	created := 0
	if err := db.DB.Transaction(func(tx *gorm.DB) error {
		for _, id := range req.SourceIDs {
			row := models.SourceSchedule{
				DataSourceID: id,
				Mode:         req.Mode,
				StartClock:   req.StartClock,
				EndClock:     req.EndClock,
				StartAt:      req.StartAt,
				DurationMin:  req.DurationMin,
				Status:       1,
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			created++
		}
		return nil
	}); err != nil {
		auth.Fail(c, http.StatusInternalServerError, "创建失败")
		return
	}

	// 立刻判定一轮：规则刚建就可能已经落在窗口里，不该让它等下一个 tick
	RunSourceScheduleTick(time.Now())
	auth.Ok(c, gin.H{"created": created})
}

type updateSourceScheduleRequest struct {
	Mode        *string    `json:"mode"`
	StartClock  *string    `json:"start_clock"`
	EndClock    *string    `json:"end_clock"`
	StartAt     *time.Time `json:"start_at"`
	DurationMin *int       `json:"duration_min"`
	Status      *int       `json:"status"` // 0=停用（生效中的会先还原源状态）
}

// UpdateSourceSchedule 改规则的窗口或停启用。
func UpdateSourceSchedule(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		auth.Fail(c, http.StatusBadRequest, "无效的规则 ID")
		return
	}
	var req updateSourceScheduleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		auth.Fail(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}

	var rule models.SourceSchedule
	if err := db.DB.First(&rule, id).Error; err != nil {
		auth.Fail(c, http.StatusNotFound, "规则不存在")
		return
	}

	// 先在内存里改完再整体验证：mode 与窗口字段是配套的，逐字段校验会放过"改成 once 却没给 start_at"这种组合
	if req.Mode != nil {
		rule.Mode = *req.Mode
	}
	if req.StartClock != nil {
		rule.StartClock = *req.StartClock
	}
	if req.EndClock != nil {
		rule.EndClock = *req.EndClock
	}
	if req.StartAt != nil {
		rule.StartAt = req.StartAt
	}
	if req.DurationMin != nil {
		rule.DurationMin = *req.DurationMin
	}
	if msg := validateScheduleFields(rule.Mode, rule.StartClock, rule.EndClock, rule.StartAt, rule.DurationMin); msg != "" {
		auth.Fail(c, http.StatusBadRequest, msg)
		return
	}
	if req.Status != nil {
		rule.Status = *req.Status
	}

	if err := db.DB.Transaction(func(tx *gorm.DB) error {
		// 停用前先还原：否则源被留在启用态，而规则退场后循环再也看不见它
		if rule.Status == 0 {
			if err := releaseSchedule(tx, rule); err != nil {
				return err
			}
		}
		return tx.Model(&models.SourceSchedule{}).Where("id = ?", rule.ID).
			Updates(map[string]interface{}{
				"mode":         rule.Mode,
				"start_clock":  rule.StartClock,
				"end_clock":    rule.EndClock,
				"start_at":     rule.StartAt,
				"duration_min": rule.DurationMin,
				"status":       rule.Status,
			}).Error
	}); err != nil {
		auth.Fail(c, http.StatusInternalServerError, "更新失败")
		return
	}

	RunSourceScheduleTick(time.Now())
	auth.Ok(c, gin.H{"message": "已更新"})
}

// DeleteSourceSchedule 删除规则；若它正处于生效中，先把源还原成进窗口前的状态。
func DeleteSourceSchedule(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		auth.Fail(c, http.StatusBadRequest, "无效的规则 ID")
		return
	}
	var rule models.SourceSchedule
	if err := db.DB.First(&rule, id).Error; err != nil {
		auth.Fail(c, http.StatusNotFound, "规则不存在")
		return
	}
	if err := db.DB.Transaction(func(tx *gorm.DB) error {
		if err := releaseSchedule(tx, rule); err != nil {
			return err
		}
		return tx.Delete(&models.SourceSchedule{}, rule.ID).Error
	}); err != nil {
		auth.Fail(c, http.StatusInternalServerError, "删除失败")
		return
	}
	catalog.InvalidateDatasourcesCache()
	auth.Ok(c, gin.H{"message": "已删除"})
}
