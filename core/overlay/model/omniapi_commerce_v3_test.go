package model

import (
	"encoding/json"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
	"sync"
	"testing"
	"time"
)

func commerceOption(t *testing.T, key string, value any) {
	t.Helper()
	common.OptionMapRWMutex.Lock()
	old, ok := common.OptionMap[key]
	raw, _ := json.Marshal(value)
	common.OptionMap[key] = string(raw)
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		if ok {
			common.OptionMap[key] = old
		} else {
			delete(common.OptionMap, key)
		}
		common.OptionMapRWMutex.Unlock()
	})
}
func commerceFixture(t *testing.T) OmniCommerceProfile {
	t.Helper()
	walletFixture(t)
	require.NoError(t, DB.AutoMigrate(&Token{}, &UserSession{}))
	commerceOption(t, "OmniAPIPricing", map[string]any{"enabled": true, "models": map[string]any{"gpt-6.1-sol": map[string]any{"costs": map[string]any{"7": map[string]any{"complete_prices_verified": true}, "8": map[string]any{"complete_prices_verified": true}}}}})
	commerceOption(t, "OmniAPIEmailV3", OmniEmailPolicy{Enabled: true, DeliveryAccepted: true, SenderDNSAccepted: true})
	commerceOption(t, "OmniAPIRouting", map[string]any{"models": map[string][]int{"gpt-6.1-sol": {7, 8}}})
	now := time.Now().Unix()
	p := OmniCommerceProfile{ID: "synthetic-audit", ApprovedBy: 1, ApprovedAt: now - 10, AuditExpiresAt: now + 86400, Evidence: "SYNTHETIC TEST ONLY: complete context/cache/image/fees/retry audit", Models: `{"gpt-6.1-sol":[7,8]}`, FXMicros: 7000000, WorstCostPPM: 400000, PaymentFeePPM: 10000, RiskReservePPM: 10000, FixedFeeCents: 1}
	p = commerceLaunchTestProfile(t, p)
	require.NoError(t, OmniCommerceApprove(p))
	require.NoError(t, DB.First(&p, "id = ?", p.ID).Error)
	commerceOption(t, "OmniAPICommerceV3", OmniCommerceSwitches{Enabled: true, SalesEnabled: true, ProfileID: p.ID, EmailReady: true, AcceptanceID: "synthetic-acceptance"})
	require.NoError(t, DB.Create(&OmniCommerceRelease{ID: "unified-20261104", State: "active", ProfileID: p.ID, AcceptanceID: "synthetic-acceptance"}).Error)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 91).Update("quota", 1000000000).Error)
	return p
}
func TestOmniCommercePricesCalendarAndCostGate(t *testing.T) {
	for _, r := range []struct {
		monthly int64
		want    []int64
	}{{1900, []int64{1900, 5500, 9900, 18900}}, {3900, []int64{3900, 10900, 20900, 38900}}, {6900, []int64{6900, 19900, 36900, 69900}}, {9900, []int64{9900, 27900, 52900, 99900}}, {19900, []int64{19900, 56900, 106900, 199900}}} {
		for i, m := range []int{1, 3, 6, 12} {
			got, e := OmniCommercePrice(r.monthly, m)
			require.NoError(t, e)
			require.Equal(t, r.want[i], got)
		}
	}
	_, err := OmniCommercePrice(1900, 2)
	require.Error(t, err)
	zone := time.FixedZone("CST", 28800)
	for _, year := range []int{2027, 2028} {
		start := time.Date(year, 1, 31, 23, 59, 59, 0, zone)
		end := time.Unix(OmniCalendarEnd(start.Unix(), 1), 0).In(zone)
		require.Equal(t, time.February, end.Month())
		require.Equal(t, time.Date(year, 3, 0, 0, 0, 0, 0, zone).Day(), end.Day())
		require.Equal(t, 23, end.Hour())
	}
	p := commerceFixture(t)
	plans, e := p.Plans()
	require.NoError(t, e)
	for i, r := range plans {
		require.Equal(t, i < 2, r.FiveEnabled)
		for _, m := range []int{1, 3, 6, 12} {
			grant, e := p.GrantCents(r.Prices[m])
			require.NoError(t, e)
			require.LessOrEqual(t, r.WeekCents*map[int]int64{1: 5, 3: 14, 6: 27, 12: 53}[m], grant)
		}
	}
	commerceOption(t, "OmniAPICommerceV3", OmniCommerceSwitches{Enabled: true})
	require.ErrorIs(t, OmniCommercePurchase(91, "v3_19", 1, p.ID, "closed-sale-request"), ErrOmniCommerceClosed)
}
func TestOmniCommerceRenewQueueConversionAndReset(t *testing.T) {
	p := commerceFixture(t)
	require.NoError(t, OmniCommercePurchase(91, "v3_69", 1, p.ID, "first-purchase-0001"))
	var s OmniSubscription
	require.NoError(t, DB.First(&s, "user_id = ?", 91).Error)
	require.False(t, s.FiveEnabled())
	start, end := s.Start, s.End
	r, e := OmniReserveAllowance(91, "high-tier-no-five", "gpt-6.1-sol", 1000)
	require.NoError(t, e)
	require.Equal(t, "omni_subscription", r.Source)
	require.NoError(t, OmniAdjust(r.RequestID, 800, true))
	require.NoError(t, OmniCommercePurchase(91, "v3_69", 3, p.ID, "extend-purchase-001"))
	require.NoError(t, DB.First(&s, "user_id = ?", 91).Error)
	require.EqualValues(t, 800, s.WeekUsed)
	require.Zero(t, s.FiveUsed)
	require.Equal(t, start, s.Start)
	require.Greater(t, s.End, end)
	quote, e := OmniCommerceResetQuote(91)
	require.NoError(t, e)
	beforeEnd, beforeEpoch := s.End, s.WeekEpoch
	_, e = OmniReserveAllowance(91, "busy-reset-request", "gpt-6.1-sol", 1)
	require.NoError(t, e)
	require.Error(t, OmniCommerceReset(91, "paid-reset-request1", quote))
	require.NoError(t, OmniAdjust("busy-reset-request", 0, true))
	require.NoError(t, OmniCommerceReset(91, "paid-reset-request1", quote))
	require.NoError(t, OmniCommerceReset(91, "paid-reset-request1", quote))
	require.NoError(t, DB.First(&s, "user_id = ?", 91).Error)
	require.Zero(t, s.WeekUsed)
	require.Equal(t, beforeEnd, s.End)
	require.Equal(t, beforeEpoch, s.WeekEpoch)
	_, e = OmniReserveAllowance(91, "not-audited-model", "other-model", 1)
	require.Error(t, e)
	// Legacy scope conversion changes no window, used amount, or term dates.
	require.NoError(t, DB.Model(&s).Updates(map[string]any{"entitlement_version": 0, "plan_key": "pro", "five_limit": 10000, "five_used": 123, "week_used": 456, "commerce_profile": ""}).Error)
	require.NoError(t, DB.First(&s, "user_id = ?", 91).Error)
	old := s
	require.NoError(t, OmniCommerceConvert(91, p.ID))
	require.NoError(t, DB.First(&s, "user_id = ?", 91).Error)
	require.True(t, s.FiveEnabled())
	require.Equal(t, old.End, s.End)
	require.Equal(t, old.FiveUsed, s.FiveUsed)
	require.Equal(t, old.WeekUsed, s.WeekUsed)
	require.NoError(t, OmniCommercePurchase(91, "v3_99", 1, p.ID, "queue-new-rights001"))
	require.NoError(t, DB.First(&s, "user_id = ?", 91).Error)
	require.Equal(t, "pro", s.PlanKey)
	require.Equal(t, old.End, s.End)
	require.NoError(t, s.advance(s.End))
	require.Equal(t, "v3_99", s.PlanKey)
	require.False(t, s.FiveEnabled())
	require.Zero(t, s.WeekUsed)
	require.Equal(t, old.End, s.Start)
}
func sharedFixture(t *testing.T) (OmniCommerceProfile, string, Token) {
	t.Helper()
	p := commerceFixture(t)
	v, e := OmniSharedMutate(91, "", OmniSharedCommand{Action: "create", RequestKey: "create-pool-000001", Name: "合成共享池", Cents: 9900, ProfileID: p.ID})
	require.NoError(t, e)
	id := v["pool_id"].(string)
	key := Token{UserId: 91, Key: "synthetic-shared-key", Status: common.TokenStatusEnabled, OmniPoolID: id, OmniFallback: "off", ExpiredTime: -1, UnlimitedQuota: true}
	require.NoError(t, DB.Create(&key).Error)
	return p, id, key
}
func TestOmniSharedConcurrencyFIFOAndStableAllocation(t *testing.T) {
	_, id, key := sharedFixture(t)
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, e := OmniSharedReserve(91, key.Id, id, fmt.Sprintf("parallel-%d", i), "gpt-6.1-sol", 100)
			errs <- e
		}(i)
	}
	wg.Wait()
	close(errs)
	success := 0
	for e := range errs {
		if e == nil {
			success++
		} else {
			require.ErrorIs(t, e, ErrOmniSharedConcurrent)
		}
	}
	require.Equal(t, 16, success)
	var holds []OmniSharedHold
	require.NoError(t, DB.Where("pool_id = ?", id).Find(&holds).Error)
	for _, h := range holds {
		require.NoError(t, OmniSharedSettle(h.RequestID, 50))
		require.NoError(t, OmniSharedSettle(h.RequestID, 50))
	}
	var batch OmniSharedBatch
	require.NoError(t, DB.First(&batch, "pool_id = ?", id).Error)
	require.Zero(t, batch.Reserved)
	require.EqualValues(t, 800, batch.Used)
	var allocated int64
	require.NoError(t, DB.Model(&OmniSharedHold{}).Where("pool_id = ?", id).Select("SUM(cost_used_micros)").Scan(&allocated).Error)
	require.Equal(t, batch.CostUsedMicros, allocated)
	h, e := OmniSharedReserve(91, key.Id, id, "failed-original-batch", "gpt-6.1-sol", 100)
	require.NoError(t, e)
	require.NoError(t, OmniSharedSettle(h.RequestID, 0))
	require.NoError(t, DB.First(&batch, "pool_id = ?", id).Error)
	require.EqualValues(t, 800, batch.Used)
	require.Zero(t, batch.Reserved)
	v, e := OmniSharedView(91, id, "")
	require.NoError(t, e)
	require.Equal(t, batch.PriceCents*10000-allocated, v["unconsumed_micros"])
}
func TestOmniSharedBudgetMembershipFallbackAndPrepurchase(t *testing.T) {
	p, id, key := sharedFixture(t)
	require.NoError(t, DB.Create(&User{Id: 92, Username: "member92", AffCode: "m92", Status: 1, Quota: 100000000}).Error)
	cmd := func(uid int, c OmniSharedCommand) map[string]any {
		t.Helper()
		v, e := OmniSharedMutate(uid, id, c)
		require.NoError(t, e)
		return v
	}
	cmd(91, OmniSharedCommand{Action: "invite", RequestKey: "invite-member-0001", UserID: 92, BudgetCents: 0})
	cmd(92, OmniSharedCommand{Action: "accept", RequestKey: "accept-member-0001"})
	memberKey := Token{UserId: 92, Key: "member-key", Status: 1, OmniPoolID: id, OmniFallback: "wallet_only", ExpiredTime: -1, UnlimitedQuota: true}
	require.NoError(t, DB.Create(&memberKey).Error)
	_, e := OmniSharedReserve(92, memberKey.Id, id, "budget-fallback-001", "gpt-6.1-sol", 100)
	require.ErrorIs(t, e, ErrOmniSharedUnavailable)
	r, e := OmniSharedPersonalReserve(92, "budget-fallback-001", "gpt-6.1-sol", 100, id, memberKey.Id)
	require.NoError(t, e)
	require.Equal(t, "wallet", r.Source)
	require.NoError(t, OmniAdjust(r.RequestID, 90, true))
	cmd(91, OmniSharedCommand{Action: "remove", RequestKey: "remove-member-0001", UserID: 92})
	_, e = OmniSharedPersonalReserve(92, "removed-fallback01", "gpt-6.1-sol", 100, id, memberKey.Id)
	require.Error(t, e)
	queued := cmd(91, OmniSharedCommand{Action: "purchase", RequestKey: "prepurchase-000001", Cents: 19900, ProfileID: p.ID})
	periodID := queued["period_id"].(string)
	h, e := OmniSharedReserve(91, key.Id, id, "pending-not-empty1", "gpt-6.1-sol", 100)
	require.NoError(t, e)
	var pool OmniSharedPool
	require.NoError(t, DB.First(&pool, "id = ?", id).Error)
	require.NotEqual(t, periodID, pool.CurrentPeriod)
	cmd(91, OmniSharedCommand{Action: "cancel", RequestKey: "cancel-queued-0001", PeriodID: periodID})
	cmd(91, OmniSharedCommand{Action: "cancel", RequestKey: "cancel-queued-0001", PeriodID: periodID})
	require.NoError(t, OmniSharedSettle(h.RequestID, 0))
	require.NoError(t, DB.First(&memberKey, memberKey.Id).Error)
	require.True(t, memberKey.OmniSharedRevoked)
}
func TestOmniSharedRestartAndPinnedTaskSettlement(t *testing.T) {
	_, id, key := sharedFixture(t)
	h, e := OmniSharedReserve(91, key.Id, id, "orphan-unsent-0001", "gpt-6.1-sol", 100)
	require.NoError(t, e)
	require.NoError(t, DB.Model(h).Update("owner", "prior-process").Error)
	require.NoError(t, OmniSharedRecoverOrphans())
	require.NoError(t, DB.First(h, "request_id = ?", h.RequestID).Error)
	require.Equal(t, "refunded", h.State)
	h, e = OmniSharedReserve(91, key.Id, id, "orphan-sent-000001", "gpt-6.1-sol", 100)
	require.NoError(t, e)
	require.NoError(t, OmniSharedMarkDispatch(h.RequestID))
	require.NoError(t, DB.Model(h).Update("owner", "prior-process").Error)
	require.NoError(t, OmniSharedRecoverOrphans())
	var pool OmniSharedPool
	require.NoError(t, DB.First(&pool, "id = ?", id).Error)
	require.True(t, pool.Frozen)
	// Recovered final evidence can settle the original source after a restart.
	changed, e := OmniAdjustOnce(h.RequestID, 60, true)
	require.NoError(t, e)
	require.True(t, changed)
	changed, e = OmniAdjustOnce(h.RequestID, 60, true)
	require.NoError(t, e)
	require.False(t, changed)
}

