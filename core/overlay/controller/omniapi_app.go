package controller

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var appIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,80}$`)
var appVerifierPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43,128}$`)

func appRandom() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func appFail(c *gin.Context, status int, code string) {
	c.JSON(status, gin.H{"success": false, "code": code})
}
func appSuccess(c *gin.Context, data any) {
	c.Header("Cache-Control", "no-store")
	c.JSON(200, gin.H{"success": true, "data": data})
}
func OmniAppStart(c *gin.Context) {
	var b struct {
		Challenge string `json:"challenge"`
		Device    string `json:"device"`
		Platform  string `json:"platform"`
	}
	if c.ShouldBindJSON(&b) != nil || !appIDPattern.MatchString(b.Device) || len(b.Challenge) != 43 || (b.Platform != "windows" && b.Platform != "android") {
		appFail(c, 400, "INVALID_REQUEST")
		return
	}
	challenge, err := base64.RawURLEncoding.DecodeString(b.Challenge)
	if err != nil || len(challenge) != 32 {
		appFail(c, 400, "INVALID_CHALLENGE")
		return
	}
	flow := model.OmniAppLogin{ID: appRandom(), Challenge: b.Challenge, Device: b.Device, Platform: b.Platform, End: time.Now().Add(5 * time.Minute).Unix()}
	if model.DB.Create(&flow).Error != nil {
		appFail(c, 503, "LOGIN_UNAVAILABLE")
		return
	}
	appSuccess(c, gin.H{"request": flow.ID, "expires_at": flow.End, "authorize_url": "https://omniapi.biz/brand/app-login.html?request=" + flow.ID})
}
func OmniAppApprove(c *gin.Context) {
	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok {
		appFail(c, 403, "BROWSER_SESSION_REQUIRED")
		return
	}
	var b struct {
		Request string `json:"request"`
	}
	if c.ShouldBindJSON(&b) != nil || !appIDPattern.MatchString(b.Request) {
		appFail(c, 400, "INVALID_REQUEST")
		return
	}
	result := model.DB.Model(&model.OmniAppLogin{}).Where("id = ? AND end > ? AND user_id = ? AND consumed = ?", b.Request, time.Now().Unix(), 0, false).Updates(map[string]any{"user_id": identity.UserID, "session_id": identity.SessionID, "user_auth_version": identity.UserAuthVersion, "session_version": identity.SessionVersion})
	if result.Error != nil || result.RowsAffected != 1 {
		appFail(c, 409, "LOGIN_REQUEST_EXPIRED_OR_APPROVED")
		return
	}
	appSuccess(c, gin.H{"approved": true})
}
func OmniAppExchange(c *gin.Context)    { omniAppExchange(c, false) }
func OmniAppWebExchange(c *gin.Context) { omniAppExchange(c, true) }
func omniAppExchange(c *gin.Context, web bool) {
	var b struct {
		Request  string `json:"request"`
		Verifier string `json:"verifier"`
	}
	if c.ShouldBindJSON(&b) != nil || !appIDPattern.MatchString(b.Request) || !appVerifierPattern.MatchString(b.Verifier) {
		appFail(c, 400, "INVALID_REQUEST")
		return
	}
	var flow model.OmniAppLogin
	if model.DB.First(&flow, "id = ? AND end > ? AND consumed = ?", b.Request, time.Now().Unix(), false).Error != nil {
		appFail(c, 410, "LOGIN_REQUEST_EXPIRED")
		return
	}
	sum := sha256.Sum256([]byte(b.Verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	if subtle.ConstantTimeCompare([]byte(challenge), []byte(flow.Challenge)) != 1 {
		appFail(c, 403, "INVALID_VERIFIER")
		return
	}
	if flow.UserID == 0 {
		appFail(c, 428, "AUTHORIZATION_PENDING")
		return
	}
	identity := service.AuthIdentity{UserID: flow.UserID, SessionID: flow.SessionID, UserAuthVersion: flow.UserAuthVersion, SessionVersion: flow.SessionVersion}
	if _, _, err := service.ValidateLoginSession(identity); err != nil {
		appFail(c, 401, "AUTHORIZATION_REVOKED")
		return
	}
	consumed := model.DB.Model(&model.OmniAppLogin{}).Where("id = ? AND consumed = ?", flow.ID, false).Update("consumed", true)
	if consumed.Error != nil || consumed.RowsAffected != 1 {
		appFail(c, 409, "LOGIN_REQUEST_CONSUMED")
		return
	}
	method := "native:" + flow.Platform
	if web {
		method = "native:webview"
	}
	bundle, err := service.CreateLoginSessionAtAuthVersion(flow.UserID, flow.UserAuthVersion, method, c.ClientIP(), c.Request.UserAgent())
	if err != nil {
		appFail(c, 503, "LOGIN_UNAVAILABLE")
		return
	}
	if web {
		if model.DB.Create(&model.OmniAppChildSession{SID: bundle.Session.SID, ParentSID: flow.SessionID, UserID: flow.UserID}).Error != nil {
			_, _ = model.RevokeUserSession(flow.UserID, bundle.Session.SID, "native_link_failed")
			appFail(c, 503, "LOGIN_UNAVAILABLE")
			return
		}
		service.WriteRefreshCookie(c, bundle.RefreshToken)
		appSuccess(c, gin.H{"ready": true})
		return
	}
	appSuccess(c, gin.H{"auth": bundle, "refresh_token": bundle.RefreshToken, "device": flow.Device, "user_id": flow.UserID})
}
func OmniAppRefresh(c *gin.Context) {
	var b struct {
		Refresh string `json:"refresh_token"`
		SID     string `json:"sid"`
	}
	if c.ShouldBindJSON(&b) != nil || len(b.Refresh) > 512 || b.SID == "" {
		appFail(c, 400, "INVALID_REQUEST")
		return
	}
	bundle, user, err := service.RefreshLoginSession(b.Refresh, b.SID, c.ClientIP(), c.Request.UserAgent())
	if err != nil {
		appFail(c, 401, "SESSION_EXPIRED")
		return
	}
	appSuccess(c, gin.H{"auth": bundle, "refresh_token": bundle.RefreshToken, "user_id": user.Id})
}
func OmniAppLogout(c *gin.Context) {
	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok {
		appFail(c, 403, "SESSION_REQUIRED")
		return
	}
	_, err := model.RevokeUserSession(identity.UserID, identity.SessionID, "native_logout")
	if err != nil {
		appFail(c, 503, "LOGOUT_UNAVAILABLE")
		return
	}
	appSuccess(c, gin.H{"logged_out": true})
}

type omniSyncClaims struct {
	UID    int    `json:"uid"`
	Device string `json:"device"`
	End    int64  `json:"end"`
	Quota  int64  `json:"quota"`
	SID    string `json:"sid"`
	UV     int64  `json:"uv"`
	SV     int64  `json:"sv"`
	jwt.RegisteredClaims
}

func appSyncKey() (ed25519.PrivateKey, error) {
	seed, err := base64.StdEncoding.DecodeString(os.Getenv("OMNI_SYNC_SIGNING_SEED"))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("sync signing key unavailable")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}
func appSyncSnapshot(uid int, device string, identity service.AuthIdentity) (omniSyncClaims, error) {
	s, err := model.OmniSyncEntitlement(uid)
	if err != nil {
		return omniSyncClaims{}, err
	}
	now := time.Now()
	return omniSyncClaims{UID: uid, Device: device, End: s.End, Quota: s.QuotaBytes, SID: identity.SessionID, UV: identity.UserAuthVersion, SV: identity.SessionVersion, RegisteredClaims: jwt.RegisteredClaims{Issuer: "https://omniapi.biz", Subject: strconv.Itoa(uid), Audience: jwt.ClaimStrings{"omniapi-user-sync"}, ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute)), IssuedAt: jwt.NewNumericDate(now)}}, nil
}
func OmniAppSyncToken(c *gin.Context) {
	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok {
		appFail(c, 403, "SESSION_REQUIRED")
		return
	}
	var b struct {
		Device string `json:"device"`
	}
	if c.ShouldBindJSON(&b) != nil || !appIDPattern.MatchString(b.Device) {
		appFail(c, 400, "INVALID_DEVICE")
		return
	}
	key, err := appSyncKey()
	if err != nil {
		appFail(c, 503, "SYNC_UNAVAILABLE")
		return
	}
	claims, err := appSyncSnapshot(identity.UserID, b.Device, identity)
	if err != nil {
		appFail(c, 503, "SYNC_UNAVAILABLE")
		return
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims).SignedString(key)
	if err != nil {
		appFail(c, 503, "SYNC_UNAVAILABLE")
		return
	}
	appSuccess(c, gin.H{"token": token, "expires_at": claims.ExpiresAt.Unix(), "url": "https://backup.omniapi.biz/api/sync"})
}
func OmniAppSyncIntrospect(c *gin.Context) {
	secret := os.Getenv("OMNI_SYNC_INTROSPECTION_SECRET")
	if len(secret) < 32 || subtle.ConstantTimeCompare([]byte(secret), []byte(c.GetHeader("X-Omni-Sync-Service"))) != 1 {
		appFail(c, 401, "UNAUTHORIZED")
		return
	}
	key, err := appSyncKey()
	if err != nil {
		appFail(c, 503, "SYNC_UNAVAILABLE")
		return
	}
	var b struct {
		Token  string `json:"token"`
		UserID int    `json:"user_id"`
	}
	if c.ShouldBindJSON(&b) != nil {
		appFail(c, 400, "INVALID_REQUEST")
		return
	}
	if b.UserID > 0 && b.Token == "" {
		s, err := model.OmniSyncEntitlement(b.UserID)
		if err != nil {
			appFail(c, 503, "SYNC_UNAVAILABLE")
			return
		}
		appSuccess(c, gin.H{"end": s.End})
		return
	}
	claims := &omniSyncClaims{}
	parsed, err := jwt.ParseWithClaims(b.Token, claims, func(t *jwt.Token) (any, error) { return key.Public(), nil }, jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithIssuer("https://omniapi.biz"), jwt.WithAudience("omniapi-user-sync"), jwt.WithExpirationRequired())
	if err != nil || !parsed.Valid || claims.UID <= 0 || !appIDPattern.MatchString(claims.Device) || claims.Subject != strconv.Itoa(claims.UID) {
		appFail(c, 401, "UNAUTHORIZED")
		return
	}
	identity := service.AuthIdentity{UserID: claims.UID, SessionID: claims.SID, UserAuthVersion: claims.UV, SessionVersion: claims.SV}
	if _, _, err := service.ValidateLoginSession(identity); err != nil {
		appFail(c, 401, "SESSION_REVOKED")
		return
	}
	latest, err := appSyncSnapshot(claims.UID, claims.Device, identity)
	if err != nil {
		appFail(c, 503, "SYNC_UNAVAILABLE")
		return
	}
	appSuccess(c, latest)
}
func OmniAppSyncSelf(c *gin.Context) {
	s, err := model.OmniSyncEntitlement(c.GetInt("id"))
	omniReply(c, gin.H{"subscription": s, "policy": model.OmniSyncConfig()}, err)
}
func OmniAppSyncPurchase(c *gin.Context) {
	var b struct {
		Key string `json:"key"`
	}
	if c.ShouldBindJSON(&b) != nil {
		appFail(c, 400, "INVALID_REQUEST")
		return
	}
	s, err := model.OmniSyncPurchase(c.GetInt("id"), b.Key)
	omniReply(c, s, err)
}
func OmniAppSyncPolicy(c *gin.Context) {
	if c.Request.Method == http.MethodGet {
		appSuccess(c, model.OmniSyncConfig())
		return
	}
	var p model.OmniSyncPolicy
	if c.ShouldBindJSON(&p) != nil || p.MonthlyCents < 0 || p.MonthlyCents > 100000000 || p.QuotaBytes != 1073741824 || (p.Enabled && p.MonthlyCents <= 0) {
		appFail(c, 400, "INVALID_POLICY")
		return
	}
	raw, err := common.Marshal(p)
	if err != nil {
		appFail(c, 400, "INVALID_POLICY")
		return
	}
	if err := model.UpdateOption("OmniAPISync", strings.TrimSpace(string(raw))); err != nil {
		appFail(c, 503, "POLICY_UNAVAILABLE")
		return
	}
	appSuccess(c, p)
}
