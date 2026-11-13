package service

import (
	"bytes"
	"errors"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
	"net/http"
	"os"
	"time"
)

type NativeWebIdentity struct {
	UID       int    `json:"uid"`
	NativeSID string `json:"native_sid"`
	ParentSID string `json:"parent_sid"`
	UV        int64  `json:"uv"`
	SV        int64  `json:"sv"`
}

var nativeWebClient = &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func NativeWebIdentityRequest(action string, body any) (*NativeWebIdentity, error) {
	secret := os.Getenv("OMNI_WEB_BRIDGE_SECRET")
	if len(secret) < 32 {
		return nil, ErrLoginSessionInvalid
	}
	raw, err := common.Marshal(body)
	if err != nil {
		return nil, err
	}
	// Fixed loopback service, not a client-supplied URL. Only this bridge secret
	// travels here; browser/native access and refresh tokens never do.
	req, err := http.NewRequest("POST", "http://host.docker.internal:8791/api/app/web-session/"+action, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Omni-Web-Service", secret)
	res, err := nativeWebClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var result struct {
		Success bool              `json:"success"`
		Data    NativeWebIdentity `json:"data"`
	}
	if res.StatusCode != 200 || common.DecodeJson(http.MaxBytesReader(nil, res.Body, 4096), &result) != nil || !result.Success || result.Data.UID <= 0 || result.Data.NativeSID == "" || result.Data.ParentSID == "" || result.Data.UV <= 0 || result.Data.SV <= 0 {
		return nil, ErrLoginSessionRevoked
	}
	return &result.Data, nil
}

func ValidateNativeWebSession(session *model.UserSession) error {
	if session.LoginMethod != "native:webview:bridge" {
		return nil
	}
	var link model.OmniNativeWebSession
	if err := model.DB.First(&link, "sid = ?", session.SID).Error; err != nil {
		return ErrLoginSessionRevoked
	}
	if link.UserID != session.UserID || link.UserAuthVersion != session.UserAuthVersion {
		return ErrLoginSessionRevoked
	}
	parent, err := ValidateSessionReference(link.UserID, link.ParentSID)
	if err != nil || parent.UserAuthVersion != link.UserAuthVersion || parent.SessionVersion != link.ParentVersion {
		return ErrLoginSessionRevoked
	}
	native, err := NativeWebIdentityRequest("parent", map[string]any{"uid": link.UserID, "sid": link.NativeSID})
	if err != nil || native.UID != link.UserID || native.NativeSID != link.NativeSID || native.ParentSID != link.ParentSID || native.UV != link.UserAuthVersion || native.SV != link.ParentVersion {
		return ErrLoginSessionRevoked
	}
	return nil
}

func ValidateNativeWebSessionFromBundle(bundle *AuthBundle) error {
	session, err := model.GetUserSessionCached(bundle.Session.SID)
	if err != nil {
		return err
	}
	return ValidateNativeWebSession(session)
}

// A verified one-time transfer can rotate this device's existing WebView
// session. Opening menus must not create dozens of active login sessions.
func ReissueNativeWebSession(identity *NativeWebIdentity) (*AuthBundle, error) {
	var link model.OmniNativeWebSession
	err := model.DB.Where("native_sid = ? AND parent_sid = ? AND user_id = ? AND user_auth_version = ? AND parent_version = ?", identity.NativeSID, identity.ParentSID, identity.UID, identity.UV, identity.SV).Order("sid DESC").First(&link).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	session, err := model.GetUserSessionCached(link.SID)
	if errors.Is(err, model.ErrUserSessionInactive) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if session.Status != model.UserSessionStatusActive || session.RevokedAt != 0 || session.ExpiresAt <= time.Now().Unix() {
		return nil, nil
	}
	if err := ValidateNativeWebSession(session); err != nil {
		return nil, err
	}
	secret, err := common.GenerateRandomCharsKey(64)
	if err != nil {
		return nil, err
	}
	rotated, err := model.RotateUserSessionRefresh(identity.UID, session.SID, session.RefreshHash, hashRefreshSecret(secret), time.Now().Unix(), RefreshReplayWindow)
	if err != nil {
		return nil, err
	}
	return issueAuthBundle(rotated, session.SID+"."+secret, true)
}
