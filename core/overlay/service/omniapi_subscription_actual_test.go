package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"net/http/httptest"
	"testing"
	"time"
)

func actualAllowanceFixture(t *testing.T) (*gin.Context, *relaycommon.RelayInfo) {
	t.Helper()
	oldDB, oldCache, oldRedis := model.DB, common.MemoryCacheEnabled, common.RedisEnabled
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	model.DB = db
	common.MemoryCacheEnabled = false
	common.RedisEnabled = false
	common.OptionMapRWMutex.Lock()
	if common.OptionMap == nil {
		common.OptionMap = map[string]string{}
	}
	oldPolicy, oldPrices := common.OptionMap["OmniAPICommercial"], common.OptionMap["OmniAPIPricing"]
	common.OptionMap["OmniAPICommercial"] = `{"billing_enabled":true,"model_scope":"all_available"}`
	common.OptionMap["OmniAPIPricing"] = `{"enabled":true,"models":{"test-model":{}}}`
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		model.DB = oldDB
		common.MemoryCacheEnabled = oldCache
		common.RedisEnabled = oldRedis
		common.OptionMapRWMutex.Lock()
		common.OptionMap["OmniAPICommercial"] = oldPolicy
		common.OptionMap["OmniAPIPricing"] = oldPrices
		common.OptionMapRWMutex.Unlock()
		sqlDB.Close()
	})
	if err = db.AutoMigrate(&model.User{}, &model.Token{}, &model.UserSubscription{}, &model.SubscriptionPlan{}); err != nil {
		t.Fatal(err)
	}
	if err = model.OmniMigrate(); err != nil {
		t.Fatal(err)
	}
	if err = db.Create(&model.User{Id: 9916, Username: "actual-allowance", Quota: 2820950}).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Create(&model.OmniAccount{UserID: 9916}).Error; err != nil {
		t.Fatal(err)
	}
	db.Model(&model.OmniAccount{}).Where("user_id = ?", 9916).Update("wallet_overflow", false)
	now := time.Now().Unix()
	if err = db.Create(&model.OmniSubscription{UserID: 9916, PlanKey: "standard", Start: now - 60, End: now + 86400, FxMicros: 6735100, FiveLimit: 307345, WeekLimit: 1228638, WeekUsed: 3708, QuotaVersion: 2, BillingVersion: 2}).Error; err != nil {
		t.Fatal(err)
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	limit := uint(64000)
	info := &relaycommon.RelayInfo{UserId: 9916, RequestId: "pessimistic-actual-small", OriginModelName: "test-model", OmniBillingActive: true, IsPlayground: true, Request: &dto.ClaudeRequest{MaxTokens: &limit}}
	return c, info
}

