// Package verify 场景化验证码：发码（Issue）→ 校验（Check）→ 业务成功后消费（Consume）。
//
// 超前设计说明（2026-09-07）：当前默认全部场景关闭（verify_code_scenes 留空，业务行为与
// 历史完全一致）；后期接入发码平台时只需两步、零代码改动：
//  1. 系统设置 verify_code_scenes 填入要启用的场景（如 register,forgot_password）；
//  2. verify_provider 改为 http 并配置 verify_http_* 模板（对接平台的发码 HTTP 接口），
//     或为平台新增专属 Sender（sender.go 加一个 case）。
package verify

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync/atomic"
	"time"

	"loomproxy/db"
	"loomproxy/models"
)

// 内置场景。新增场景：加常量 + 在 targetValidated 中补充目标格式校验 + 业务侧接入 Check/Consume。
const (
	SceneRegister       = "register"
	SceneForgotPassword = "forgot_password"
)

// 常见配置错误/业务错误（Error() 直接作为响应 msg）
var (
	ErrSceneDisabled   = errors.New("该场景未启用验证码")
	ErrSceneUnknown    = errors.New("未知的验证码场景")
	ErrTargetInvalid   = errors.New("目标格式不正确")
	ErrSendTooFrequent = errors.New("验证码发送过于频繁，请稍后再试")
	ErrSendLimitDaily  = errors.New("今日验证码发送次数已达上限，请明日再试")
	ErrNotFound        = errors.New("请先获取验证码")
	ErrExpired         = errors.New("验证码已过期，请重新获取")
	ErrTooManyAttempts = errors.New("错误次数过多，该验证码已作废，请重新获取")
	ErrMismatch        = errors.New("验证码错误")
)

// Setting keys（seed 预置，管理后台「系统设置」可改；db.GetSetting 10s 缓存）
const (
	settingScenes      = "verify_code_scenes"       // 启用场景，逗号分隔；留空=全部关闭
	settingIntervalSec = "verify_send_interval_sec" // 同目标两次发码最小间隔（秒）
	settingDailyLimit  = "verify_daily_send_limit"  // 同目标/同 IP 每日发码上限
	settingTTLSec      = "verify_code_ttl_sec"      // 验证码有效期（秒）
	settingMaxAttempts = "verify_code_max_attempts" // 单码最大验证失败次数
)

// builtinScenes 合法场景集合（/verify/send 入口校验，防任意 scene 落库）
var builtinScenes = map[string]bool{
	SceneRegister:       true,
	SceneForgotPassword: true,
}

// SceneEnabled 场景是否启用了验证码校验
func SceneEnabled(scene string) bool {
	for _, s := range strings.Split(db.GetSetting(settingScenes), ",") {
		if strings.TrimSpace(s) == scene {
			return true
		}
	}
	return false
}

// EnabledScenes 返回已启用的场景列表（/verify/config 给前端决定是否渲染验证码输入）
func EnabledScenes() []string {
	scenes := []string{}
	for _, s := range strings.Split(db.GetSetting(settingScenes), ",") {
		if t := strings.TrimSpace(s); t != "" {
			scenes = append(scenes, t)
		}
	}
	return scenes
}

// 下面四个设置读取全部走 `db.SettingInt`。这里原来有一份自己的实现：手写逐字符十进制解析，
// 于是任何非数字字符（含粘贴进来的首尾空白）与 0 都**静默**回默认，
// 而一条超长的数字串会 wrap 出一个没人认识的数（实测 `-8814407034` 被当成秒用）——
// 填错的人永远看不到任何提示（待办清单 P61）。
func ttl() time.Duration { return time.Duration(db.SettingInt(settingTTLSec, 600)) * time.Second }
func interval() time.Duration {
	return time.Duration(db.SettingInt(settingIntervalSec, 60)) * time.Second
}
func dailyLimit() int  { return db.SettingInt(settingDailyLimit, 10) }
func maxAttempts() int { return db.SettingInt(settingMaxAttempts, 5) }

// ValidTarget 目标格式校验：当前两个场景均为邮箱（与前端 register/forgot 的正则一致）。
// 接入短信场景时按 Target 类型分流（手机号正则），此处统一收口。
func ValidTarget(scene, target string) bool {
	if strings.Contains(scene, "password") || scene == SceneRegister {
		// 邮箱
		at := strings.IndexByte(target, '@')
		return at > 0 && at < len(target)-1 && strings.IndexByte(target[at:], '.') > 0
	}
	return target != ""
}

// NormalizeTarget 目标归一化（邮箱小写、去空白）；发码与校验两侧必须一致
func NormalizeTarget(target string) string {
	return strings.ToLower(strings.TrimSpace(target))
}

// genCode 生成 6 位数字验证码（crypto/rand）
func genCode() (string, error) {
	max := big.NewInt(1000000)
	for {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		// 首位不为 0，避免某些短信平台吞前导零
		if n.Int64() >= 100000 {
			return fmt.Sprintf("%d", n.Int64()), nil
		}
	}
}

