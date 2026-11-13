package controller

import (
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func xorCheckoutInput(c *gin.Context) (string, bool) {
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("X-Content-Type-Options", "nosniff")
	if origin := c.GetHeader("Origin"); origin != "" && origin != "https://omniapi.biz" {
		c.JSON(403, gin.H{"success": false, "message": "付款请求来源无效"})
		return "", false
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var body struct {
		Grant string `json:"grant"`
	}
	if c.ShouldBindJSON(&body) != nil || body.Grant == "" {
		c.JSON(400, gin.H{"success": false, "message": "付款链接无效"})
		return "", false
	}
	return body.Grant, true
}

// A payment grant deliberately exposes no user ID, balance, private fields or
// account token. The original owner's authenticated endpoints remain separate.
func xorCheckoutView(o model.OmniTopup) gin.H {
	return gin.H{"id": o.ID, "method": o.Method, "cents": o.Cents, "usd": o.USD, "quota": o.Quota, "state": o.State, "expires_at": o.ExpiresAt, "created_at": o.CreatedAt, "credited_at": o.CreditedAt, "bonus": o.Bonus, "fx_rate": o.FxRate, "fx_source": o.FxSource, "fx_as_of": o.FxAsOf, "error_code": o.ErrorCode, "payment_flow": o.PaymentFlow, "server_time": time.Now().Unix()}
}
func OmniXorCheckoutLaunch(c *gin.Context) {
	grant, ok := xorCheckoutInput(c)
	if !ok {
		return
	}
	data, err := service.XorCheckoutLaunch(grant)
	omniReply(c, data, err)
}
func OmniXorCheckoutStatus(c *gin.Context) { xorCheckoutStatus(c, false) }
func OmniXorCheckoutCheck(c *gin.Context)  { xorCheckoutStatus(c, true) }
func xorCheckoutStatus(c *gin.Context, check bool) {
	grant, ok := xorCheckoutInput(c)
	if !ok {
		return
	}
	o, err := service.XorCheckoutOrder(grant)
	if err != nil {
		c.JSON(403, gin.H{"success": false, "message": "付款链接无效或已过期，请在原账户查看订单"})
		return
	}
	if check && o.AttemptedAt > 0 && o.State != "credited" {
		o, err = service.XorCheck(c.Request.Context(), o.ID)
	}
	omniReply(c, xorCheckoutView(o), err)
}
