package db

// 一行 `quota_limits(scope=source, target=数据源码)` 同时是「该套餐可用该源」与「该源的限额」（P34）。
// 它的形状过去在四处各拼一遍，而**缺省值有两份**：helper 与播种取默认档、通用「限制项」CRUD 静默给 0——
// 于是"同一次授权"按通路落成 `100/day` 或 `0/month` 两种行（P70 摘掉 period 之后，剩下的分歧就是 `Limit` 这一半）。
//
// 拍板（待办清单 P69，2026-10-06 选 A）：**两个意图口，各一个构造处**——
//   - `NewDefaultSourceGrant`：「默认档授权」。建套餐时铺满全部声明源、新接入的源补授权、
//     面板与脚本授予一个源，都走它；限额只从 `DefaultPerSourceLimit` 取，别处不许再写字面量。
//   - `NewExplicitGrant`：「调用方显式给值」。管理员在「限制项」里给的数，以及旧数据迁移里的**忠实还原**
//     （旧模型「有关联行、没限额行」= 不限额，搬成默认档等于给老实人新加一道上限，那一处必须显式 -1）。
//
// 为什么落在 `db` 而不是 `gate`：依赖方向不许反过来（`db` 不能导入 `gate`，而播种也在 `db` 里），
// 所以**行的构造在 `db`、授权动作在 `gate`**；判定口仍然只有 `gate/grant.go` 那一处
// （判定与写分离是 P34 的原设计，不因这次收口而合并）。
//
// 一句话判据：**看到 `models.QuotaLimit{` 出现在这两个函数之外，就是第五个构造点**——
// 加字段、改默认时最容易出现"改了四处漏一处"，而那正是这一条要防的静默不同步。

import "loomproxy/models"

// NewExplicitGrant 显式给值的那一行。scope/target 的取值口径写在这里：
// "source" 配数据源码、"global" 配 "api"，别处不要再各自拼字符串常量。
func NewExplicitGrant(planID uint, scope, target string, limit int64) models.QuotaLimit {
	return models.QuotaLimit{PlanID: planID, Scope: scope, Target: target, Limit: limit}
}

// NewSourceGrantRow 默认档授权：限额只从该套餐的默认档取（自定义套餐 = -1 不限）。
// 名字里带 Grant 而函数只造行不写库——写与判定的分界保持在调用方。
func NewSourceGrantRow(planID uint, planCode, sourceName string) models.QuotaLimit {
	return NewExplicitGrant(planID, "source", sourceName, DefaultPerSourceLimit(planCode))
}
