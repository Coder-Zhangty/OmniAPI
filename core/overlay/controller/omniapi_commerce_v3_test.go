package controller

import (
	"encoding/json"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"html"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

func commerceControllerFixture(t *testing.T) (*model.User, string, *model.Token) {
	t.Helper()
	u, _ := appFixture(t)
	oldLog := model.LOG_DB
	model.LOG_DB = model.DB
	t.Cleanup(func() { model.LOG_DB = oldLog })
	require.NoError(t, model.DB.AutoMigrate(&model.Log{}, &model.TopUp{}, &model.Option{}))
	sql, _ := model.DB.DB()
	sql.SetMaxOpenConns(1)
	oldMemory := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() { common.MemoryCacheEnabled = oldMemory; sql.Close() })
	require.NoError(t, model.DB.AutoMigrate(&model.Token{}, &model.UserSubscription{}, &model.SubscriptionPlan{}))
	require.NoError(t, model.OmniMigrate())
	common.OptionMapRWMutex.Lock()
	if common.OptionMap == nil {
		common.OptionMap = map[string]string{}
	}
	keys := []string{"OmniAPICommerceV3", "OmniAPIEmailV3", "OmniAPICommercial", "OmniAPIPricing", "OmniAPIRouting", "OmniAPIRiskV2"}
	old := map[string]string{}
	for _, k := range keys {
		old[k] = common.OptionMap[k]
	}
	common.OptionMap["OmniAPIRiskV2"] = ""
	common.OptionMap["OmniAPIEmailV3"] = `{"enabled":true,"delivery_accepted":true,"sender_dns_accepted":true}`
	common.OptionMap["OmniAPICommercial"] = `{"billing_enabled":true,"model_scope":"all_available"}`
	common.OptionMap["OmniAPIPricing"] = `{"enabled":true,"models":{"gpt-6.1-sol":{"costs":{"7":{"complete_prices_verified":true}}}}}`
	common.OptionMap["OmniAPIRouting"] = `{"models":{"gpt-6.1-sol":[7]}}`
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		for k, v := range old {
			common.OptionMap[k] = v
		}
		common.OptionMapRWMutex.Unlock()
	})
	require.NoError(t, model.DB.Model(u).Update("quota", 100000000).Error)
	now := time.Now().Unix()
	p := model.OmniCommerceProfile{ID: "controller-synthetic", ApprovedBy: 1, ApprovedAt: now - 1, AuditExpiresAt: now + 3600, Evidence: "SYNTHETIC TEST ONLY", Models: `{"gpt-6.1-sol":[7]}`, FXMicros: 7000000, WorstCostPPM: 400000}
	p = commerceLaunchControllerProfile(t, p)
	require.NoError(t, model.OmniCommerceApprove(p))
	cfg, _ := json.Marshal(model.OmniCommerceSwitches{Enabled: true, SalesEnabled: true, ProfileID: p.ID, EmailReady: true, AcceptanceID: "synthetic"})
	common.OptionMapRWMutex.Lock()
	common.OptionMap["OmniAPICommerceV3"] = string(cfg)
	common.OptionMapRWMutex.Unlock()
	require.NoError(t, model.DB.Create(&model.OmniCommerceRelease{ID: "unified-20261104", State: "active", ProfileID: p.ID, AcceptanceID: "synthetic"}).Error)
	v, e := model.OmniSharedMutate(u.Id, "", model.OmniSharedCommand{Action: "create", Name: "真实链路合成池", RequestKey: "integration-create-001", Cents: 9900, ProfileID: p.ID})
	require.NoError(t, e)
	id := v["pool_id"].(string)
	token := &model.Token{UserId: u.Id, Key: "integrationsharedkeywithoutdash", Name: "shared", Status: 1, ExpiredTime: -1, UnlimitedQuota: true, OmniPoolID: id, OmniFallback: "off"}
	require.NoError(t, token.Insert())
	return u, id, token
}
func TestOmniSharedTokenAuthRelayInfoAndBillingIntegration(t *testing.T) {
	u, pool, key := commerceControllerFixture(t)
	var before model.User
	require.NoError(t, model.DB.First(&before, u.Id).Error)
	router := gin.New()
	router.POST("/v1/chat/completions", middleware.TokenAuth(), func(c *gin.Context) {
		require.Equal(t, pool, c.GetString("omni_pool_id"))
		common.SetContextKey(c, constant.ContextKeyOriginalModel, "gpt-6.1-sol")
		request := &dto.GeneralOpenAIRequest{}
		info := relaycommon.GenRelayInfoOpenAI(c, request)
		require.Equal(t, pool, info.OmniPoolID)
		require.Equal(t, key.Id, info.TokenId)
		session, apiErr := service.NewOmniBillingSession(c, info, 1000)
		require.Nil(t, apiErr)
		require.NotNil(t, session)
		require.NoError(t, session.Settle(125))
		c.JSON(200, gin.H{"success": true})
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer sk-"+key.Key)
	router.ServeHTTP(rec, req)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	var holds []model.OmniSharedHold
	require.NoError(t, model.DB.Where("pool_id = ?", pool).Find(&holds).Error)
	require.Len(t, holds, 1)
	require.Equal(t, "settled", holds[0].State)
	var after model.User
	require.NoError(t, model.DB.First(&after, u.Id).Error)
	require.Equal(t, before.Quota, after.Quota, "shared usage must not charge personal wallet")
}
func TestOmniSharedFallbackModesAndPrivateChartsIntegration(t *testing.T) {
	for _, mode := range []string{"off", "subscription_only", "wallet_only", "subscription_then_wallet"} {
		t.Run(mode, func(t *testing.T) {
			u, pool, key := commerceControllerFixture(t)
			require.NoError(t, model.DB.Model(&model.OmniSharedMember{}).Where("pool_id = ? AND user_id = ?", pool, u.Id).Update("budget_micros", 0).Error)
			require.NoError(t, model.DB.Model(key).Update("omni_fallback", mode).Error)
			if mode == "subscription_only" || mode == "subscription_then_wallet" {
				now := time.Now().Unix()
				require.NoError(t, model.DB.Create(&model.OmniSubscription{UserID: u.Id, PlanKey: "standard", Start: now - 60, End: now + 3600, FiveLimit: 500, WeekLimit: 2000, QuotaVersion: 2, BillingVersion: 2}).Error)
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
			info := &relaycommon.RelayInfo{UserId: u.Id, TokenId: key.Id, TokenKey: key.Key, TokenUnlimited: true, OriginModelName: "gpt-6.1-sol", RequestId: "fallback-integration-request", OmniPoolID: pool, IsPlayground: true, Request: &dto.GeneralOpenAIRequest{}}
			session, apiErr := service.NewOmniBillingSession(c, info, 100)
			if mode == "off" {
				require.NotNil(t, apiErr)
				return
			}
			require.Nil(t, apiErr)
			require.NoError(t, session.Settle(25))
			var record model.OmniReservation
			require.NoError(t, model.DB.First(&record, "request_id = ?", info.RequestId).Error)
			if mode == "wallet_only" {
				require.Equal(t, "wallet", record.Source)
			} else {
				require.Equal(t, "omni_subscription", record.Source)
			}
			view, e := model.OmniSharedView(u.Id, pool, "")
			require.NoError(t, e)
			require.EqualValues(t, 0, view["consumed_micros"])
		})
	}
}

func TestOmniCommerceEmailSMTPDeliveryAndSelfSetReset(t *testing.T) {
	u, _, key := commerceControllerFixture(t)
	mailbox := newSecurityMailbox(t)
	email := "local+normal.alias@example.com"
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("GET", "/api/verification?email="+url.QueryEscape(email), nil)
	SendEmailVerification(c)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"success":true`)
	code := mailbox.code(t, email)
	require.NoError(t, model.OmniEmailCheck("register", email, 0, code))
	require.NoError(t, model.DB.Model(u).Updates(map[string]any{"email": email, "verified_email": email, "email_verified_at": time.Now().Unix()}).Error)
	rec = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("GET", "/api/reset_password?email="+url.QueryEscape(email), nil)
	SendPasswordResetEmail(c)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	mailbox.mutex.Lock()
	mails := append([]string{}, mailbox.mail[email]...)
	mailbox.mutex.Unlock()
	var link string
	for _, body := range mails {
		if m := regexp.MustCompile(`href="([^"]+)"`).FindStringSubmatch(body); len(m) == 2 {
			link = html.UnescapeString(m[1])
		}
	}
	parsed, e := url.Parse(link)
	require.NoError(t, e)
	require.Empty(t, parsed.RawQuery)
	fragment, e := url.ParseQuery(parsed.Fragment)
	require.NoError(t, e)
	token := fragment.Get("token")
	require.NotEmpty(t, token)
	body, _ := json.Marshal(map[string]any{"email": email, "token": token, "password": "chosen-new-password-123", "revoke_keys": false})
	rec = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/api/user/reset", strings.NewReader(string(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	ResetPassword(c)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), "chosen-new-password")
	var current model.User
	require.NoError(t, model.DB.First(&current, u.Id).Error)
	require.True(t, common.ValidatePasswordAndHash("chosen-new-password-123", current.Password))
	require.NoError(t, model.DB.First(key, key.Id).Error)
	require.Equal(t, common.TokenStatusEnabled, key.Status)
}

func TestOmniSharedExplicitSubscriptionThenWalletAuthorization(t *testing.T) {
	u, pool, key := commerceControllerFixture(t)
	require.NoError(t, model.DB.Model(&model.OmniSharedMember{}).Where("pool_id = ? AND user_id = ?", pool, u.Id).Update("budget_micros", 0).Error)
	require.NoError(t, model.DB.Model(key).Update("omni_fallback", "subscription_then_wallet").Error)
	_, e := model.OmniSetWalletOverflow(u.Id, false)
	require.NoError(t, e)
	now := time.Now().Unix()
	require.NoError(t, model.DB.Create(&model.OmniSubscription{UserID: u.Id, PlanKey: "standard", Start: now - 60, End: now + 3600, FiveLimit: 500, WeekLimit: 2000, FiveUsed: 500, WeekUsed: 2000, QuotaVersion: 2, BillingVersion: 2}).Error)
	r, e := model.OmniSharedPersonalReserve(u.Id, "explicit-key-wallet-authorization", "gpt-6.1-sol", 100, pool, key.Id)
	require.NoError(t, e)
	require.Equal(t, "wallet", r.Source)
	require.NoError(t, model.OmniAdjust(r.RequestID, 25, true))
}