// lastSweep 上次全表过期清理的 unix 时间戳（节流 1 小时，避免每次发码都全表删）
var lastSweep atomic.Int64

// CleanupExpired 删除已过期验证码行（Issue 时对同目标顺带清理；全表清理按 1h 节流）
func CleanupExpired() {
	db.DB.Where("expires_at < ?", time.Now()).Delete(&models.VerificationCode{})
	lastSweep.Store(time.Now().Unix())
}

func maybeSweep() {
	if time.Now().Unix()-lastSweep.Load() > 3600 {
		CleanupExpired()
	}
}

// beijingDayStart 与额度计费同口径：北京时间零点重置
func beijingDayStart() time.Time {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Now().In(loc)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
}

// Issue 生成并发送验证码。返回过期时间。发送失败不入库。
func Issue(ip, scene, target string) (time.Time, error) {
	if !builtinScenes[scene] {
		return time.Time{}, ErrSceneUnknown
	}
	if !SceneEnabled(scene) {
		return time.Time{}, ErrSceneDisabled
	}
	target = NormalizeTarget(target)
	if !ValidTarget(scene, target) {
		return time.Time{}, ErrTargetInvalid
	}

	now := time.Now()
	dayStart := beijingDayStart()

	// 限频三重：同目标冷却 / 同目标每日 / 同 IP 每日（按行统计，量小直查库）
	var latest models.VerificationCode
	if err := db.DB.Where("scene = ? AND target = ?", scene, target).
		Order("id DESC").First(&latest).Error; err == nil {
		if wait := latest.CreatedAt.Add(interval()).Sub(now); wait > 0 {
			return time.Time{}, fmt.Errorf("%w（约 %d 秒后可重试）", ErrSendTooFrequent, int(wait.Seconds())+1)
		}
	}
	var targetCount, ipCount int64
	db.DB.Model(&models.VerificationCode{}).
		Where("target = ? AND created_at >= ?", target, dayStart).Count(&targetCount)
	db.DB.Model(&models.VerificationCode{}).
		Where("ip = ? AND created_at >= ?", ip, dayStart).Count(&ipCount)
	if targetCount >= int64(dailyLimit()) || ipCount >= int64(dailyLimit()) {
		return time.Time{}, ErrSendLimitDaily
	}

	code, err := genCode()
	if err != nil {
		return time.Time{}, fmt.Errorf("生成验证码失败: %w", err)
	}
	if err := getSender().Send(scene, target, code); err != nil {
		return time.Time{}, fmt.Errorf("验证码发送失败，请稍后再试")
	}

	expires := now.Add(ttl())
	row := models.VerificationCode{
		Scene:     scene,
		Target:    target,
		CodeHash:  models.HashRedemptionCode(code),
		IP:        ip,
		ExpiresAt: expires,
	}
	if err := db.DB.Create(&row).Error; err != nil {
		return time.Time{}, fmt.Errorf("验证码写入失败，请稍后再试")
	}

	// 顺带清理：同目标过期行 + 节流的全表过期清理
	db.DB.Where("scene = ? AND target = ? AND expires_at < ?", scene, target, now).
		Delete(&models.VerificationCode{})
	maybeSweep()
	return expires, nil
}

// Check 校验验证码（不消费）：匹配成功返回该行，由业务成功后调 Consume 消费。
// 失败计次：单码失败达上限即作废。校验与消费分离，业务中途失败（如用户名冲突）不烧码。
func Check(scene, target, code string) (*models.VerificationCode, error) {
	target = NormalizeTarget(target)
	var row models.VerificationCode
	err := db.DB.Where("scene = ? AND target = ?", scene, target).
		Order("id DESC").First(&row).Error
	if err != nil {
		return nil, ErrNotFound
	}
	if row.ConsumedAt != nil {
		return nil, ErrExpired // 最新行已用掉，视为过期引导重取
	}
	if time.Now().After(row.ExpiresAt) {
		return nil, ErrExpired
	}
	if row.Attempts >= maxAttempts() {
		return nil, ErrTooManyAttempts
	}
	if models.HashRedemptionCode(strings.TrimSpace(code)) != row.CodeHash {
		db.DB.Model(&row).UpdateColumn("attempts", row.Attempts+1)
		if row.Attempts+1 >= maxAttempts() {
			return nil, ErrTooManyAttempts
		}
		return nil, ErrMismatch
	}
	return &row, nil
}

// Consume 消费验证码（一次性）。并发下同码仅一人成功。
func Consume(id uint) bool {
	res := db.DB.Model(&models.VerificationCode{}).
		Where("id = ? AND consumed_at IS NULL", id).
		Update("consumed_at", time.Now())
	return res.Error == nil && res.RowsAffected > 0
}
