package middleware

import (
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"strings"
	"time"
)

// Installed before CAPTCHA/rate limit/body parsing. Never reads request bodies,
// OAuth query strings, credentials, state, codes or claimed usernames.
func OmniRiskAuthAudit() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		registration := path == "/api/user/register"
		login := path == "/api/user/login" || path == "/api/user/login/2fa" || path == "/api/user/auth/login/verify" || strings.HasSuffix(path, "/login/finish")
		callback := strings.HasPrefix(path, "/api/oauth/") && !strings.Contains(path, "/start") && !strings.Contains(path, "/bind") && c.Request.Method == "GET"
		if !registration && !login && !callback {
			c.Next()
			return
		}
		c.Next()
		if c.GetString("risk_auth_intent") == "bind" || c.GetString("risk_auth_intent") == "verify" {
			return
		}
		if model.DB == nil || !model.DB.Migrator().HasTable(&model.OmniRiskAuthEvent{}) {
			return
		}
		if callback && c.GetString("risk_auth_intent") != "login" && (c.FullPath() != "/api/oauth/:provider" && c.FullPath() != "/api/oauth/wechat") {
			return
		}
		if callback && c.GetString("risk_auth_intent") != "login" && c.Writer.Status() < 400 && c.Writer.Status() >= 300 {
			return
		}
		if callback && c.GetBool("risk_auth_completed") {
			return
		} // existing successful login audit is canonical
		if login && c.GetBool("risk_auth_completed") {
			return
		}
		event := model.OmniRiskAuthEvent{CreatedAt: time.Now().Unix(), UserID: c.GetInt("risk_auth_user_id"), InviterID: c.GetInt("risk_auth_inviter_id"), RequestID: c.GetString(common.RequestIdKey), Action: "login.failed", Stage: "rejected_before_completion"}
		if registration {
			event.Action = "registration.failed"
			if c.GetBool("risk_auth_registered") {
				event.Action = "registration.success"
				event.Success = true
				event.Stage = "account_committed"
			}
		}
		if reason := c.GetString("risk_auth_turnstile_error"); reason != "" {
			event.Stage = reason
		}
		event.GrantedQuota = c.GetInt64("risk_auth_granted_quota")
		if event.Action == "registration.success" {
			_ = model.OmniRiskRecordV2(event.UserID, "registration.success", fmt.Sprint(event.UserID), c.ClientIP(), c.Request.UserAgent(), event.GrantedQuota)
		}
		if callback && c.GetString("risk_auth_intent") != "login" {
			event.Action = "oauth.callback.rejected"
			event.Stage = "unverified_callback"
		}
		if c.GetBool("risk_auth_registration_attempt") && !c.GetBool("risk_auth_registration_recorded") {
			event.Action = "registration.failed"
		}
		if c.GetBool("risk_auth_pending") {
			event.Action = "login.pending_verification"
			event.Stage = "pending_verification"
		}
		if secrets, err := model.LoadOmniRiskSecrets(); err == nil {
			ip := model.OmniRiskCanonicalIP(c.ClientIP())
			if ip != "" {
				event.NetworkTag = model.OmniRiskTag(secrets.HMACKey, "network", ip)
			}
			event.BrowserTag = model.OmniRiskTag(secrets.HMACKey, "browser", c.Request.UserAgent())
			event.BrowserFamily = model.OmniRiskBrowser(c.Request.UserAgent())
		}
		if err := model.DB.Create(&event).Error; err != nil {
			common.SysError("Risk authentication audit write failed")
		}
	}
}
