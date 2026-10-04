package userconfig

import (
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"loomproxy/db"
	"loomproxy/handlers/auth"
	"loomproxy/handlers/catalog"
	"loomproxy/models"
)

// 兑换限频：每用户每分钟最多 5 次尝试；连续失败 10 次锁定 1 小时（内存计数，重启清零）
const (
	redeemMaxPerMinute = 5
	redeemMaxFails     = 10
	redeemLockDuration = time.Hour
)

var redeemLimiter = struct {
	sync.Mutex
	attempts map[uint]*redeemAttempt
}{attempts: make(map[uint]*redeemAttempt)}

type redeemAttempt struct {
	windowStart time.Time
	count       int
	failStreak  int
	lockedUntil time.Time
}

// checkRedeemLimit 返回锁定截止时间与是否被限制
func checkRedeemLimit(userID uint) (time.Time, bool) {
	redeemLimiter.Lock()
	defer redeemLimiter.Unlock()
	a := redeemLimiter.attempts[userID]
	if a == nil {
		return time.Time{}, false
	}
	if time.Now().Before(a.lockedUntil) {
		return a.lockedUntil, true
	}
	return time.Time{}, false
}

func recordRedeemAttempt(userID uint, success bool) {
	redeemLimiter.Lock()
	defer redeemLimiter.Unlock()
	a := redeemLimiter.attempts[userID]
	if a == nil {
		a = &redeemAttempt{windowStart: time.Now()}
		redeemLimiter.attempts[userID] = a
	}
	now := time.Now()
	if now.Sub(a.windowStart) > time.Minute {
		a.windowStart = now
		a.count = 0
	}
	a.count++
	if a.count > redeemMaxPerMinute {
		a.lockedUntil = now.Add(redeemLockDuration)
	}
	if success {
		a.failStreak = 0
	} else {
		a.failStreak++
		if a.failStreak >= redeemMaxFails {
			a.lockedUntil = now.Add(redeemLockDuration)
		}
	}
}

type redeemRequest struct {
	Code string `json:"code" binding:"required"`
}

