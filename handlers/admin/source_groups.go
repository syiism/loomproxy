package admin

// 数据源分组管理：组是「视图与批量操作单位」，不是键。
// 存储与解析口径不变——quota_limits 的 target、quota_costs 的 group_code、套餐关联的
// data_source_id 全都按数据源逐行；这里的批量入口只是替管理员把「选中一批源」变成一次请求。

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"loomproxy/db"
	"loomproxy/handlers/auth"
	"loomproxy/handlers/catalog"
	"loomproxy/models"
)

// sourceGroupView 组列表项：成员数由查询算出，不落列（成员关系就是 data_sources.group_id）
type sourceGroupView struct {
	models.SourceGroup
	MemberCount int64 `json:"member_count"`
}

// ListSourceGroups 组列表（含未分组计数，供首页筛选栏与面板成员勾选使用）
func ListSourceGroups(c *gin.Context) {
	var groups []models.SourceGroup
	if err := db.DB.Order("sort_order ASC, id ASC").Find(&groups).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}

	counts := make(map[uint]int64)
	var rows []struct {
		GroupID *uint
		Cnt     int64
	}
	if err := db.DB.Model(&models.DataSource{}).
		Select("group_id, COUNT(1) AS cnt").
		Group("group_id").Scan(&rows).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}
	var ungrouped int64
	for _, r := range rows {
		if r.GroupID == nil {
			ungrouped = r.Cnt
			continue
		}
		counts[*r.GroupID] = r.Cnt
	}

	views := make([]sourceGroupView, 0, len(groups))
	for _, g := range groups {
		views = append(views, sourceGroupView{SourceGroup: g, MemberCount: counts[g.ID]})
	}
	auth.Ok(c, gin.H{"groups": views, "ungrouped_count": ungrouped})
}

type createSourceGroupRequest struct {
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
	Status      *int   `json:"status"`
	SortOrder   *int   `json:"sort_order"`
}

// CreateSourceGroup 新建分组
func CreateSourceGroup(c *gin.Context) {
	var req createSourceGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		auth.Fail(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		auth.Fail(c, http.StatusBadRequest, "组名不能为空")
		return
	}

	// 组名唯一：先查再写，否则约束冲突一路撞到驱动，变成没头没尾的 500
	var count int64
	if err := db.DB.Model(&models.SourceGroup{}).Where("name = ?", name).Count(&count).Error; err != nil {
		db.LogReadFail("source_group_dupcheck:source_groups", err)
	}
	if count > 0 {
		auth.Fail(c, http.StatusConflict, "分组名称已存在")
		return
	}

	status := 1
	if req.Status != nil {
		status = *req.Status
	}
	sortOrder := 0
	if req.SortOrder != nil {
		sortOrder = *req.SortOrder
	}
	group := models.SourceGroup{Name: name, Description: req.Description, Status: status, SortOrder: sortOrder}
	if err := db.DB.Create(&group).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "创建分组失败")
		return
	}
	// 组名进入 /datasources 与首页输出，改动必须立刻可见
	catalog.InvalidateDatasourcesCache()
	auth.Ok(c, group)
}

type updateSourceGroupRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Status      *int    `json:"status"`
	SortOrder   *int    `json:"sort_order"`
}

// UpdateSourceGroup 改组名/说明/停用/排序
func UpdateSourceGroup(c *gin.Context) {
	group, ok := findSourceGroup(c)
	if !ok {
		return
	}
	var req updateSourceGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		auth.Fail(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}

	updates := map[string]interface{}{}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			auth.Fail(c, http.StatusBadRequest, "组名不能为空")
			return
		}
		if name != group.Name {
			var count int64
			if err := db.DB.Model(&models.SourceGroup{}).Where("name = ? AND id <> ?", name, group.ID).Count(&count).Error; err != nil {
				db.LogReadFail("source_group_dupcheck_update:source_groups", err)
			}
			if count > 0 {
				auth.Fail(c, http.StatusConflict, "分组名称已存在")
				return
			}
		}
		updates["name"] = name
	}
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	if req.Status != nil {
		updates["status"] = *req.Status
	}
	if req.SortOrder != nil {
		updates["sort_order"] = *req.SortOrder
	}
	if len(updates) == 0 {
		auth.Fail(c, http.StatusBadRequest, "无更新字段")
		return
	}
	if err := db.DB.Model(group).Updates(updates).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "更新失败")
		return
	}
	if err := db.DB.First(group, group.ID).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}
	catalog.InvalidateDatasourcesCache()
	auth.Ok(c, group)
}

// DeleteSourceGroup 删除分组：成员回落未分组，源本身不删
func DeleteSourceGroup(c *gin.Context) {
	group, ok := findSourceGroup(c)
	if !ok {
		return
	}
	err := db.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.DataSource{}).Where("group_id = ?", group.ID).
			Update("group_id", nil).Error; err != nil {
			return err
		}
		// 硬删：SourceGroup 没有软删除列，组也不被任何表按 ID 引用（成员已在上面摘干净），
		// 留下行只会占住 name 唯一索引，让同名重建撞 409
		return tx.Delete(group).Error
	})
	if err != nil {
		auth.Fail(c, http.StatusInternalServerError, "删除失败")
		return
	}
	catalog.InvalidateDatasourcesCache()
	auth.Ok(c, gin.H{"message": "已删除"})
}

type updateGroupMembersRequest struct {
	SourceNames []string `json:"source_names"`
}