func TestOmniSubscriptionActualOverageCompletesThenPauses(t *testing.T) {
	c, info := actualAllowanceFixture(t)
	// The original overage stays platform-funded. Later requests follow the
	// committed preference while only the subscription source stays paused.
	_, err := model.OmniSetWalletOverflow(9916, true)
	require.NoError(t, err)
	session, apiErr := NewOmniBillingSession(c, info, 800000)
	require.Nil(t, apiErr)
	require.NoError(t, session.Settle(800000))
	require.NoError(t, session.Settle(800000))
	var s model.OmniSubscription
	require.NoError(t, model.DB.First(&s, "user_id = ?", 9916).Error)
	var r model.OmniReservation
	require.NoError(t, model.DB.First(&r, "request_id = ?", info.RequestId).Error)
	var u model.User
	require.NoError(t, model.DB.First(&u, 9916).Error)
	assert.EqualValues(t, 800000, s.FiveUsed)
	assert.EqualValues(t, 803708, s.WeekUsed)
	assert.True(t, s.FivePaused)
	assert.False(t, s.WeekPaused)
	assert.EqualValues(t, 492655, r.PlatformQuota)
	assert.EqualValues(t, 800000, r.EstimatedQuota)
	assert.Equal(t, "settled", r.State)
	assert.Equal(t, 2820950, u.Quota)
	// The admin log reports the settled source and platform-funded overage.
	other := model.NewLogOther()
	appendBillingInfo(info, other)
	snapshot := other.Snapshot()
	assert.Equal(t, "omni_subscription", snapshot["billing_source"])
	admin, ok := snapshot["admin_info"].(map[string]any)
	require.True(t, ok)
	assert.EqualValues(t, 492655, admin["platform_quota"])
	info.RequestId = "paused-next"
	nextWallet, apiErr := NewOmniBillingSession(c, info, 800000)
	require.Nil(t, apiErr)
	assert.Equal(t, "wallet", nextWallet.funding.Source())
	assert.Equal(t, 0, nextWallet.GetPreConsumedQuota())
	_, err = model.OmniSetWalletOverflow(9916, false)
	require.NoError(t, err)
	require.NoError(t, nextWallet.Settle(100))
	require.NoError(t, nextWallet.Settle(100))
	require.NoError(t, model.DB.First(&u, 9916).Error)
	require.NoError(t, model.DB.First(&r, "request_id = ?", "pessimistic-actual-small").Error)
	require.NoError(t, model.DB.First(&s, "user_id = ?", 9916).Error)
	assert.Equal(t, 2820850, u.Quota)
	assert.EqualValues(t, 492655, r.PlatformQuota)
	assert.True(t, s.FivePaused)
	assert.EqualValues(t, 800000, s.FiveUsed)
	info.RequestId = "paused-disabled"
	_, apiErr = NewOmniBillingSession(c, info, 100)
	require.NotNil(t, apiErr)
	assert.Contains(t, apiErr.Error(), "未转扣钱包")
	assert.NotContains(t, apiErr.Error(), "减少上下文")
	// Simulate crossing the five-hour boundary without changing subscription.
	require.NoError(t, model.DB.Model(&s).Update("five_epoch", -1).Error)
	info.RequestId = "reset-next"
	next, apiErr := NewOmniBillingSession(c, info, 100)
	require.Nil(t, apiErr)
	require.NoError(t, next.Settle(100))
	require.NoError(t, model.DB.First(&s, "user_id = ?", 9916).Error)
	assert.False(t, s.FivePaused)
	assert.EqualValues(t, 100, s.FiveUsed)
	assert.EqualValues(t, 803808, s.WeekUsed)
}

func TestOmniSubscriptionWaitingAdmissionReadsLatestWalletPreference(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled-%t", enabled), func(t *testing.T) {
			c, info := actualAllowanceFixture(t)
			// Start with the opposite preference; the queued request must not freeze it.
			_, err := model.OmniSetWalletOverflow(9916, !enabled)
			require.NoError(t, err)
			first, apiErr := NewOmniBillingSession(c, info, 800000)
			require.Nil(t, apiErr)
			_, err = model.OmniReserveAllowance(9916, "wait-probe", "test-model", 800000)
			require.ErrorIs(t, err, model.ErrOmniAllowancePending)
			secondInfo := *info
			secondInfo.RequestId = "waiting-preference"
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			secondContext, _ := gin.CreateTestContext(httptest.NewRecorder())
			secondContext.Request = httptest.NewRequest("POST", "/v1/messages", nil).WithContext(ctx)
			type result struct {
				session *BillingSession
				failure string
			}
			done := make(chan result, 1)
			go func() {
				session, apiErr := NewOmniBillingSession(secondContext, &secondInfo, 800000)
				failure := ""
				if apiErr != nil {
					failure = apiErr.Error()
				}
				done <- result{session, failure}
			}()
			// No settlement has released the hold, so any completed admission is wrong.
			select {
			case result := <-done:
				require.FailNow(t, "pending request completed", result.failure)
			case <-time.After(60 * time.Millisecond):
			}
			_, err = model.OmniSetWalletOverflow(9916, enabled)
			require.NoError(t, err)
			// Exact exhaustion proves the queued request needs a fresh preference read.
			require.NoError(t, first.Settle(307345))
			var admitted result
			select {
			case admitted = <-done:
			case <-time.After(3 * time.Second):
				cancel()
				<-done
				require.FailNow(t, "queued request did not finish")
			}
			if enabled {
				require.Empty(t, admitted.failure)
				require.NotNil(t, admitted.session)
				assert.Equal(t, "wallet", admitted.session.funding.Source())
				assert.Equal(t, 0, admitted.session.GetPreConsumedQuota())
				require.NoError(t, admitted.session.Settle(100))
				require.NoError(t, admitted.session.Settle(100))
			} else {
				require.Nil(t, admitted.session)
				assert.Contains(t, admitted.failure, "未转扣钱包")
			}
			var user model.User
			require.NoError(t, model.DB.First(&user, 9916).Error)
			expected := 2820950
			if enabled {
				expected -= 100
			}
			assert.Equal(t, expected, user.Quota)
		})
	}
}

