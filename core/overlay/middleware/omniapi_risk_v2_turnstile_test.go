package middleware

import (
	"context"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type riskV2ChallengeTransport func(*http.Request) (*http.Response, error)

func (f riskV2ChallengeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestRiskV2TurnstileHostnameActionAndOneAttempt(t *testing.T) {
	oldDB, oldClient, oldEnabled, oldSecret := model.DB, omniTurnstileClient, common.TurnstileCheckEnabled, common.TurnstileSecretKey
	db, e := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, e)
	model.DB = db
	require.NoError(t, model.OmniRiskMigrate(db))
	require.NoError(t, db.Model(&model.OmniRiskPolicyV2{}).Where("id = 1").Updates(map[string]any{"shadow": false, "auto_hold": true}).Error)
	common.TurnstileCheckEnabled = true
	common.TurnstileSecretKey = "fixture"
	t.Cleanup(func() {
		model.DB = oldDB
		omniTurnstileClient = oldClient
		common.TurnstileCheckEnabled = oldEnabled
		common.TurnstileSecretKey = oldSecret
		sql, _ := db.DB()
		sql.Close()
	})
	for _, tc := range []struct {
		name, body, reason string
		timeout, allowed   bool
	}{
		{name: "valid", body: `{"success":true,"hostname":"omniapi.biz","action":"omni_auth"}`, allowed: true},
		{name: "wrong-host", body: `{"success":true,"hostname":"evil.invalid","action":"omni_auth"}`, reason: "turnstile_hostname_mismatch"},
		{name: "wrong-action", body: `{"success":true,"hostname":"omniapi.biz","action":"other"}`, reason: "turnstile_action_mismatch"},
		{name: "missing-action", body: `{"success":true,"hostname":"omniapi.biz"}`, reason: "turnstile_action_mismatch"},
		{name: "used-token", body: `{"success":false,"error-codes":["timeout-or-duplicate"]}`, reason: "turnstile_timeout_or_duplicate"},
		{name: "timeout", timeout: true, reason: "turnstile_timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			omniTurnstileClient = &http.Client{Transport: riskV2ChallengeTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if tc.timeout {
					return nil, context.DeadlineExceeded
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest("POST", "https://omniapi.biz/api/user/register?turnstile=fixture", nil)
			TurnstileCheck()(c)
			require.Equal(t, 1, calls)
			require.Equal(t, !tc.allowed, c.IsAborted())
			require.Equal(t, tc.reason, c.GetString("risk_auth_turnstile_error"))
			if !tc.allowed { require.Contains(t, recorder.Body.String(), `"code":"`+tc.reason+`"`) }
		})
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "https://omniapi.biz/api/user/checkin", nil)
	TurnstileCheck()(c)
	require.True(t, c.IsAborted())
	require.Equal(t, "turnstile_missing_or_invalid", c.GetString("risk_auth_turnstile_error"))
}
