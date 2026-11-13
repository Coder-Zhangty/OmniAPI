package middleware

import (
	"errors"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"net/http"
	"strings"
)

func riskV2Reject(c *gin.Context) {
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "code": "risk_review_required", "message": "当前账号或来源处于安全保护，请登录后查看本人复核通知。", "review_url": "/brand/risk-review.html", "error": gin.H{"type": "permission_error", "code": "risk_review_required", "message": "安全保护中，请登录查看复核通知"}})
}
func OmniRiskV2BusinessAllowed(c *gin.Context, uid int) bool {
	if !model.OmniRiskV2Ready() {
		return true
	}
	path := c.Request.URL.Path
	if strings.HasPrefix(path, "/api/omni/risk/") || strings.Contains(path, "/logout") {
		return true
	}
	if e := model.OmniRiskAdmission(model.DB, uid); e != nil {
		riskV2Failure(c, e)
		return false
	}
	// Automatic source restrictions only gate consumption. Passing true on
	// ordinary account pages still enforces permanent manual business blocks.
	consumption := strings.HasPrefix(path, "/v1/") || strings.HasPrefix(path, "/v1beta/") || strings.HasPrefix(path, "/v2/") || strings.HasPrefix(path, "/mj/") || strings.HasPrefix(path, "/suno/")
	paid := true
	var e error
	if consumption {
		paid, e = model.OmniRiskPaid(model.DB, uid)
	}
	if e != nil {
		c.AbortWithStatusJSON(503, gin.H{"success": false, "message": "安全校验暂时不可用"})
		return false
	}
	if e = model.OmniRiskIPCheck(model.DB, model.OmniRiskNetworkTag(c.ClientIP()), paid); e != nil {
		riskV2Failure(c, e)
		return false
	}
	return true
}
func OmniRiskV2OriginGate() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.URL.Path == "/api/user/auth/refresh" && model.OmniRiskV2Ready() {
			if e := model.OmniRiskIPCheck(model.DB, model.OmniRiskNetworkTag(c.ClientIP()), true); e != nil {
				riskV2Failure(c, e)
				return
			}
		}
		if c.Request.URL.Path == "/api/user/register" && !OmniRiskNewRegistrationAllowed(c) {
			return
		}
		c.Next()
	}
}
func OmniRiskV2StreamTrack() gin.HandlerFunc {
	return func(c *gin.Context) { done := service.OmniRiskTrackRequest(c, c.GetInt("id")); defer done(); c.Next() }
}
func OmniRiskNewRegistrationAllowed(c *gin.Context) bool {
	if !model.OmniRiskV2Ready() {
		return true
	}
	if e := model.OmniRiskIPCheck(model.DB, model.OmniRiskNetworkTag(c.ClientIP()), false); e != nil {
		riskV2Failure(c, e)
		return false
	}
	return true
}

func OmniRiskKnownTokenAllowed(c *gin.Context, uid int) bool {
	if !model.OmniRiskV2Ready() {
		return true
	}
	e := model.OmniRiskAdmission(model.DB, uid)
	if e == nil {
		return true
	}
	if errors.Is(e, model.ErrOmniRiskHold) {
		riskV2Reject(c)
	} else {
		c.AbortWithStatusJSON(503, gin.H{"success": false, "code": "risk_check_unavailable", "message": "安全校验暂时不可用"})
	}
	return false
}

func riskV2Failure(c *gin.Context, e error) {
	if errors.Is(e, model.ErrOmniRiskHold) {
		riskV2Reject(c)
		return
	}
	c.AbortWithStatusJSON(503, gin.H{"success": false, "code": "risk_check_unavailable", "message": "安全校验暂时不可用，请稍后再试"})
}
