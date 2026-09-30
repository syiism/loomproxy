package db

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"loomproxy-go/models"
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

// ListActiveSessions 列出用户未吊销且未过期的会话
func ListActiveSessions(userID uint) []models.AuthSession {
	var sessions []models.AuthSession
	DB.Where("user_id = ? AND revoked_at IS NULL AND expires_at > ?", userID, time.Now()).
		Order("last_active_at DESC").Find(&sessions)
	return sessions
}
