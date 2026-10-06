package db

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"time"

	"gorm.io/gorm"

	"loomproxy/models"
)

// lastActiveMinInterval LastActiveAt 更新的最小间隔，避免每次请求都写库
const lastActiveMinInterval = 5 * time.Minute

// NewSessionID 生成随机会话 ID（同时作为 JWT 的 jti）
func NewSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// CreateSession 创建登录会话
func CreateSession(s *models.AuthSession) error {
	return DB.Create(s).Error
}

// ValidateSession 校验会话有效性（未吊销、未过期），并按需节流更新最后活跃时间
func ValidateSession(sessionID string, userID uint) bool {
	if sessionID == "" {
		return false
	}
	var s models.AuthSession
	if err := DB.Where("session_id = ? AND user_id = ?", sessionID, userID).First(&s).Error; err != nil {
		return false
	}
	now := time.Now()
	if s.RevokedAt != nil || now.After(s.ExpiresAt) {
		return false
	}
	if now.Sub(s.LastActiveAt) > lastActiveMinInterval {
		DB.Model(&s).Update("last_active_at", now)
	}
	return true
}

// RevokeSession 吊销单个会话。
//
// **报错必须交回调用方**（待办清单 P56）：会话行就是鉴权的执行点
// （`AuthRequired` → `ValidateSession` 每请求查 `revoked_at`），
// 所以"吊销没做成"不等于"没有会话要吊销"——它等于**那个人还能继续用**。
// 这几个函数原来只返回计数/布尔，把 `res.Error` 整个丢掉：一次失败的 UPDATE 与
// "本来就没有可吊销的行"长得一模一样（都是 0 / false），调用方于是心安理得地回 200。
func RevokeSession(sessionID string) error {
	now := time.Now()
	return DB.Model(&models.AuthSession{}).
		Where("session_id = ? AND revoked_at IS NULL", sessionID).
		Update("revoked_at", now).Error
}

// RevokeOtherSessions 吊销用户除 keepSessionID 外的全部会话，返回吊销数量与错误。
func RevokeOtherSessions(userID uint, keepSessionID string) (int64, error) {
	return RevokeOtherSessionsTx(DB, userID, keepSessionID)
}