func TestOmniCommerceNativeLegacyRightsQueueWithoutPause(t *testing.T) {
	p := commerceFixture(t)
	now := time.Now().Unix()
	native := UserSubscription{UserId: 91, PlanId: 1, Status: "active", StartTime: now - 86400, EndTime: now + 86400, AmountTotal: 10000, AmountUsed: 100}
	require.NoError(t, DB.Create(&native).Error)
	require.NoError(t, OmniCommercePurchase(91, "v3_39", 1, p.ID, "native-legacy-queue1"))
	require.NoError(t, DB.First(&native, native.Id).Error)
	require.Equal(t, "active", native.Status)
	require.EqualValues(t, 100, native.AmountUsed)
	var s OmniSubscription
	require.NoError(t, DB.First(&s, "user_id = ?", 91).Error)
	require.True(t, s.NativeLegacyAwait)
	require.Equal(t, native.EndTime, s.End)
	require.NoError(t, OmniLegacyBegin(91, "existing-native-request"))
	require.NoError(t, s.advance(native.EndTime))
	require.False(t, s.NativeLegacyAwait)
	require.Equal(t, "v3_39", s.PlanKey)
	require.Equal(t, native.EndTime, s.Start)
}

func TestOmniSharedFullReservationDoesNotActivateNextPeriod(t *testing.T) {
	p, id, key := sharedFixture(t)
	v, e := OmniSharedMutate(91, id, OmniSharedCommand{Action: "purchase", RequestKey: "next-period-reserved", Cents: 19900, ProfileID: p.ID})
	require.NoError(t, e)
	next := v["period_id"].(string)
	var pool OmniSharedPool
	require.NoError(t, DB.First(&pool, "id = ?", id).Error)
	current := pool.CurrentPeriod
	var batch OmniSharedBatch
	require.NoError(t, DB.First(&batch, "period_id = ?", current).Error)
	h, e := OmniSharedReserve(91, key.Id, id, "all-reserved-pending", "gpt-6.1-sol", batch.Granted)
	require.NoError(t, e)
	_, e = OmniSharedView(91, id, "")
	require.NoError(t, e)
	require.NoError(t, DB.First(&pool, "id = ?", id).Error)
	require.Equal(t, current, pool.CurrentPeriod)
	require.NoError(t, OmniSharedSettle(h.RequestID, batch.Granted))
	require.NoError(t, DB.First(&pool, "id = ?", id).Error)
	require.Equal(t, next, pool.CurrentPeriod)
}
func TestOmniSharedLateRepurchaseStartsAtPurchaseNotExpiredAnchor(t *testing.T) {
	p, id, _ := sharedFixture(t)
	var pool OmniSharedPool
	require.NoError(t, DB.First(&pool, "id = ?", id).Error)
	require.NoError(t, DB.Model(&OmniSharedPeriod{}).Where("id = ?", pool.CurrentPeriod).Update("end", time.Now().Unix()-90*86400).Error)
	now := time.Now().Unix()
	v, e := OmniSharedMutate(91, id, OmniSharedCommand{Action: "purchase", RequestKey: "late-purchase-period", Cents: 9900, ProfileID: p.ID})
	require.NoError(t, e)
	var period OmniSharedPeriod
	require.NoError(t, DB.First(&period, "id = ?", v["period_id"]).Error)
	require.GreaterOrEqual(t, period.Start, now)
	require.Greater(t, period.End, time.Now().Unix())
}
func TestOmniSharedLiveNodeLeaseAndRecoveryAudit(t *testing.T) {
	_, id, key := sharedFixture(t)
	h, e := OmniSharedReserve(91, key.Id, id, "other-live-node-hold", "gpt-6.1-sol", 100)
	require.NoError(t, e)
	require.NoError(t, DB.Model(h).Update("owner", "other-live-node").Error)
	require.NoError(t, DB.Create(&OmniSharedProcess{ID: "other-live-node", HeartbeatAt: time.Now().Unix()}).Error)
	require.NoError(t, OmniSharedRecoverOrphans())
	require.NoError(t, DB.First(h, "request_id = ?", h.RequestID).Error)
	require.Equal(t, "pending", h.State)
	require.NoError(t, DB.Model(&OmniSharedProcess{}).Where("id = ?", "other-live-node").Update("heartbeat_at", time.Now().Unix()-121).Error)
	require.NoError(t, OmniSharedMarkDispatch(h.RequestID))
	require.NoError(t, OmniSharedRecoverOrphans())
	require.Error(t, OmniSharedMarkDispatch(h.RequestID))
	require.NoError(t, OmniSharedReconcile(1, h.RequestID, 60, "SYNTHETIC final provider evidence", "recover-once-key001"))
	require.NoError(t, OmniSharedReconcile(1, h.RequestID, 60, "SYNTHETIC final provider evidence", "recover-once-key001"))
	require.Error(t, OmniSharedReconcile(1, h.RequestID, 70, "SYNTHETIC final provider evidence", "recover-once-key001"))
	require.NoError(t, OmniSharedUnfreeze(1, id, "SYNTHETIC all final usage reconciled", "unfreeze-pool-key01"))
}

