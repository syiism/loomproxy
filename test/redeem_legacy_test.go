package test

// 存量哈希卡密回落回归：2026-09-29 前卡密落库为 SHA-256，明文化后兑换需
// 明文未命中时按哈希回落匹配——保证旧卡不失效。

import (
	"net/http"
	"testing"

	"loomproxy/db"
	"loomproxy/models"
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

// TestRedemptionLogKeepsLongInput 审计表要留得住「用户输入的原文」：
// 粘贴进来的可能是 64 位历史哈希，而 code 列以前只有 32（严格模式下整条 insert 失败，
// 调用侧又不检查错误 → 最想留档的那一次恰好没留档）。这条用例钉住列宽与写库成功。
func TestRedemptionLogKeepsLongInput(t *testing.T) {
	srv := newTestServer(t)
	token := registerUser(t, srv, "lg_u2", "lg_u2@example.com", "pass1234")

	longCode := models.HashRedemptionCode("粘贴了旧哈希的输入") // 64 位十六进制
	status, env := doJSON(t, srv, http.MethodPost, "/user/redeem",
		map[string]string{"code": longCode}, authHeader(token))
	// 这张卡不存在，兑换该失败——但失败也要完整留档
	if status != http.StatusBadRequest {
		t.Fatalf("不存在的卡兑换 status = %d, want 400（msg=%s）", status, env.Msg)
	}

	var logRow models.RedemptionLog
	if err := db.DB.Where("user_id = ? AND success = ?", userIDByName(t, "lg_u2"), false).
		Order("id DESC").First(&logRow).Error; err != nil {
		t.Fatalf("审计行没写进去（列过窄或错误被吞）: %v", err)
	}
	if len(logRow.Code) != 64 {
		t.Errorf("审计里的 code 长度 = %d, want 64（用户原文被截了）", len(logRow.Code))
	}
	if logRow.FailReason == "" {
		t.Error("失败原因未记录")
	}
}