// RevokeOtherSessionsTx 同一件事，但跑在调用方给的事务里。
//
// 为什么要有这个口子：改密码 / 找回密码 / 禁用 / 删除这几处，语义都是
// 「凭证变了，别人的登录要一起作废」。分两条独立语句写，中间任何一步失败就正好造出
// 最坏的组合——**密码改了、旧会话还活着**，而响应已经说了"成功"。放进同一个事务，
// 回滚就是一起回滚，用户拿到 500 时知道这次没做成。
func RevokeOtherSessionsTx(tx *gorm.DB, userID uint, keepSessionID string) (int64, error) {
	now := time.Now()
	res := tx.Model(&models.AuthSession{}).
		Where("user_id = ? AND session_id <> ? AND revoked_at IS NULL", userID, keepSessionID).
		Update("revoked_at", now)
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// RevokeSessionByID 吊销用户的指定会话（仅限本人）。
// 返回「是否命中」与错误：**没命中与查失败是两件事**——
// 合成一个 false，面板就会在数据库出问题时对用户说"这个会话不存在"。
func RevokeSessionByID(userID uint, id uint) (bool, error) {
	now := time.Now()
	res := DB.Model(&models.AuthSession{}).
		Where("id = ? AND user_id = ? AND revoked_at IS NULL", id, userID).
		Update("revoked_at", now)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// RevokeExcessSessions 保留该用户最近活跃的 maxKeep 个会话（keepSessionID 一定在保留之列，
// 那是刚签发的当前会话），其余吊销，返回移出数量。
//
// 这是多设备监控唯一的处置动作，**判据是会话数而不是 IP 数**：一个出口 IP 后面可能是整个公司，
// 按 IP 踢人必然大面积误伤（待办清单 P44）。maxKeep<=0 视为不处置。
func RevokeExcessSessions(userID uint, keepSessionID string, maxKeep int) int64 {
	if maxKeep <= 0 {
		return 0
	}
	var keep models.AuthSession
	if err := DB.Where("session_id = ? AND user_id = ?", keepSessionID, userID).First(&keep).Error; err != nil {
		return 0
	}
	// 保留 maxKeep-1 个「比当前会话更早活跃」的，加当前会话凑成 maxKeep 个。
	// **一次取回、在 Go 里切片**，不用 SQL 的 OFFSET：MySQL 要求 `OFFSET` 必须跟 `LIMIT` 同现
	// （裸 `... ORDER BY x OFFSET 4` 直接 1064，sqlite 与 PostgreSQL 却接受），
	// 而一个用户的活跃会话本来就是个位数，取回全部不比分页贵。
	var candidates []models.AuthSession
	if err := DB.Where("user_id = ? AND revoked_at IS NULL AND expires_at > ? AND id <> ? AND last_active_at <= ?",
		userID, time.Now(), keep.ID, keep.LastActiveAt).
		Order("last_active_at DESC").Find(&candidates).Error; err != nil {
		return 0
	}
	if len(candidates) <= maxKeep-1 {
		return 0
	}
	stale := candidates[maxKeep-1:]
	if len(stale) == 0 {
		return 0
	}
	ids := make([]uint, 0, len(stale))
	for _, s := range stale {
		ids = append(ids, s.ID)
	}
	now := time.Now()
	res := DB.Model(&models.AuthSession{}).Where("id IN ?", ids).Update("revoked_at", now)
	if res.Error != nil {
		log.Printf("ERROR: 移出多余会话失败 user_id=%d: %v", userID, res.Error)
		return 0
	}
	return res.RowsAffected
}

// RevokedSince 该用户在 since 之后被移出的会话数——登录提示用的就是它。
// **不建处置日志表**：`revoked_at` 本身带时刻，配合「上次登录时间」就能算出「你这次登录后有几台设备被移出」，
// 代价是分不出是自动处置还是用户自己点的「登出其他设备」——所以 journal 那行必须写清是哪一种（P44）。
func RevokedSince(userID uint, since time.Time) int64 {
	var n int64
	if err := DB.Model(&models.AuthSession{}).
		Where("user_id = ? AND revoked_at IS NOT NULL AND revoked_at >= ?", userID, since).
		Count(&n).Error; err != nil {
		log.Printf("ERROR: 统计移出会话失败 user_id=%d: %v", userID, err)
	}
	return n
}

// ListActiveSessions 列出用户未吊销且未过期的会话
func ListActiveSessions(userID uint) []models.AuthSession {
	var sessions []models.AuthSession
	if err := DB.Where("user_id = ? AND revoked_at IS NULL AND expires_at > ?", userID, time.Now()).
		Order("last_active_at DESC").Find(&sessions).Error; err != nil {
		LogReadFail("active_sessions:auth_sessions", err)
	}
	return sessions
}

// SessionWindowCond 统计窗口内**建立**的全部会话，含已吊销与已过期。
// 「设备与密钥」页的设备数 / IP 数 / 已吊销数按这条算：一次被吊销的登录照样留下它来自哪台设备哪个 IP，
// 那正是借号要看的痕迹——把它们一起筛掉，页面就只剩「现在在线的人」，而这页要抓的是「曾经换过人」。
func SessionWindowCond(windowStart time.Time) (string, []interface{}) {
	return "created_at >= ?", []interface{}{windowStart}
}

// ActiveSessionCond 窗口内的「活跃会话」= 在窗口内建立 + 未吊销 + 未过期。
// **读侧两处必须共用它**：管理端「设备与密钥」页的 active_sessions，与用户管理里「会话数超上限」的筛选
// ——两边口径不一致时，筛出来的人和页面上排出来的人会对不上，而那正是这页存在的意义。
func ActiveSessionCond(windowStart time.Time) (string, []interface{}) {
	cond, args := SessionWindowCond(windowStart)
	return cond + " AND revoked_at IS NULL AND expires_at > ?", append(args, time.Now())
}

// ActiveSessionCounts 按 ActiveSessionCond 一次算出多个用户的活跃会话数。
// 刻意不在 Go 里数（`revoked_at == nil && expires_at > now`）：那等于把同一条定义写两遍，
// 而写两遍的定义迟早只改其中一遍——上一版就是这么把 revoked_recent 静默做成恒 0 的。
func ActiveSessionCounts(userIDs []uint, windowStart time.Time) map[uint]int64 {
	out := make(map[uint]int64, len(userIDs))
	if len(userIDs) == 0 {
		return out
	}
	cond, args := ActiveSessionCond(windowStart)
	var rows []struct {
		UserID uint
		N      int64
	}
	if err := DB.Model(&models.AuthSession{}).
		Select("user_id, COUNT(*) AS n").
		Where("user_id IN ? AND "+cond, append([]interface{}{userIDs}, args...)...).
		Group("user_id").Scan(&rows).Error; err != nil {
		return out
	}
	for _, r := range rows {
		out[r.UserID] = r.N
	}
	return out
}

// UserIDsWithSessionsOver 用子查询把「窗口内活跃会话数 > cap」的账号取出来。谓词左侧是
// **users.id**——用户表没有 user_id 这一列，写成裸 user_id 的症状是筛选直接 500。
func UserIDsWithSessionsOver(windowStart time.Time, cap int) (string, []interface{}) {
	cond, args := ActiveSessionCond(windowStart)
	return "users.id IN (SELECT user_id FROM auth_sessions WHERE " + cond +
		" GROUP BY user_id HAVING COUNT(*) > ?)", append(args, cap)
}
