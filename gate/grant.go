package gate

// 授权与限额是同一件事，用一行表达（待办清单 P34，方案 A）。
//
// `quota_limits(scope='source', target=数据源码, limit=N, period=day)` 的一行同时说明：
// 这个套餐**能**用这个源，以及它的额度是多少（`limit=-1` = 可用但不设限）。
// 旧表 `quota_plan_data_sources` 从此不再被读——它和限额表是同一份事实的两处记录，
// 谁都可能被单独改一遍（P28·A1 的互覆盖就是它的前身）。表本身保留一个弃用期，
// 何时 DROP 由运维单独决定（那是 DDL，需要一次带备份的发布）。
//
// **语义反转是这次合并唯一的贵处，别记错**：过去「没有 limit 行」= 不限额；
// 现在「没有 limit 行」= **没有权限**。所以面板上的「删除限额」等价于「回收这个数据源」，
// 脚本直接删行也一样。存量数据靠 `db/seed.go` 的对齐步骤补齐（授权过但没行的补 `limit=-1`），
// 那条迁移是幂等的、只补行不删行，且补的是 -1（旧模型里「有授权行、无限额行」就是不限额，
// 搬迁不得顺手给用户新加一道上限）。

import (
	"log"

	"loomproxy/db"
	"loomproxy/models"
)

// PlanSourceGrants 套餐已授权的数据源名集合（scope='source' 的 target 列）。
// global 作用域的行（如 `api` 全局限额）**不参与**授权判定——那是另一轴。
func PlanSourceGrants(planID uint) map[string]bool {
	// 与 PlanSourceLimits 同一份查询：限额行存在=授权，值不参与这里的判定
	limits := PlanSourceLimits(planID)
	grants := make(map[string]bool, len(limits))
	for name := range limits {
		grants[name] = true
	}
	return grants
}

// PlanHasSource 单个源的快速判定（访问控制走这条，免得为一行判定读全表）
func PlanHasSource(planID uint, sourceName string) bool {
	if planID == 0 || sourceName == "" {
		return false
	}
	var count int64
	if err := db.DB.Model(&models.QuotaLimit{}).
		Where("plan_id = ? AND scope = ? AND target = ?", planID, "source", sourceName).
		Count(&count).Error; err != nil {
		log.Printf("ERROR: PlanHasSource query failed: %v", err)
		return false
	}
	return count > 0
}

// FreePlanAllowsSource 匿名请求的判据：免费套餐有没有这个源（免费版没授权就是不给匿名用）
func FreePlanAllowsSource(sourceName string) bool {
	var free models.QuotaPlan
	if err := db.DB.Where("code = ?", "free").First(&free).Error; err != nil {
		return false
	}
	return PlanHasSource(free.ID, sourceName)
}

// PlanAllowedSourceIDs 授权源的数据源 ID 集合，给 /datasources 这类按 ID 过滤的调用方。
// 名字查不到 ID 的行（源已下线、限额行还没清）会被自然跳过。
func PlanAllowedSourceIDs(planID uint) map[uint]bool {
	ids := make(map[uint]bool)
	names := PlanSourceGrants(planID)
	if len(names) == 0 {
		return ids
	}
	var rows []models.DataSource
	if err := db.DB.Where("name IN ?", keysOf(names)).Select("id").Find(&rows).Error; err != nil {
		log.Printf("ERROR: PlanAllowedSourceIDs query failed: %v", err)
		return ids
	}
	for _, r := range rows {
		ids[r.ID] = true
	}
	return ids
}

// GrantPlanSource 授予一个源 = 保证有一行 limit；已存在则不动（幂等）。
// 新建行取该套餐的每源默认档（db.DefaultPerSourceLimit，与播种同源；自定义套餐 = -1 不限）。
func GrantPlanSource(planID uint, sourceName string) (bool, error) {
	if PlanHasSource(planID, sourceName) {
		return false, nil
	}
	var plan models.QuotaPlan
	if err := db.DB.Where("id = ?", planID).First(&plan).Error; err != nil {
		return false, err
	}
	// 行的构造在 db（P69 的两个意图口之一），判定与写仍然分开
	row := db.NewSourceGrantRow(planID, plan.Code, sourceName)
	if err := db.DB.Create(&row).Error; err != nil {
		return false, err
	}
	return true, nil
}

// UngrantPlanSource 回收一个源 = 删掉那行 limit。
// 调用方要清楚这一刀同时取消了该源在此套餐下的限额——它们是同一行。
func UngrantPlanSource(planID uint, sourceName string) error {
	return db.DB.Where("plan_id = ? AND scope = ? AND target = ?", planID, "source", sourceName).
		Delete(&models.QuotaLimit{}).Error
}

// FilterByPlan 保留套餐已授权的源、保持传入顺序。
// 授权集为空（含套餐不存在、planID=0）就返回空列表——**失败关闭**：
// 读不到授权就不发源，比"当作没限制"安全。四处按套餐过滤的调用方共用这一份，
// 免得每处各写一遍 map（它们过去各读一次弃用表，是同一份事实的第四个副本）。
func FilterByPlan(rows []models.DataSource, planID uint) []models.DataSource {
	allowed := PlanAllowedSourceIDs(planID)
	filtered := make([]models.DataSource, 0, len(allowed))
	if len(allowed) == 0 {
		return filtered
	}
	for _, ds := range rows {
		if allowed[ds.ID] {
			filtered = append(filtered, ds)
		}
	}
	return filtered
}

// FreePlanID 免费套餐 ID（找不到返回 0，调用方据此失败关闭）
func FreePlanID() uint {
	var free models.QuotaPlan
	if err := db.DB.Where("code = ?", "free").First(&free).Error; err != nil {
		return 0
	}
	return free.ID
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
