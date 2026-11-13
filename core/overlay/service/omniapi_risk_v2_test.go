package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func riskV2TextInfo(t *testing.T) *relaycommon.RelayInfo {
	var req dto.GeneralOpenAIRequest
	require.NoError(t, json.Unmarshal([]byte(`{"model":"fixture","messages":[{"role":"user","content":"fixture"}],"max_tokens":100}`), &req))
	return &relaycommon.RelayInfo{OriginModelName: "fixture", Request: &req, ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 14, UpstreamModelName: "fixture"}, OmniPriceQuote: []byte(`{"costs":{"14":{"complete_prices_verified":true,"input_usd_per_million":"1","output_usd_per_million":"2","cache_read_usd_per_million":"1","cache_write_usd_per_million":"1","cache_write_1h_usd_per_million":"1"}}}`)}
}

func TestRiskV2OneDetectorFailureDoesNotStallWatermark(t *testing.T) {
	db := riskServiceDB(t)
	oldBurst, oldConsumer := riskV2BurstCheck, riskV2ConsumerCheck
	t.Cleanup(func() {
		riskV2BurstCheck = oldBurst
		riskV2ConsumerCheck = oldConsumer
		riskV2CursorLoaded = false
		riskV2EventCursor = 0
	})
	failed := true
	otherProcessed := false
	riskV2BurstCheck = func(uid int) error {
		if uid == 1 && failed {
			return errors.New("simulated subject overflow")
		}
		if uid == 2 {
			otherProcessed = true
		}
		return nil
	}
	riskV2ConsumerCheck = func(int) error { return nil }
	riskV2CursorLoaded = false
	riskV2EventCursor = 0
	riskV2LastSlow = 0
	riskV2LastReconcile = time.Now().Unix()
	riskV2LastCache = time.Now().Unix()
	require.NoError(t, db.Create(&model.OmniRiskEventV2{Key: "failed-view", UserID: 1, Kind: "token.view", Network: "a", Browser: "a", CreatedAt: time.Now().Unix()}).Error)
	require.NoError(t, db.Create(&model.OmniRiskEventV2{Key: "good-view", UserID: 2, Kind: "token.view", Network: "b", Browser: "b", CreatedAt: time.Now().Unix()}).Error)
	require.NoError(t, riskV2Maintenance())
	require.True(t, otherProcessed)
	require.Equal(t, uint64(2), riskV2EventCursor)
	var jobs []model.OmniRiskRetryV2
	require.NoError(t, db.Find(&jobs).Error)
	require.Len(t, jobs, 1)
	require.Equal(t, 1, jobs[0].UserID)
	failed = false
	require.NoError(t, db.Model(&model.OmniRiskRetryV2{}).Where("key = ?", jobs[0].Key).Update("next_at", 0).Error)
	riskV2CursorLoaded = false
	require.NoError(t, riskV2Maintenance())
	var n int64
	require.NoError(t, db.Model(&model.OmniRiskRetryV2{}).Count(&n).Error)
	require.Zero(t, n)
	require.Equal(t, uint64(2), riskV2EventCursor)
}
func TestRiskV2FinalProviderPayloadBound(t *testing.T) {
	info := riskV2TextInfo(t)
	good := `{"model":"fixture","contents":[{"parts":[{"text":"fixture"}]}],"generationConfig":{"maxOutputTokens":1000,"candidateCount":1}}`
	a, _, e := OmniRiskUpperBound(info, []byte(good), "7")
	require.NoError(t, e)
	var x map[string]any
	require.NoError(t, json.Unmarshal([]byte(good), &x))
	x["generationConfig"].(map[string]any)["candidateCount"] = float64(4)
	b, _ := json.Marshal(x)
	upper, _, e := OmniRiskUpperBound(info, b, "7")
	require.NoError(t, e)
	require.GreaterOrEqual(t, upper-a, int64(42000))
	for _, bad := range []string{
		`{"model":"fixture","messages":[{"content":"fixture"}]}`,
		`{"model":"other","max_tokens":100}`,
		`{"max_tokens":100,"contents":[{"parts":[{"inlineData":{"mimeType":"image/png","data":"AA"}}]}]}`,
		`{"max_tokens":100,"generationConfig":{"responseModalities":["IMAGE"]}}`,
		`{"max_tokens":100,"tools":[{"googleSearch":{}}]}`,
		`{"max_tokens":100,"generationConfig":{"candidateCount":1.5}}`,
		`{"max_tokens":100,"previous_response_id":"unknown"}`,
	} {
		_, _, e = OmniRiskUpperBound(info, []byte(bad), "7")
		require.ErrorIs(t, e, model.ErrOmniRiskQuote, bad)
	}
	_, _, e = OmniRiskUpperBound(info, []byte(good), "0")
	require.ErrorIs(t, e, model.ErrOmniRiskQuote)
}

