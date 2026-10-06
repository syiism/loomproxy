package gate

// 额度转移（待办清单 P97）：把某个数据源的**日限额增量**挪给同一个人的另一个源。
//
// 归属先钉住：限额优先级是「用户数据源级覆盖（**追加语义**）> 套餐限额 > 不限」，
// 所以转移的唯一合法落点是 `user_quota_overrides` 那两行的数字重排——**不新造第三份限额事实**，
// 也不许回改 `quota_usage_logs`（那是流水，改它等于在总和里掺假账，P41 那条）。
//
// 这一份实现同时是「判定只有一个入口」的那条：本人端与管理端都调 TransferQuota，
// 四条边界（不限额两端拒 / 目标须已授权 / 可转量上限 / 落 0 删行）**只在这里判一次**。

import (
	"errors"

	"loomproxy/db"
	"loomproxy/models"

	"gorm.io/gorm"
)

// 六条拒绝成因。处理端按成因回**给人看的固定句**（§10：不把内部形状与 SQL 发出去）。
var (
	ErrTransferAmount       = errors.New("转移数量必须为正整数")
	ErrTransferSame         = errors.New("转出与转入是同一个数据源")
	ErrTransferSource       = errors.New("数据源不存在或已停用")
	ErrTransferUnlimited    = errors.New("不限额的源不参与转移")
	ErrTransferNoGrant      = errors.New("目标源未授权给该用户")
	ErrTransferInsufficient = errors.New("转出源的当日限额不足")
)

// TransferResult 转移前后的有效额度，给响应与审计行用。
type TransferResult struct {
	Amount     int64
	FromCode   string
	ToCode     string
	FromBefore int64
	FromAfter  int64
	ToBefore   int64
	ToAfter    int64
	// 守恒是这条功能的底线：转出恰减 n、转入恰加 n，两端增量之和为 0。
	// 它是**结构保证**而不是运行时检查——写这句是因为审计行把前后额度都记下来了，
	// 而 `sum(effective)` 全表对账没人做：真要防"某天有人改了这里的算术"，那得是一条用例（已补）。
	// FromOverride / ToOverride 是转移后写回（或删除）的**增量**；0 表示那一行已删除（回到无覆盖）。
	FromOverride int64
	ToOverride   int64
}

