package controller

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/boombuler/barcode"
	"github.com/boombuler/barcode/qr"
	"github.com/gin-gonic/gin"
)

func topupView(o model.OmniTopup) any {
	var receipt model.OmniLedger
	if model.DB.Where("key = ?", "xorpay:"+o.AID+":"+o.ID).Limit(1).Find(&receipt).Error == nil && receipt.Key != "" {
		o.Bonus = receipt.Bonus
		o.PaidAt = receipt.PaidAt
		o.PaidAtSource = receipt.PaidAtSource
		o.CampaignID = receipt.CampaignID
		o.CampaignBonus = receipt.CampaignBonus
		o.PromotionState = receipt.PromotionState
		if receipt.Refunded {
			o.State = "refunded"
		}
	}
	paymentURL := ""
	if o.PaymentFlow == service.XorCashierFlow {
		paymentURL = service.XorCheckoutURL(o)
		o.QR = paymentURL
	}
	imageURL := ""
	if o.State != "credited" {
		imageURL = topupQR(o.QR)
	}
	return struct {
		model.OmniTopup
		QRImage    string `json:"qr_image,omitempty"`
		PaymentURL string `json:"payment_url,omitempty"`
	}{o, imageURL, paymentURL}
}
func topupQR(value string) string {
	imageURL := ""
	if value != "" {
		if code, err := qr.Encode(value, qr.M, qr.Auto); err == nil {
			if scaled, err := barcode.Scale(code, 256, 256); err == nil {
				canvas := image.NewRGBA(image.Rect(0, 0, 300, 300))
				draw.Draw(canvas, canvas.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
				draw.Draw(canvas, image.Rect(22, 22, 278, 278), scaled, image.Point{}, draw.Src)
				var out bytes.Buffer
				if png.Encode(&out, canvas) == nil {
					imageURL = "data:image/png;base64," + base64.StdEncoding.EncodeToString(out.Bytes())
				}
			}
		}
	}
	return imageURL
}
func OmniTopupSettings(c *gin.Context) {
	cfg, err := service.XorPayConfig()
	c.Header("Cache-Control", "no-store")
	methods := []string{}
	if err == nil {
		methods = cfg.Methods(c.GetInt("id"))
	}
	omniReply(c, gin.H{"enabled": len(methods) > 0, "min_cents": 100, "max_cents": 1000000, "methods": methods, "checkout_flow": service.XorCashierFlow, "promotion": model.GetOmniPolicy().TopupPromotion, "server_time": time.Now().Unix()}, nil)
}
func OmniTopupCreate(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var b struct {
		Method    string `json:"method"`
		Amount    string `json:"amount"`
		RequestID string `json:"request_id"`
	}
	if c.ShouldBindJSON(&b) != nil {
		c.JSON(400, gin.H{"success": false, "message": "支付参数无效"})
		return
	}
	o, err := service.XorCreate(c.Request.Context(), c.GetInt("id"), b.Method, b.Amount, b.RequestID)
	omniReply(c, topupView(o), err)
}
func OmniTopupGet(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	o, err := model.OmniTopupForUser(c.Param("id"), c.GetInt("id"))
	if err != nil {
		c.JSON(404, gin.H{"success": false, "message": "充值订单不存在"})
		return
	}
	omniReply(c, topupView(o), nil)
}
func OmniTopupCheck(c *gin.Context) {
	if _, err := model.OmniTopupForUser(c.Param("id"), c.GetInt("id")); err != nil {
		c.JSON(404, gin.H{"success": false, "message": "充值订单不存在"})
		return
	}
	o, err := service.XorCheck(c.Request.Context(), c.Param("id"))
	omniReply(c, topupView(o), err)
}
func OmniTopupList(c *gin.Context) {
	var rows []model.OmniTopup
	query := model.DB.Order("created_at DESC").Limit(100)
	if c.FullPath() != "/api/omni/admin/topups" {
		query = query.Where("user_id = ?", c.GetInt("id"))
	}
	c.Header("Cache-Control", "no-store")
	err := query.Find(&rows).Error
	omniReply(c, rows, err)
}
func OmniXorPayNotify(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16384)
	if c.Request.ParseForm() != nil {
		c.String(400, "invalid")
		return
	}
	if _, err := service.XorNotify(c.Request.PostForm); err != nil {
		c.String(503, "unconfirmed")
		return
	}
	c.String(200, "success")
}

// Payment-time evidence is deliberately visible to operators for follow-up.
func OmniPromotionPending(c *gin.Context) {
	var rows []model.OmniLedger
	err := model.DB.Where("kind = ? AND refunded = ? AND promotion_state = ?", "topup", false, "pending_payment_time").Order("created_at DESC").Limit(500).Find(&rows).Error
	c.Header("Cache-Control", "no-store")
	omniReply(c, rows, err)
}