func TestRiskV2OversizedNotificationDoesNotPoisonOutbox(t *testing.T) {
	db := riskServiceDB(t)
	old := omniRiskWecomDo
	t.Cleanup(func() { omniRiskWecomDo = old })
	sent := 0
	omniRiskWecomDo = func(req *http.Request) (*http.Response, error) {
		data, e := io.ReadAll(req.Body)
		require.NoError(t, e)
		require.True(t, utf8.Valid(data))
		var message map[string]any
		require.NoError(t, json.Unmarshal(data, &message))
		sent++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"errcode":0}`))}, nil
	}
	require.NoError(t, model.OmniRiskEnqueue(db, "long", "case-long", "case", strings.Repeat("复核事项", 1000)))
	require.NoError(t, model.OmniRiskEnqueue(db, "short", "case-short", "case", "第二个案件"))
	var secrets model.OmniRiskSecrets
	secrets.Wecom.Webhook = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=fixture"
	OmniRiskDeliver(context.Background(), secrets, "", 0)
	var n int64
	require.NoError(t, db.Model(&model.OmniRiskOutbox{}).Where("id IN ? AND state = ?", []string{"long", "short"}, "delivered").Count(&n).Error)
	require.Equal(t, int64(2), n)
	require.Equal(t, 1, sent)
}

func TestRiskV2RevokedManualIPIsNotPermanentExemption(t *testing.T) {
	db := riskServiceDB(t)
	now := time.Now().Unix()
	riskV2LastReconcile = now
	riskV2LastSlow = 0
	riskV2LastCache = now
	riskV2CursorLoaded = false
	riskV2EventCursor = 0
	require.NoError(t, db.AutoMigrate(&model.Token{}, &model.UserSession{}))
	require.NoError(t, db.Model(&model.OmniRiskPolicyV2{}).Where("id = 1").Updates(map[string]any{"shadow": false, "temporary_ip": true}).Error)
	require.NoError(t, db.Create(&model.OmniRiskIP{Tag: "same-source", Source: "manual", Version: 1, RevokedAt: now - 2}).Error)
	for uid := 1; uid <= 3; uid++ {
		require.NoError(t, db.Create(&model.User{Id: uid, Username: fmt.Sprint("ip-fixture-", uid), AffCode: fmt.Sprint("ip-aff-", uid), Status: 2}).Error)
		require.NoError(t, db.Create(&model.OmniRiskControl{UserID: uid, Version: 1, State: "hold", Reason: "registration_sequence", Network: "same-source", StartedAt: now}).Error)
	}
	require.NoError(t, riskV2Maintenance())
	var ip model.OmniRiskIP
	require.NoError(t, db.First(&ip, "tag = ?", "same-source").Error)
	require.Equal(t, "automatic", ip.Source)
	require.Zero(t, ip.RevokedAt)
	require.Greater(t, ip.ExpiresAt, now)
}

func TestRiskV2RealLegacyTargetAndNoticeNames(t *testing.T) {
	db := riskServiceDB(t)
	now := time.Now().Unix()
	require.NoError(t, db.AutoMigrate(&model.OmniNotice{}))
	require.NoError(t, db.Exec("INSERT INTO audit_logs(event_id,user_id,action,success,created_at,other) VALUES(?,?,?,?,?,?)", "legacy-owner-action", 0, "user.risk_suspension", true, now, `{"target_user_id":1077,"after_status":2,"owner_authorized":true}`).Error)
	handled, e := riskV2HandledTimes(context.Background())
	require.NoError(t, e)
	require.Equal(t, now, handled[1077])
	require.Zero(t, handled[0])
	require.NoError(t, db.Create(&model.OmniNotice{Key: "announcement:customer:registration-enforcement-20261025:1077", UserID: 1077, Title: "账号封禁通知", Body: "原有本人通知", CreatedAt: now}).Error)
	require.NoError(t, riskV2LegacyNotices())
	var notices []model.OmniRiskNoticeV2
	require.NoError(t, db.Where("user_id = ?", 1077).Find(&notices).Error)
	require.Len(t, notices, 1)
	require.Zero(t, notices[0].DisplayedAt)
	require.Zero(t, notices[0].AcknowledgedAt)
	require.NoError(t, db.Create(&model.OmniRiskCase{ID: "131-false-positive", UserID: 131, Status: "closed", Verdict: "false_positive", ClosedAt: now - 60}).Error)
	evidence, _ := json.Marshal(OmniRiskPack{SubjectID: 131, Evidence: []OmniRiskEvidence{{Kind: "usage_30d", Values: map[string]int64{"last_activity_at": now}}, {Kind: "related_registration_benefit_claims", Values: map[string]int64{"last_at": now - 100}}}})
	fresh, e := riskV2LegacyCaseHasFreshEvidence(model.OmniRiskCase{UserID: 131, Evidence: string(evidence)})
	require.NoError(t, e)
	require.False(t, fresh, "normal new usage cannot revive old rejected benefit evidence")
	evidence, _ = json.Marshal(OmniRiskPack{SubjectID: 131, Evidence: []OmniRiskEvidence{{Kind: "known_initial_registration_credit", Values: map[string]int64{"last_at": now}}}})
	fresh, e = riskV2LegacyCaseHasFreshEvidence(model.OmniRiskCase{UserID: 131, Evidence: string(evidence)})
	require.NoError(t, e)
	require.True(t, fresh, "new independent evidence is not permanently exempt")
}

func TestRiskV2HeldRequestsNeverSkipFinalDispatchGuard(t *testing.T) {
	db := riskServiceDB(t)
	require.NoError(t, db.Create(&model.User{Id: 77, Username: "held-dispatch", AffCode: "held77", Role: 1, Status: 1, CreatedAt: time.Now().Unix() - 172800}).Error)
	require.NoError(t, db.Create(&model.OmniRiskFundingEvidence{UserID: 77, Classification: "paid", SourceHash: "fixture"}).Error)
	require.NoError(t, db.Create(&model.OmniRiskControl{UserID: 77, State: "restored"}).Error)
	for _, state := range []string{"hold", "budget", "confirmed"} {
		require.NoError(t, db.Model(&model.OmniRiskControl{}).Where("user_id = ?", 77).Update("state", state).Error)
		_, err := OmniRiskNeedsBudget(77)
		require.ErrorIs(t, err, model.ErrOmniRiskHold, "existing measures survive disabled new-policy flags")
	}
	require.NoError(t, db.Model(&model.OmniRiskControl{}).Where("user_id = ?", 77).Update("state", "restored").Error)
	secret := filepath.Join(t.TempDir(), "risk.json")
	require.NoError(t, os.WriteFile(secret, []byte(`{"hmac_key":"kkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkk","api_key":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}`), 0600))
	t.Setenv("OMNI_RISK_SECRETS_FILE", secret)
	// Commit a restriction during metadata persistence, after the first guard and
	// budget decision. Deliberately do not run the post-commit cancellation hook.
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("hold_before_dispatch", func(tx *gorm.DB) {
		if tx.Statement.Table == "omni_risk_event_v2" {
			tx.AddError(tx.Session(&gorm.Session{NewDB: true}).Model(&model.OmniRiskControl{}).Where("user_id = ?", 77).Update("state", "hold").Error)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Create().Remove("hold_before_dispatch") })
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "https://omniapi.biz/v1/chat/completions", strings.NewReader(`{}`))
	info := riskV2TextInfo(t)
	info.UserId = 77
	info.RequestId = "late-hold-dispatch"
	req, err := http.NewRequest("POST", "https://upstream.invalid", strings.NewReader(`{}`))
	require.NoError(t, err)
	finish, err := OmniRiskBeforeDispatch(c, info, req)
	upstreamCalls := 0
	if err == nil {
		upstreamCalls++
		finish()
	}
	require.Error(t, err)
	require.Contains(t, err.Error(), "risk_review_required")
	require.Zero(t, upstreamCalls)
}