// Redeem 用户兑换卡密升级套餐（见 docs/套餐升级方案.md）：
// 同套餐续费叠加时长；更高等级套餐立即切换重算时长；同级/更低级未到期（含永久）时拒绝；
// duration_days=0 的卡密为永久卡，兑换后 PlanExpireAt 置 NULL（resolvePlan 对 NULL 永不回退）
func Redeem(c *gin.Context) {
	uidVal, _ := c.Get("user_id")
	userID, _ := uidVal.(uint)

	if until, limited := checkRedeemLimit(userID); limited {
		auth.Fail(c, http.StatusTooManyRequests, "尝试过于频繁，请 "+until.Format("15:04")+" 后再试")
		return
	}

	var req redeemRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		auth.Fail(c, http.StatusBadRequest, "参数错误: 请输入卡密")
		return
	}
	code := strings.ToUpper(strings.TrimSpace(req.Code))

	var planName string
	var planID uint // 兑换到的那份套餐：显示别名按 (kind, target_id) 存，提示里要用它取别名
	var expireAt *time.Time
	failReason := ""

	err := db.DB.Transaction(func(tx *gorm.DB) error {
		var rc models.RedemptionCode
		if err := tx.Where("code = ?", code).First(&rc).Error; err != nil {
			// 存量哈希行回落（2026-09-29 前落库为 SHA-256，明文不可逆只能按哈希匹配）
			if err := tx.Where("code = ?", models.HashRedemptionCode(code)).First(&rc).Error; err != nil {
				failReason = "卡密不存在或已失效"
				return errors.New(failReason)
			}
		}
		if rc.Status != 1 {
			failReason = "卡密已被使用或已作废"
			return errors.New(failReason)
		}
		var plan models.QuotaPlan
		if err := tx.First(&plan, rc.PlanID).Error; err != nil || plan.Status != 1 {
			failReason = "卡密对应套餐不可用"
			return errors.New(failReason)
		}
		var user models.User
		if err := tx.Preload("Plan").First(&user, userID).Error; err != nil {
			failReason = "用户不存在"
			return errors.New(failReason)
		}

		now := time.Now()
		permanent := rc.DurationDays == 0 // 0=永久卡
		var newExpire *time.Time          // nil 表示永久（PlanExpireAt 写 NULL）
		if !permanent {
			exp := now.Add(time.Duration(rc.DurationDays) * 24 * time.Hour)
			newExpire = &exp
		}
		switch {
		case user.PlanID != nil && *user.PlanID == rc.PlanID:
			// 同套餐续费：永久卡直接置永久；当前已是永久时保持永久不降级；
			// 否则未到期在剩余时长上叠加，已过期从今日起算
			if permanent || user.PlanExpireAt == nil {
				newExpire = nil
			} else if now.Before(*user.PlanExpireAt) {
				exp := user.PlanExpireAt.Add(time.Duration(rc.DurationDays) * 24 * time.Hour)
				newExpire = &exp
			}
		case user.Plan != nil && (user.PlanExpireAt == nil || now.Before(*user.PlanExpireAt)) && user.Plan.Level >= plan.Level:
			// 当前套餐未到期（永久视为永不到期）且等级不低于卡密套餐：拒绝，防止误操作损失
			failReason = "当前套餐未到期，无法兑换同级或更低级套餐"
			return errors.New(failReason)
		}
		// 其余情况（无套餐/套餐已过期/更高等级）：立即切换，时长从今日起算（永久卡为永久）

		if err := tx.Model(&models.User{}).Where("id = ?", user.ID).
			Updates(map[string]interface{}{"plan_id": rc.PlanID, "plan_expire_at": newExpire}).Error; err != nil {
			failReason = "兑换失败"
			return err
		}
		// 套餐变更同步绑定角色（skipAdminRole=true：自助兑换永不自动授予 admin 角色）
		if err := db.SyncUserPlanRole(tx, user.ID, &plan, true); err != nil {
			failReason = "兑换失败"
			return err
		}
		// 条件更新销码并校验影响行数：并发下同一张码只有一个请求能成功，
		// RowsAffected=0 说明已被并发请求销掉，回滚整个事务
		res := tx.Model(&models.RedemptionCode{}).Where("id = ? AND status = 1", rc.ID).
			Updates(map[string]interface{}{"status": 2, "used_by": user.ID, "used_at": now})
		if res.Error != nil {
			failReason = "兑换失败"
			return res.Error
		}
		if res.RowsAffected == 0 {
			failReason = "卡密已被使用或已作废"
			return errors.New(failReason)
		}
		planName = plan.Name
		planID = rc.PlanID
		expireAt = newExpire
		return nil
	})

	success := err == nil
	recordRedeemAttempt(userID, success)
	// 审计行写不进去不能改兑换结果，但必须吭声：这张表存在的意义就是留下失败的那几次
	if logErr := db.DB.Create(&models.RedemptionLog{
		UserID:     userID,
		Code:       code,
		Success:    success,
		FailReason: failReason,
		IP:         c.ClientIP(),
	}).Error; logErr != nil {
		log.Printf("ERROR: 写卡密兑换审计日志失败（用户 %d，结果=%v）: %v", userID, success, logErr)
	}
	if !success {
		auth.Fail(c, http.StatusBadRequest, failReason)
		return
	}
	// 兑换后套餐已变更：失效 /datasources 缓存视图，用户立即看到新套餐的数据源
	catalog.InvalidateDatasourcesCache()
	// 兑换提示是**本人界面**，所以走同一处显示规则（别名优先，见 models.DisplayAlias / 待办清单 P43）：
	// 用户给这个套餐起过别名就该看到别名，看到默认名会变成「我明明改过、怎么还叫这个」。
	// 管理员侧的卡密批次列表（handlers/admin/redeem.go）保持默认名——那是运营事实，不受别名影响。
	auth.Ok(c, gin.H{
		"message":   "兑换成功",
		"plan_name": models.DisplayAlias(db.DisplayAliasesFor(userID), models.DisplayKindPlan, planID, planName),
		"expire_at": expireAt,
	})
}
