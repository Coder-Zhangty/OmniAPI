package controller

import (
	"errors"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"strconv"
)

func giftReply(c *gin.Context, data any, e error) {
	c.Header("Cache-Control", "no-store")
	if e != nil {
		status, code, message := 409, "gift_unavailable", "操作未完成，请核对参数或刷新重试。"
		var input *model.OmniGiftInputError
		if errors.As(e, &input) {
			status = 400
			code = "gift_invalid_input"
			message = input.Error()
		}
		if errors.Is(e, model.ErrOmniGiftUnavailable) {
			message = e.Error()
		}
		if errors.Is(e, model.ErrOmniRiskHold) {
			status = 403
			code = "risk_review_required"
			message = "账号处于安全保护，请查看本人复核通知。"
		}
		c.JSON(status, gin.H{"success": false, "code": code, "message": message})
		return
	}
	c.JSON(200, gin.H{"success": true, "data": data})
}
func OmniGiftRedeem(c *gin.Context) {
	if !riskV2Origin(c) {
		return
	}
	var b struct {
		Code string `json:"code"`
	}
	if c.ShouldBindJSON(&b) != nil || len(b.Code) > 80 {
		c.AbortWithStatus(400)
		return
	}
	r, e := model.OmniGiftRedeem(b.Code, c.GetInt("id"), c.GetInt64("auth_version"))
	giftReply(c, r, e)
}
func OmniGiftSelf(c *gin.Context) {
	var rows []model.OmniGiftClaim
	e := model.DB.Where("user_id = ?", c.GetInt("id")).Order("created_at DESC,campaign_id").Limit(100).Find(&rows).Error
	giftReply(c, rows, e)
}
func OmniGiftAdminList(c *gin.Context) {
	var rows []model.OmniGiftCampaign
	before := c.Query("before")
	q := model.DB.Order("created_at DESC,id DESC").Limit(100)
	if before != "" {
		var cursor model.OmniGiftCampaign
		if e := model.DB.Select("id", "created_at").First(&cursor, "id = ?", before).Error; e != nil {
			giftReply(c, nil, &model.OmniGiftInputError{Message: "分页位置已失效，请刷新列表"})
			return
		}
		q = q.Where("created_at < ? OR (created_at = ? AND id < ?)", cursor.CreatedAt, cursor.CreatedAt, cursor.ID)
	}
	e := q.Find(&rows).Error
	next := ""
	if len(rows) == 100 {
		next = rows[99].ID
	}
	giftReply(c, gin.H{"items": rows, "next": next}, e)
}
func OmniGiftAdminCreate(c *gin.Context) {
	if !riskV2Origin(c) {
		return
	}
	var b struct {
		RequestID string `json:"request_id"`
		Title     string `json:"title"`
		Amount    string `json:"amount_usd"`
		Limit     int    `json:"max_claims"`
		Expires   int64  `json:"expires_at"`
	}
	if c.ShouldBindJSON(&b) != nil {
		c.AbortWithStatus(400)
		return
	}
	r, e := model.OmniGiftCreate(c.GetInt("id"), b.RequestID, b.Title, b.Amount, b.Limit, b.Expires)
	giftReply(c, r, e)
}
func OmniGiftAdminUpdate(c *gin.Context) {
	if !riskV2Origin(c) {
		return
	}
	var b struct {
		Version int64 `json:"version"`
		Enabled *bool `json:"enabled"`
	}
	if c.ShouldBindJSON(&b) != nil || b.Version < 1 || b.Enabled == nil {
		c.AbortWithStatus(400)
		return
	}
	e := model.OmniGiftSetEnabled(c.Param("campaign"), b.Version, *b.Enabled, c.GetInt("id"))
	giftReply(c, true, e)
}
func OmniGiftAdminClaims(c *gin.Context) {
	after, e := strconv.Atoi(c.DefaultQuery("after_user", "0"))
	if e != nil || after < 0 {
		c.AbortWithStatus(400)
		return
	}
	var rows []model.OmniGiftClaim
	e = model.DB.Where("campaign_id = ? AND user_id > ?", c.Param("campaign"), after).Order("user_id").Limit(100).Find(&rows).Error
	next := 0
	if len(rows) == 100 {
		next = rows[99].UserID
	}
	giftReply(c, gin.H{"items": rows, "next_user": next}, e)
}
