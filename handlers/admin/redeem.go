package admin

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"loomproxy/db"
	"loomproxy/handlers/auth"
	"loomproxy/models"
)

// 卡密字符集：Crockford Base32 去歧义（无 I/L/O/U）
const redeemCodeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// generateRedeemCode 生成 XXXX-XXXX-XXXX-XXXX 格式卡密
func generateRedeemCode() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	out := make([]byte, 0, 19)
	for i, v := range b {
		if i > 0 && i%4 == 0 {
			out = append(out, '-')
		}
		out = append(out, redeemCodeAlphabet[int(v)%len(redeemCodeAlphabet)])
	}
	return string(out)
}

// isLegacyHashRow 判断存量哈希行（2026-09-29 前落库为 64 位 SHA-256 hex，不可逆，
// 明文卡密持有者仍可正常兑换；列表仅作标记展示）
func isLegacyHashRow(code string) bool {
	if len(code) != 64 {
		return false
	}
	for _, c := range code {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

type createRedeemCodesRequest struct {
	PlanID       uint   `json:"plan_id" binding:"required"`
	DurationDays *int   `json:"duration_days" binding:"required"` // 0=永久
	Count        int    `json:"count" binding:"required"`
	Note         string `json:"note"`
}

// CreateRedeemCodes 批量生成卡密（明文落库，列表可随时查看）
func CreateRedeemCodes(c *gin.Context) {
	var req createRedeemCodesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		auth.Fail(c, http.StatusBadRequest, "参数错误: 需要 plan_id, duration_days, count")
		return
	}
	if req.Count < 1 || req.Count > 100 {
		auth.Fail(c, http.StatusBadRequest, "数量需在 1-100 之间")
		return
	}
	if *req.DurationDays < 0 {
		auth.Fail(c, http.StatusBadRequest, "有效天数不能为负（0=永久）")
		return
	}
	var plan models.QuotaPlan
	if err := db.DB.First(&plan, req.PlanID).Error; err != nil {
		auth.Fail(c, http.StatusBadRequest, "套餐不存在")
		return
	}

	batchNo := fmt.Sprintf("%s%04d", time.Now().Format("20060102150405"), time.Now().Nanosecond()%10000)
	codes := make([]models.RedemptionCode, 0, req.Count)
	plain := make([]string, 0, req.Count)
	for i := 0; i < req.Count; i++ {
		code := generateRedeemCode()
		codes = append(codes, models.RedemptionCode{
			Code:         code, // 明文落库
			PlanID:       req.PlanID,
			DurationDays: *req.DurationDays,
			Status:       1,
			BatchNo:      batchNo,
			Note:         req.Note,
			Channel:      "manual",
		})
		plain = append(plain, code)
	}
	if err := db.DB.Create(&codes).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "生成失败")
		return
	}
	auth.Ok(c, gin.H{
		"batch_no":  batchNo,
		"plan_name": plan.Name,
		"count":     len(codes),
		"codes":     plain,
		"notice":    "卡密已入库，可随时在列表中查看复制",
	})
}

// ListRedeemCodes 卡密分页查询（展示完整码），支持批次/状态/套餐过滤
func ListRedeemCodes(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	q := db.DB.Model(&models.RedemptionCode{})
	if v := c.Query("batch_no"); v != "" {
		q = q.Where("batch_no = ?", v)
	}
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	if v := c.Query("plan_id"); v != "" {
		q = q.Where("plan_id = ?", v)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}
	list := make([]models.RedemptionCode, 0, pageSize)
	if err := q.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error; err != nil {
		auth.Fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}
	// 附带套餐名与使用者用户名；明文直出，存量哈希行仅作标记
	planNames := make(map[uint]string)
	userNames := make(map[uint]string)
	items := make([]gin.H, 0, len(list))
	for _, rc := range list {
		codeDisplay := rc.Code
		if isLegacyHashRow(rc.Code) {
			codeDisplay = rc.Code[:8] + "…（旧哈希存量）"
		}
		planName := planNames[rc.PlanID]
		if planName == "" {
			var p models.QuotaPlan
			if db.DB.First(&p, rc.PlanID).Error == nil {
				planName = p.Name
				planNames[rc.PlanID] = planName
			}
		}
		usedByName := ""
		if rc.UsedBy != nil {
			if n, ok := userNames[*rc.UsedBy]; ok {
				usedByName = n
			} else {
				var u models.User
				if db.DB.First(&u, *rc.UsedBy).Error == nil {
					usedByName = u.Username
					userNames[*rc.UsedBy] = usedByName
				}
			}
		}
		items = append(items, gin.H{
			"id":            rc.ID,
			"code":          codeDisplay,
			"plan_id":       rc.PlanID,
			"plan_name":     planName,
			"duration_days": rc.DurationDays,
			"status":        rc.Status,
			"batch_no":      rc.BatchNo,
			"note":          rc.Note,
			"used_by_name":  usedByName,
			"used_at":       rc.UsedAt,
			"created_at":    rc.CreatedAt,
		})
	}
	auth.Ok(c, gin.H{"total": total, "page": page, "page_size": pageSize, "list": items})
}

// RevokeRedeemCode 作废单张未使用卡密
func RevokeRedeemCode(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		auth.Fail(c, http.StatusBadRequest, "无效的卡密 ID")
		return
	}
	res := db.DB.Model(&models.RedemptionCode{}).Where("id = ? AND status = 1", id).Update("status", 0)
	if res.Error != nil {
		auth.Fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}
	if res.RowsAffected == 0 {
		auth.Fail(c, http.StatusBadRequest, "卡密不存在或已被使用/作废")
		return
	}
	auth.Ok(c, gin.H{"message": "已作废"})
}

// RevokeRedeemCodes 批量作废选中的未使用卡密
type revokeRedeemCodesRequest struct {
	IDs []uint `json:"ids" binding:"required"`
}

func RevokeRedeemCodes(c *gin.Context) {
	var req revokeRedeemCodesRequest
	if err := c.ShouldBindJSON(&req); err != nil || len(req.IDs) == 0 {
		auth.Fail(c, http.StatusBadRequest, "参数错误: 需要 ids")
		return
	}
	if len(req.IDs) > 500 {
		auth.Fail(c, http.StatusBadRequest, "单次最多作废 500 张")
		return
	}
	res := db.DB.Model(&models.RedemptionCode{}).Where("id IN ? AND status = 1", req.IDs).Update("status", 0)
	if res.Error != nil {
		auth.Fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}
	auth.Ok(c, gin.H{"message": fmt.Sprintf("已作废 %d 张未使用卡密", res.RowsAffected)})
}

// RevokeRedeemCodeBatch 整批作废（仅未使用的）
func RevokeRedeemCodeBatch(c *gin.Context) {
	batchNo := c.Param("batch_no")
	if batchNo == "" {
		auth.Fail(c, http.StatusBadRequest, "缺少批次号")
		return
	}
	res := db.DB.Model(&models.RedemptionCode{}).Where("batch_no = ? AND status = 1", batchNo).Update("status", 0)
	if res.Error != nil {
		auth.Fail(c, http.StatusInternalServerError, "数据库错误")
		return
	}
	auth.Ok(c, gin.H{"message": fmt.Sprintf("已作废 %d 张未使用卡密", res.RowsAffected)})
}