func TestOmniSubscriptionActualRefundAndTokenFailureRestoreAllowance(t *testing.T) {
	c, info := actualAllowanceFixture(t)
	info.IsPlayground = false
	info.TokenId = 111
	info.TokenKey = "allowance-token"
	token := model.Token{Id: 111, UserId: 9916, Key: info.TokenKey, RemainQuota: 900000}
	if err := model.DB.Create(&token).Error; err != nil {
		t.Fatal(err)
	}
	session, apiErr := NewOmniBillingSession(c, info, 800000)
	if apiErr != nil {
		t.Fatal(apiErr)
	}
	model.DB.First(&token, 111)
	if token.RemainQuota != 900000-307345 {
		t.Fatal("token reserved theoretical maximum")
	}
	session.Refund(c)
	session.Refund(c)
	deadline := time.Now().Add(3 * time.Second)
	for {
		var s model.OmniSubscription
		model.DB.First(&s, "user_id = ?", 9916)
		model.DB.First(&token, 111)
		if s.FiveUsed == 0 && s.WeekUsed == 3708 && token.RemainQuota == 900000 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("failed request refund left a hold")
		}
		time.Sleep(10 * time.Millisecond)
	}
	model.DB.Model(&token).Update("remain_quota", 1)
	info.RequestId = "token-failure"
	if _, apiErr := NewOmniBillingSession(c, info, 800000); apiErr == nil {
		t.Fatal("limited token was ignored")
	}
	var s model.OmniSubscription
	model.DB.First(&s, "user_id = ?", 9916)
	var r model.OmniReservation
	model.DB.First(&r, "request_id = ?", info.RequestId)
	if s.FiveUsed != 0 || s.WeekUsed != 3708 || r.Actual != 0 || r.State != "refunded" {
		t.Fatal("token failure did not refund subscription")
	}
}

func TestOmniSubscriptionActualConcurrentHoldWaitsAndCancellationDoesNotCharge(t *testing.T) {
	c, info := actualAllowanceFixture(t)
	first, apiErr := NewOmniBillingSession(c, info, 800000)
	if apiErr != nil {
		t.Fatal(apiErr)
	}
	if _, err := model.OmniReserveAllowance(9916, "pending-probe", "test-model", 800000); !errors.Is(err, model.ErrOmniAllowancePending) {
		t.Fatalf("pending hold misclassified: %v", err)
	}
	secondInfo := *info
	secondInfo.RequestId = "concurrent-next"
	secondContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	secondContext.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	done := make(chan *BillingSession, 1)
	fail := make(chan error, 1)
	go func() {
		s, e := NewOmniBillingSession(secondContext, &secondInfo, 800000)
		if e != nil {
			fail <- e
		} else {
			done <- s
		}
	}()
	select {
	case <-done:
		t.Fatal("pending allowance oversubscribed")
	case e := <-fail:
		t.Fatal(e)
	case <-time.After(60 * time.Millisecond):
	}
	if err := first.Settle(24823); err != nil {
		t.Fatal(err)
	}
	select {
	case second := <-done:
		if err := second.Settle(100); err != nil {
			t.Fatal(err)
		}
	case e := <-fail:
		t.Fatal(e)
	case <-time.After(3 * time.Second):
		t.Fatal("queued request did not resume")
	}
	// A cancelled queued request never creates a financial reservation.
	hold, err := model.OmniReserveAllowance(9916, "hold-for-cancel", "test-model", 800000)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.Request = c.Request.WithContext(ctx)
	info.RequestId = "cancelled-next"
	if _, e := NewOmniBillingSession(c, info, 800000); e == nil {
		t.Fatal("cancelled request admitted")
	}
	var count int64
	model.DB.Model(&model.OmniReservation{}).Where("request_id = ?", info.RequestId).Count(&count)
	if count != 0 {
		t.Fatal("cancelled request charged")
	}
	if err = model.OmniAdjust(hold.RequestID, 0, true); err != nil {
		t.Fatal(err)
	}
}