// UpdateSourceGroupMembers 整盘提交成员：列表内的进组（他从组移过来一并改，一源至多一组），
// 原成员不在列表内的回落未分组。勾选式界面一次提交即可，无需前端算增删
func UpdateSourceGroupMembers(c *gin.Context) {
	group, ok := findSourceGroup(c)
	if !ok {
		return
	}
	var req updateGroupMembersRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		auth.Fail(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}

	names := make([]string, 0, len(req.SourceNames))
	seen := make(map[string]bool, len(req.SourceNames))
	for _, raw := range req.SourceNames {
		name := strings.TrimSpace(raw)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}

	var members []models.DataSource
	if len(names) > 0 {
		if err := db.DB.Where("name IN ?", names).Find(&members).Error; err != nil {
			auth.Fail(c, http.StatusInternalServerError, "数据库错误")
			return
		}
	}
	// 不存在的源码直接拒绝：静默丢掉会让管理员以为勾上了，实际没有
	if len(members) != len(names) {
		found := make(map[string]bool, len(members))
		for _, m := range members {
			found[m.Name] = true
		}
		missing := make([]string, 0, len(names))
		for _, n := range names {
			if !found[n] {
				missing = append(missing, n)
			}
		}
		auth.Fail(c, http.StatusNotFound, "数据源不存在: "+strings.Join(missing, ", "))
		return
	}

	ids := make([]uint, 0, len(members))
	for _, m := range members {
		ids = append(ids, m.ID)
	}
	err := db.DB.Transaction(func(tx *gorm.DB) error {
		// 先摘掉本组本次不再包含的旧成员。空列表就是整组清空：SQL 里 `id NOT IN (空集)`
		// 恒不成立，直接拼上会让旧成员原地不动
		stale := tx.Model(&models.DataSource{}).Where("group_id = ?", group.ID)
		if len(ids) > 0 {
			stale = stale.Where("id NOT IN ?", ids)
		}
		if err := stale.Update("group_id", nil).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		return tx.Model(&models.DataSource{}).Where("name IN ?", names).Update("group_id", group.ID).Error
	})
	if err != nil {
		auth.Fail(c, http.StatusInternalServerError, "成员更新失败")
		return
	}
	catalog.InvalidateDatasourcesCache()
	auth.Ok(c, gin.H{"member_count": len(ids)})
}

type applyGroupLimitsRequest struct {
	PlanCode string `json:"plan_code" binding:"required"`
	Limit    *int64 `json:"limit" binding:"required"`
}

// ApplySourceGroupLimits 按组批量套用限额：存储仍是每源一行 quota_limits
// （scope=source、target=数据源码），组不落任何键。limit 语义与限额页一致：
// -1=不限，0=当日不可用，>0=每日次数
func ApplySourceGroupLimits(c *gin.Context) {
	group, ok := findSourceGroup(c)
	if !ok {
		return
	}
	var req applyGroupLimitsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		auth.Fail(c, http.StatusBadRequest, "参数错误: "+err.Error())
		return
	}
	if req.Limit == nil {
		auth.Fail(c, http.StatusBadRequest, "缺少 limit")
		return
	}

	var plan models.QuotaPlan
	if err := db.DB.Where("code = ?", req.PlanCode).First(&plan).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			auth.Fail(c, http.StatusNotFound, "套餐不存在")
			return
		}
		auth.Fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}

	var members []models.DataSource
	if err := db.DB.Where("group_id = ?", group.ID).Order("sort_order ASC, id ASC").Find(&members).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}
	if len(members) == 0 {
		auth.Fail(c, http.StatusBadRequest, "该分组没有成员，未套用")
		return
	}

	written := 0
	codes := make([]string, 0, len(members))
	skipped := make([]string, 0, len(members))
	err := db.DB.Transaction(func(tx *gorm.DB) error {
		for _, ds := range members {
			var row models.QuotaLimit
			err := tx.Where("plan_id = ? AND scope = ? AND target = ?", plan.ID, "source", ds.Name).First(&row).Error
			if err == nil {
				if err := tx.Model(&row).Update("limit", *req.Limit).Error; err != nil {
					return err
				}
				written++
				codes = append(codes, ds.Name)
				continue
			}
			if errors.Is(err, gorm.ErrRecordNotFound) {
				// **不新建行**：限额行现在就是授权行（待办清单 P34），
				// 「按组套用额度」一旦补行就等于替整组发权限，那是另一个动作，
				// 只能由人在「额度 · 套餐」页逐条做。跳过并回报，让他看得见自己漏了谁。
				skipped = append(skipped, ds.Name)
				continue
			}
			return err
		}
		return nil
	})
	if err != nil {
		auth.Fail(c, http.StatusInternalServerError, "限额套用失败")
		return
	}
	// G2：不写覆盖日志（可回滚靠限额页逐行看），只留一条服务端日志说明批量改动了哪些源
	log.Printf("按分组套用限额: group=%s plan=%s limit=%d sources=%s 未授权跳过=%s",
		group.Name, plan.Code, *req.Limit, strings.Join(codes, ","), strings.Join(skipped, ","))
	auth.Ok(c, gin.H{
		"applied": written, "plan_code": plan.Code, "limit": *req.Limit,
		"sources": codes, "skipped_ungranted": skipped,
	})
}

// findSourceGroup 按路径 id 取组，失败时已写好响应
func findSourceGroup(c *gin.Context) (*models.SourceGroup, bool) {
	id := c.Param("id")
	var group models.SourceGroup
	if err := db.DB.First(&group, id).Error; err != nil {
		auth.Fail(c, http.StatusNotFound, "分组不存在")
		return nil, false
	}
	return &group, true
}
