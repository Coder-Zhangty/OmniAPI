package controller

import (
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"time"
)

func recordOmniRiskRegistration(c *gin.Context, user *model.User, inviter int) {
	if user != nil {
		_ = model.OmniRiskRecordV2(user.Id, "registration.success", fmt.Sprint(user.Id), c.ClientIP(), c.Request.UserAgent(), int64(user.Quota))
	}
	if user == nil || user.Id <= 0 || model.DB == nil || !model.DB.Migrator().HasTable(&model.OmniRiskAuthEvent{}) {
		return
	}
	event := model.OmniRiskAuthEvent{UserID: user.Id, InviterID: inviter, GrantedQuota: int64(user.Quota), CreatedAt: time.Now().Unix(), Action: "registration.success", Stage: "account_committed", Success: true, RequestID: c.GetString(common.RequestIdKey)}
	if secrets, err := model.LoadOmniRiskSecrets(); err == nil {
		ip := model.OmniRiskCanonicalIP(c.ClientIP())
		if ip != "" {
			event.NetworkTag = model.OmniRiskTag(secrets.HMACKey, "network", ip)
		}
		event.BrowserTag = model.OmniRiskTag(secrets.HMACKey, "browser", c.Request.UserAgent())
		event.BrowserFamily = model.OmniRiskBrowser(c.Request.UserAgent())
	}
	if err := model.DB.Create(&event).Error; err != nil {
		common.SysError("Risk registration audit write failed")
	} else {
		c.Set("risk_auth_registration_recorded", true)
	}
}