func TestOmniSharedRevokedKeyCannotReviveAfterReinvite(t *testing.T) {
	_, id, _ := sharedFixture(t)
	require.NoError(t, DB.Create(&User{Id: 92, Username: "reinvite-member", AffCode: "rein", Status: 1}).Error)
	cmd := func(uid int, c OmniSharedCommand) {
		t.Helper()
		_, e := OmniSharedMutate(uid, id, c)
		require.NoError(t, e)
	}
	cmd(91, OmniSharedCommand{Action: "invite", RequestKey: "original-invite-key", UserID: 92, BudgetCents: -1})
	cmd(92, OmniSharedCommand{Action: "accept", RequestKey: "original-accept-key"})
	key := Token{UserId: 92, Key: "oldrevokedsharedkey", Status: 1, OmniPoolID: id, ExpiredTime: -1, UnlimitedQuota: true}
	require.NoError(t, key.Insert())
	cmd(91, OmniSharedCommand{Action: "remove", RequestKey: "remove-for-reinvite", UserID: 92})
	cmd(91, OmniSharedCommand{Action: "invite", RequestKey: "new-invite-after-remove", UserID: 92, BudgetCents: -1})
	cmd(92, OmniSharedCommand{Action: "accept", RequestKey: "new-accept-after-remove"})
	// Even an old risk-restore status write cannot undo irreversible pool revocation.
	require.NoError(t, DB.Model(&Token{}).Where("id = ?", key.Id).Update("status", 1).Error)
	_, e := OmniSharedReserve(92, key.Id, id, "revoked-key-admission", "gpt-6.1-sol", 1)
	require.Error(t, e)
}