// TransferQuota 执行一次转移。operatorID 在本人自助时等于 userID，管理端操作时是管理员的 ID——
// 它必须进审计行，否则"谁动了这个人的额度分布"在记录里分不出来。
func TransferQuota(userID, operatorID uint, fromCode, toCode string, amount int64, via string) (*TransferResult, error) {
	if amount <= 0 {
		return nil, ErrTransferAmount
	}
	if fromCode == "" || toCode == "" {
		return nil, ErrTransferSource
	}
	if fromCode == toCode {
		return nil, ErrTransferSame
	}
	var user models.User
	if err := db.DB.First(&user, userID).Error; err != nil {
		return nil, err
	}
	// 两端都必须是**存在的、启用的**数据源：源名写错不该静默造出一行永远花不掉的覆盖
	var srcCount int64
	if err := db.DB.Model(&models.DataSource{}).
		Where("name IN ? AND status = ?", []string{fromCode, toCode}, 1).Count(&srcCount).Error; err != nil {
		return nil, err
	}
	if srcCount != 2 {
		return nil, ErrTransferSource
	}

	plan := ResolvePlanForUser(&user)
	limits := PlanSourceLimits(plan.ID)

	// 「有没有这一行」在这一张表上同时承担两件事（P34）：既是有权、也是限额。
	// 所以 `!hasPlan` 对**转入侧**就是"未授权"，对**转出侧**与 `base<0` 一起归成"不参与转移"。
	baseFrom, hasFrom := limits[fromCode]
	baseTo, hasTo := limits[toCode]
	if !hasTo || baseTo < 0 {
		if !hasTo {
			return nil, ErrTransferNoGrant
		}
		return nil, ErrTransferUnlimited
	}
	if !hasFrom || baseFrom < 0 {
		return nil, ErrTransferUnlimited
	}

	ovFrom := currentOverride(user.ID, fromCode)
	ovTo := currentOverride(user.ID, toCode)
	res := &TransferResult{
		Amount:   amount,
		FromCode: fromCode,
		ToCode:   toCode,
	}
	// 全部算术在 `transferPlan` 这一处（纯函数、可包内测）——理由写在那个文件里：
	// 有效额度与覆盖增量差一个 `base`，错位不报错、只让额度凭空变多。
	// 纯函数放包内而不是 `utils`：`utils` 已经在用 `models`，让它反向接收就是 import cycle，
	// 这条边界是被编译器教的，不是设计偏好。
	m, ok := transferPlan(baseFrom, ovFrom, baseTo, ovTo, amount)
	if !ok {
		return nil, ErrTransferInsufficient
	}
	res.FromBefore, res.FromAfter = m.FromBefore, m.FromAfter
	res.ToBefore, res.ToAfter = m.ToBefore, m.ToAfter
	res.FromOverride, res.ToOverride = m.FromOverrideAfter, m.ToOverrideAfter

	err := db.DB.Transaction(func(tx *gorm.DB) error {
		if err := putOverride(tx, user.ID, fromCode, res.FromOverride); err != nil {
			return err
		}
		if err := putOverride(tx, user.ID, toCode, res.ToOverride); err != nil {
			return err
		}
		// 审计行与两行覆盖**同一个事务**（§10 判定输入表那条）：额度挪了而记录没留下，
		// 等于这件事没发生过——而这正是最需要回读的那次。
		return tx.Create(&models.QuotaTransferLog{
			UserID:       user.ID,
			OperatorID:   operatorID,
			Via:          via,
			FromCode:     fromCode,
			ToCode:       toCode,
			Amount:       amount,
			FromBefore:   res.FromBefore,
			FromAfter:    res.FromAfter,
			ToBefore:     res.ToBefore,
			ToAfter:      res.ToAfter,
			FromOverride: res.FromOverride,
			ToOverride:   res.ToOverride,
		}).Error
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// currentOverride 读**原始增量**（包括"没有这一行"= 0）。
// 不能用 userOverrideLimit：它把 0 折成 ok=false，而转移要写的正是这个增量本身。
func currentOverride(userID uint, code string) int64 {
	var row models.UserQuotaOverride
	if err := db.DB.Where("user_id = ? AND group_code = ?", userID, code).First(&row).Error; err != nil {
		return 0
	}
	return row.Limit
}

// putOverride 写回一个增量。**增量为 0 时删行而不是留一行 0**：
// 0 在这张表上的语义是「没有覆盖」，留着一行 0 等于让「被手工刷过额度」那个筛选（P46）
// 把没刷过的人数进去——**同一个值在两张脸上说两种话**，删行是让它只说一种。
func putOverride(tx *gorm.DB, userID uint, code string, limit int64) error {
	if limit == 0 {
		return tx.Where(map[string]interface{}{"user_id": userID, "group_code": code}).
			Delete(&models.UserQuotaOverride{}).Error
	}
	var row models.UserQuotaOverride
	err := tx.Where(map[string]interface{}{"user_id": userID, "group_code": code}).First(&row).Error
	if err == nil {
		// 列名走 map 形式让 GORM 按方言加引号（`limit` 是 MySQL 保留字，§10 那条）
		return tx.Model(&row).Updates(map[string]interface{}{"limit": limit}).Error
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return tx.Create(&models.UserQuotaOverride{UserID: userID, GroupCode: code, Limit: limit}).Error
	}
	return err
}

// TransferErrorStatus 把成因翻成（HTTP 状态码，给人看的那一句）。
//
// 放在**成因旁边**而不是某个 handler 里，是因为两个入口（本人端与管理端）必须翻同一份：
// 表长在端点包里，就会出现"新增一个成因、只有一头改了文案"——另一头把内部错误原文发出去，
// 那正是 §10 第三条出口（我们自己把 err.Error() 拼进响应）要防的形状。
// 句子都不带 SQL、表名与内部形状（P90）。
func TransferErrorStatus(err error) (int, string) {
	switch {
	case errors.Is(err, ErrTransferAmount):
		return 400, "转移数量必须是正整数"
	case errors.Is(err, ErrTransferSame):
		return 400, "转出与转入是同一个数据源"
	case errors.Is(err, ErrTransferSource):
		return 400, "数据源不存在或已停用"
	case errors.Is(err, ErrTransferUnlimited):
		return 400, "不限额的源不参与转移"
	case errors.Is(err, ErrTransferNoGrant):
		return 400, "目标源未授权给该用户"
	case errors.Is(err, ErrTransferInsufficient):
		return 400, "转出源的当日限额不足"
	default:
		return 500, "转移失败"
	}
}
