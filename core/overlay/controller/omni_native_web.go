package controller

import (
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"regexp"
)

var nativeWebRequestPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{32}$`)
var nativeWebVerifierPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43,80}$`)

func OmniNativeWebExchange(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	fail := func(status int, code string) { c.JSON(status, gin.H{"success": false, "code": code}) }
	var body struct {
		Request  string `json:"request"`
		Verifier string `json:"verifier"`
	}
	if c.ShouldBindJSON(&body) != nil || !nativeWebRequestPattern.MatchString(body.Request) || !nativeWebVerifierPattern.MatchString(body.Verifier) {
		fail(400, "INVALID_REQUEST")
		return
	}
	identity, err := service.NativeWebIdentityRequest("consume", map[string]string{"request": body.Request, "verifier": body.Verifier})
	if err != nil {
		fail(401, "WEB_TRANSFER_INVALID")
		return
	}
	parent, err := service.ValidateSessionReference(identity.UID, identity.ParentSID)
	if err != nil || parent.UserAuthVersion != identity.UV || parent.SessionVersion != identity.SV {
		fail(401, "WEB_RELOGIN_REQUIRED")
		return
	}
	bundle, err := service.ReissueNativeWebSession(identity)
	if err != nil {
		fail(503, "WEB_SESSION_UNAVAILABLE")
		return
	}
	if bundle == nil {
		bundle, err = service.CreateLoginSessionAtAuthVersion(identity.UID, identity.UV, "native:webview:bridge", c.ClientIP(), c.Request.UserAgent())
		if err != nil {
			fail(503, "WEB_SESSION_UNAVAILABLE")
			return
		}
		link := model.OmniNativeWebSession{SID: bundle.Session.SID, NativeSID: identity.NativeSID, ParentSID: identity.ParentSID, UserID: identity.UID, UserAuthVersion: identity.UV, ParentVersion: identity.SV}
		if model.DB.Create(&link).Error != nil || service.ValidateNativeWebSessionFromBundle(bundle) != nil {
			_, _ = model.RevokeUserSession(identity.UID, bundle.Session.SID, "web_link_failed")
			fail(401, "WEB_SESSION_UNAVAILABLE")
			return
		}
	}
	service.WriteRefreshCookie(c, bundle.RefreshToken)
	c.JSON(200, gin.H{"success": true, "data": gin.H{"ready": true}})
}