func TestOmniSharedDifferentMembersSerializePoolLimit(t *testing.T) {
	_, poolID, _ := sharedFixture(t)
	keys := []Token{}
	for i := 0; i < 20; i++ {
		uid := 300 + i
		require.NoError(t, DB.Create(&User{Id: uid, Username: fmt.Sprintf("shared-parallel-%d", uid), AffCode: fmt.Sprintf("mp%d", uid), Status: 1}).Error)
		require.NoError(t, DB.Create(&OmniSharedMember{PoolID: poolID, UserID: uid, State: "active", BudgetMicros: -1, JoinedAt: time.Now().Unix()}).Error)
		key := Token{UserId: uid, Key: fmt.Sprintf("parallelmemberkey%d", uid), Status: 1, OmniPoolID: poolID, OmniFallback: "off", ExpiredTime: -1, UnlimitedQuota: true}
		require.NoError(t, key.Insert())
		keys = append(keys, key)
	}
	var wg sync.WaitGroup
	results := make(chan error, len(keys))
	for _, key := range keys {
		wg.Add(1)
		go func(key Token) {
			defer wg.Done()
			_, err := OmniSharedReserve(key.UserId, key.Id, poolID, fmt.Sprintf("member-request-%d", key.UserId), "gpt-6.1-sol", 100)
			results <- err
		}(key)
	}
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		} else {
			require.ErrorIs(t, err, ErrOmniSharedConcurrent)
		}
	}
	require.Equal(t, 16, accepted)
	var holds []OmniSharedHold
	require.NoError(t, DB.Where("pool_id = ? AND state = ?", poolID, "pending").Find(&holds).Error)
	for _, hold := range holds {
		require.NoError(t, OmniSharedSettle(hold.RequestID, 50))
	}
	var batch OmniSharedBatch
	require.NoError(t, DB.First(&batch, "pool_id = ?", poolID).Error)
	require.EqualValues(t, 800, batch.Used)
	require.Zero(t, batch.Reserved)
}
func TestOmniSharedEstimatorOverflowFreezesWithoutOverdraft(t *testing.T) {
	_, poolID, key := sharedFixture(t)
	var before User
	require.NoError(t, DB.First(&before, 91).Error)
	hold, err := OmniSharedReserve(91, key.Id, poolID, "overflow-bound-request", "gpt-6.1-sol", 100)
	require.NoError(t, err)
	require.NoError(t, OmniSharedSettle(hold.RequestID, 200))
	require.NoError(t, DB.First(hold, "request_id = ?", hold.RequestID).Error)
	require.EqualValues(t, 100, hold.PlatformQuota)
	var pool OmniSharedPool
	require.NoError(t, DB.First(&pool, "id = ?", poolID).Error)
	require.True(t, pool.Frozen)
	var batch OmniSharedBatch
	require.NoError(t, DB.First(&batch, "pool_id = ?", poolID).Error)
	require.EqualValues(t, 100, batch.Used)
	require.Zero(t, batch.Reserved)
	var after User
	require.NoError(t, DB.First(&after, 91).Error)
	require.Equal(t, before.Quota, after.Quota)
}
