package db

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"time"

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

// RevokeSession 吊销单个会话
func RevokeSession(sessionID string) {
	now := time.Now()
	DB.Model(&models.AuthSession{}).
		Where("session_id = ? AND revoked_at IS NULL", sessionID).
		Update("revoked_at", now)
}

// RevokeOtherSessions 吊销用户除 keepSessionID 外的全部会话，返回吊销数量
func RevokeOtherSessions(userID uint, keepSessionID string) int64 {
	now := time.Now()
	res := DB.Model(&models.AuthSession{}).
		Where("user_id = ? AND session_id <> ? AND revoked_at IS NULL", userID, keepSessionID).
		Update("revoked_at", now)
	return res.RowsAffected
}

// RevokeSessionByID 吊销用户的指定会话（仅限本人），返回是否命中
func RevokeSessionByID(userID uint, id uint) bool {
	now := time.Now()
	res := DB.Model(&models.AuthSession{}).
		Where("id = ? AND user_id = ? AND revoked_at IS NULL", id, userID).
		Update("revoked_at", now)
	return res.RowsAffected > 0
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
	DB.Where("user_id = ? AND revoked_at IS NULL AND expires_at > ?", userID, time.Now()).
		Order("last_active_at DESC").Find(&sessions)
	return sessions
}
