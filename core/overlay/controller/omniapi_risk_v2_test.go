package controller

import (
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRiskV2PrivateIdentityPurposeAndIsolation(t *testing.T) {
	a, _ := appFixture(t)
	require.NoError(t, model.DB.AutoMigrate(&model.AuthFlow{}))
	require.NoError(t, model.OmniRiskV2Migrate(model.DB))
	b := &model.User{Username: "normal-other-account", AffCode: "riskb", Role: 1, Status: 1, AuthVersion: 1, Group: "default"}
	require.NoError(t, model.DB.Create(b).Error)
	session, e := service.CreateLoginSession(b.Id, "password", "127.0.0.1", "fixture")
	require.NoError(t, e)
	require.NoError(t, model.DB.Create(&model.OmniRiskNoticeV2{ID: "private-a", UserID: a.Id, Title: "A", Body: "private-A"}).Error)
	require.NoError(t, model.DB.Create(&model.OmniRiskNoticeV2{ID: "private-b", UserID: b.Id, Title: "B", Body: "private-B"}).Error)
	restricted, e := service.IssueOmniRiskNotice(a.Id, 1)
	require.NoError(t, e)
	_, e = service.ParseAccessToken(restricted.RiskNoticeToken)
	require.Error(t, e, "notice credential cannot become an access JWT")
	require.NoError(t, model.DB.Model(a).Update("status", 2).Error)
	call := func(bearer string, ack bool) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		method, path, body := "GET", "https://omniapi.biz/api/omni/risk/self", ""
		if ack {
			method, path, body = "POST", "https://omniapi.biz/api/omni/risk/notices/private-b/ack", `{"acknowledged":true}`
			c.Params = gin.Params{{Key: "id", Value: "private-b"}}
		}
		c.Request = httptest.NewRequest(method, path, strings.NewReader(body))
		c.Request.AddCookie(&http.Cookie{Name: riskNoticeCookie, Value: restricted.RiskNoticeToken})
		c.Request.Header.Set("Content-Type", "application/json")
		c.Request.Header.Set("Origin", "https://omniapi.biz")
		if bearer != "" {
			c.Request.Header.Set("Authorization", "Bearer "+bearer)
		}
		if ack {
			OmniRiskV2Ack(c)
		} else {
			OmniRiskV2Self(c)
		}
		return rec
	}
	self := call("", false)
	require.Equal(t, 200, self.Code)
	require.Contains(t, self.Body.String(), "private-A")
	require.NotContains(t, self.Body.String(), "private-B")
	require.NotContains(t, self.Body.String(), "quota")
	self = call(session.AccessToken, false)
	require.Equal(t, 200, self.Code)
	require.Contains(t, self.Body.String(), "private-B")
	require.NotContains(t, self.Body.String(), "private-A")
	require.Equal(t, 404, call("", true).Code, "A cannot acknowledge B's notice")
	require.Equal(t, 401, call("invalid-explicit-identity", false).Code)
	require.NoError(t, model.DB.Model(a).Update("auth_version", 2).Error)
	require.Equal(t, 401, call("", false).Code, "authentication version invalidates restricted proof")
}

func TestRiskV2PolicyReductionDoesNotRequireNewActivationReceipt(t *testing.T) {
	user, _ := appFixture(t)
	require.NoError(t, model.OmniRiskV2Migrate(model.DB))
	t.Setenv("OMNI_RISK_ACTIVATION_GATE", "")
	require.NoError(t, model.DB.Model(&model.OmniRiskPolicyV2{}).Where("id = 1").Updates(map[string]any{"shadow": false, "auto_hold": true, "new_user_protection": true}).Error)
	call := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(r)
		c.Set("id", user.Id)
		c.Request = httptest.NewRequest("POST", "https://omniapi.biz/api/omni/admin/risk/v2/policy", strings.NewReader(body))
		c.Request.Header.Set("Origin", "https://omniapi.biz")
		c.Request.Header.Set("Content-Type", "application/json")
		OmniRiskV2PolicyPut(c)
		return r
	}
	require.Contains(t, call(`{"version":1,"shadow":false,"auto_hold":false,"new_user_protection":true,"new_budget_micros":1000000}`).Body.String(), `"success":true`)
	var policy model.OmniRiskPolicyV2
	require.NoError(t, model.DB.First(&policy, 1).Error)
	require.Equal(t, int64(2), policy.Version)
	require.False(t, policy.AutoHold)
	require.True(t, policy.NewUserProtection)
	require.Contains(t, call(`{"version":2,"shadow":false,"auto_hold":true,"new_user_protection":true,"new_budget_micros":1000000}`).Body.String(), "risk_activation_not_verified")
	require.NoError(t, model.DB.First(&policy, 1).Error)
	require.Equal(t, int64(2), policy.Version)
}
