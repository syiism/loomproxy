package test

// 存量哈希卡密回落回归：2026-09-29 前卡密落库为 SHA-256，明文化后兑换需
// 明文未命中时按哈希回落匹配——保证旧卡不失效。

import (
	"net/http"
	"testing"

	"loomproxy-go/db"
	"loomproxy-go/models"
)

func TestRedeemLegacyHashFallback(t *testing.T) {
	srv := newTestServer(t)
	planID := planIDByCode(t, "vip")

	// 直插一条「历史哈希」卡密（模拟 2026-09-29 前落库形态）
	legacyPlain := "LEGC-TEST-HASH-CARD"
	legacy := models.RedemptionCode{
		Code:         models.HashRedemptionCode(legacyPlain),
		PlanID:       planID,
		DurationDays: 3,
		Status:       1,
		BatchNo:      "legacy",
		Channel:      "manual",
	}
	if err := db.DB.Create(&legacy).Error; err != nil {
		t.Fatalf("构造存量哈希卡密失败: %v", err)
	}

	token := registerUser(t, srv, "lg_u1", "lg_u1@example.com", "pass1234")
	status, env := doJSON(t, srv, http.MethodPost, "/user/redeem",
		map[string]string{"code": legacyPlain}, authHeader(token))
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("存量哈希卡兑换失败: status=%d msg=%s", status, env.Msg)
	}
}