func TestOmniSubscriptionActualAcrossWindowKeepsNewAllowanceClean(t *testing.T) {
	_, info := actualAllowanceFixture(t)
	r, err := model.OmniReserveAllowance(9916, info.RequestId, "test-model", 800000)
	if err != nil {
		t.Fatal(err)
	}
	// The persisted hold belongs to the previous windows. The live windows
	// have reset, so late settlement must not consume their fresh allowance.
	model.DB.Model(&model.OmniReservation{}).Where("request_id = ?", r.RequestID).Updates(map[string]interface{}{"five_epoch": -1, "week_epoch": -1})
	model.DB.Model(&model.OmniSubscription{}).Where("user_id = ?", 9916).Updates(map[string]interface{}{"five_used": 0, "week_used": 0})
	if err = model.OmniAdjust(r.RequestID, 800000, true); err != nil {
		t.Fatal("old request failed completion", err)
	}
	var s model.OmniSubscription
	model.DB.First(&s, "user_id = ?", 9916)
	if s.FiveUsed != 0 || s.WeekUsed != 0 || s.FivePaused || s.WeekPaused {
		t.Fatal("old request consumed new window")
	}
}

func TestOmniSubscriptionActualWeekOverageWaitsForWeekReset(t *testing.T) {
	c, info := actualAllowanceFixture(t)
	model.DB.Model(&model.OmniSubscription{}).Where("user_id = ?", 9916).Update("week_limit", 4000)
	first, e := NewOmniBillingSession(c, info, 800000)
	if e != nil {
		t.Fatal(e)
	}
	if first.GetPreConsumedQuota() != 292 {
		t.Fatal("week remainder was ignored")
	}
	if err := first.Settle(1000); err != nil {
		t.Fatal(err)
	}
	model.DB.Model(&model.OmniSubscription{}).Where("user_id = ?", 9916).Update("five_epoch", -1)
	info.RequestId = "week-still-paused"
	if _, e = NewOmniBillingSession(c, info, 100); e == nil {
		t.Fatal("five-hour reset incorrectly restored exhausted week")
	}
	model.DB.Model(&model.OmniSubscription{}).Where("user_id = ?", 9916).Update("week_epoch", -1)
	info.RequestId = "week-restored"
	next, e := NewOmniBillingSession(c, info, 100)
	if e != nil {
		t.Fatal(e)
	}
	if err := next.Settle(100); err != nil {
		t.Fatal(err)
	}
}

func TestOmniSubscriptionActualUsageIgnoresPessimisticMaximum(t *testing.T) {
	c, info := actualAllowanceFixture(t)
	session, apiErr := NewOmniBillingSession(c, info, 800000)
	if apiErr != nil {
		t.Fatalf("a normal request with usable subscription was rejected by its theoretical maximum: %v", apiErr)
	}
	if session.funding.Source() != "omni_subscription" {
		t.Fatal("request used wallet")
	}
	if session.GetPreConsumedQuota() != 307345 || info.FinalPreConsumedQuota != 307345 {
		t.Fatal("reservation did not use the available allowance")
	}
	if *info.Request.(*dto.ClaudeRequest).MaxTokens != 64000 {
		t.Fatal("client output limit was rewritten")
	}
	if err := session.Reserve(1600000); err != nil {
		t.Fatal("routing estimate must not reject an admitted request", err)
	}
	if err := session.Settle(24823); err != nil {
		t.Fatal("actual usage settlement failed", err)
	}
	if err := session.Settle(24823); err != nil {
		t.Fatal("duplicate settlement failed", err)
	}
	var user model.User
	model.DB.First(&user, 9916)
	var subscription model.OmniSubscription
	model.DB.First(&subscription, "user_id = ?", 9916)
	if user.Quota != 2820950 || subscription.FiveUsed != 24823 || subscription.WeekUsed != 3708+24823 {
		t.Fatalf("wrong actual charge: wallet=%d five=%d week=%d", user.Quota, subscription.FiveUsed, subscription.WeekUsed)
	}
	var reservation model.OmniReservation
	model.DB.First(&reservation, "request_id = ?", info.RequestId)
	if reservation.State != "settled" || reservation.Actual != 24823 {
		t.Fatal("settlement not durable")
	}
}
