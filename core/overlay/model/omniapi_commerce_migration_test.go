package model

import (
	"encoding/json"
	"fmt"
	"github.com/stretchr/testify/require"
	"sync"
	"testing"
	"time"
)

func migrationFixture(t *testing.T) (OmniCommerceProfile, OmniSubscription) {
	p := commerceFixture(t)
	now := time.Now().Unix()
	start := OmniCalendarEnd(now, -1) + 86400
	s := OmniSubscription{UserID: 91, PlanKey: "pro", Start: start, End: OmniCalendarEnd(start, 13), MonthsPurchased: 13, LastPurchaseMonths: 12, FxMicros: 7000000, FiveLimit: 10000, WeekLimit: 40000, FiveUsed: 12000, WeekUsed: 50000, QuotaVersion: 2, BillingVersion: 2}
	require.NoError(t, DB.Create(&s).Error)
	for i, months := range []int{1, 12} {
		q := int64(100000 + 900000*i)
		key := fmt.Sprintf("original-%d", i)
		require.NoError(t, DB.Create(&OmniOrder{ID: key, Receipt: key, UserID: 91, PlanKey: "pro", Months: months, Cents: int64(9900 + 85140*i), State: "fulfilled", CreatedAt: start - 86400 + int64(i)}).Error)
		require.NoError(t, DB.Create(&OmniLedger{Key: key, UserID: 91, Kind: "subscription:pro", Cents: int64(9900 + 85140*i), Quota: q, CreatedAt: start - 86400 + int64(i)}).Error)
	}
	cfg := OmniCommerceSettings()
	cfg.SalesEnabled = false
	commerceOption(t, "OmniAPICommerceV3", cfg)
	require.NoError(t, DB.Delete(&OmniCommerceRelease{}, "id = ?", "unified-20261104").Error)
	require.NoError(t, OmniMigrationCloseLegacy(1, p.ID, "synthetic-release-acceptance"))
	return p, s
}
func TestOmniCommerceMigrationRefillAnchorsAndRetry(t *testing.T) {
	p, old := migrationFixture(t)
	batch, err := OmniMigrationPrepare(1, "migration-batch-0001", p.ID)
	require.NoError(t, err)
	require.NoError(t, OmniMigrationApply(1, batch.ID, batch.PreviewHash))
	var s OmniSubscription
	require.NoError(t, DB.First(&s, "user_id = ?", 91).Error)
	require.Equal(t, old.Start, s.Start)
	require.Equal(t, old.End, s.End)
	require.Equal(t, 13, s.MonthsPurchased)
	require.False(t, s.FiveEnabled())
	require.Zero(t, s.WeekUsed)
	require.Equal(t, (time.Now().Unix()-old.Start)/604800, s.WeekEpoch)
	require.NoError(t, DB.Model(&s).Update("week_used", int64(123)).Error)
	require.NoError(t, OmniMigrationApply(1, batch.ID, batch.PreviewHash))
	require.NoError(t, DB.First(&s, "user_id = ?", 91).Error)
	require.EqualValues(t, 123, s.WeekUsed)
	var item OmniMigrationItem
	require.NoError(t, DB.First(&item, "user_id = ?", 91).Error)
	var segments []OmniMigrationSegment
	require.NoError(t, json.Unmarshal([]byte(item.SegmentsJSON), &segments))
	require.Len(t, segments, 2)
	require.Equal(t, OmniCalendarEnd(old.Start, 1), segments[1].Start)
	require.EqualValues(t, 1000000, segments[1].PaidQuota)
	require.Error(t, OmniPurchaseWallet(91, "pro", 1, "retired-purchase-0001"))
}

func TestOmniCommerceMigrationMustFinishBeforeEmailIndependentSales(t *testing.T) {
	p, _ := migrationFixture(t)
	require.Error(t, OmniMigrationOpenSales(1, "nonexistent-batch", "synthetic-release-acceptance"))
	b, e := OmniMigrationPrepare(1, "release-batch-0001", p.ID)
	require.NoError(t, e)
	require.NoError(t, OmniMigrationApply(1, b.ID, b.PreviewHash))
	commerceOption(t, "OmniAPIEmailV3", OmniEmailPolicy{Enabled: false})
	require.NoError(t, OmniMigrationOpenSales(1, b.ID, "synthetic-release-acceptance"))
	require.NoError(t, OmniCommercePurchase(91, "v3_69", 3, p.ID, "after-real-cutover-purchase"))
	var order OmniOrder
	require.NoError(t, DB.Where("plan_key = ? AND price_version <> ?", "v3_69", "").First(&order).Error)
	require.EqualValues(t, 19900, order.Cents)
}
func TestOmniCommerceMigrationStaleAndBusyRollback(t *testing.T) {
	p, _ := migrationFixture(t)
	batch, e := OmniMigrationPrepare(1, "migration-batch-stale", p.ID)
	require.NoError(t, e)
	require.NoError(t, DB.Model(&OmniSubscription{}).Where("user_id = ?", 91).Update("week_used", 123).Error)
	require.Error(t, OmniMigrationApply(1, batch.ID, batch.PreviewHash))
	var n int64
	require.NoError(t, DB.Model(&OmniMigrationItem{}).Count(&n).Error)
	require.Zero(t, n)
	batch, e = OmniMigrationPrepare(1, "migration-batch-busy", p.ID)
	require.NoError(t, e)
	require.NoError(t, DB.Create(&OmniReservation{RequestID: "pending-cutover", UserID: 91, Source: "omni_subscription", State: "pending"}).Error)
	require.Error(t, OmniMigrationApply(1, batch.ID, batch.PreviewHash))
}
func TestOmniCommerceMigrationExitRefundAndQueuedNewPurchase(t *testing.T) {
	p, old := migrationFixture(t)
	batch, e := OmniMigrationPrepare(1, "migration-exit-batch", p.ID)
	require.NoError(t, e)
	require.NoError(t, OmniMigrationApply(1, batch.ID, batch.PreviewHash))
	cfg := OmniCommerceSettings()
	cfg.SalesEnabled = true
	commerceOption(t, "OmniAPICommerceV3", cfg)
	require.NoError(t, DB.Model(&OmniCommerceRelease{}).Where("id = ?", "unified-20261104").Updates(map[string]any{"state": "active", "acceptance_id": cfg.AcceptanceID}).Error)
	require.NoError(t, OmniCommercePurchase(91, "v3_99", 1, p.ID, "new-purchase-after-migration"))
	var s OmniSubscription
	require.NoError(t, DB.First(&s, "user_id = ?", 91).Error)
	require.Equal(t, old.End, s.End)
	var queue []OmniCommerceTerm
	require.NoError(t, json.Unmarshal([]byte(s.CommerceQueue), &queue))
	require.Len(t, queue, 1)
	require.Equal(t, old.End, queue[0].NotBefore)
	r, e := OmniMigrationExitRequest(91, "request-exit-0001", "不同意迁移")
	require.NoError(t, e)
	again, e := OmniMigrationExitRequest(91, "request-exit-0001", "不同意迁移")
	require.NoError(t, e)
	require.Equal(t, r.RefundQuota, again.RefundQuota)
	var item OmniMigrationItem
	require.NoError(t, DB.First(&item, "user_id = ?", 91).Error)
	var parts []OmniMigrationSegment
	require.NoError(t, json.Unmarshal([]byte(item.SegmentsJSON), &parts))
	expected := int64(0)
	for _, part := range parts {
		q, e := omniIntRatio(part.PaidQuota, max(int64(0), part.End-max(r.RequestedAt, part.Start)), part.End-part.Start, false)
		require.NoError(t, e)
		expected += q
	}
	require.Equal(t, expected, r.RefundQuota)
	require.NoError(t, DB.First(&s, "user_id = ?", 91).Error)
	require.True(t, s.paused())
	require.Zero(t, s.available())
	_, e = OmniCommerceResetQuote(91)
	require.Error(t, e)
	var balance User
	require.NoError(t, DB.First(&balance, 91).Error)
	before := balance.Quota
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- OmniMigrationExitResolve(1, 91, r.ID, "approve", "核对原购买段")
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
	require.NoError(t, DB.First(&balance, 91).Error)
	require.EqualValues(t, int64(before)+r.RefundQuota, balance.Quota)
	require.NoError(t, DB.First(&s, "user_id = ?", 91).Error)
	require.LessOrEqual(t, s.End, time.Now().Unix())
	require.NoError(t, s.advance(time.Now().Unix()))
	require.Equal(t, old.Start, s.Start)
	require.NoError(t, s.advance(old.End))
	require.Equal(t, old.End, s.Start)
	require.Equal(t, "v3_99", s.PlanKey)
}
func TestOmniCommerceMigrationWithdrawalDoesNotRefreshOrExtend(t *testing.T) {
	p, _ := migrationFixture(t)
	b, e := OmniMigrationPrepare(1, "migration-withdrawal-batch", p.ID)
	require.NoError(t, e)
	require.NoError(t, OmniMigrationApply(1, b.ID, b.PreviewHash))
	var s OmniSubscription
	require.NoError(t, DB.First(&s, "user_id = ?", 91).Error)
	end := s.End
	require.NoError(t, DB.Model(&s).Update("week_used", 123).Error)
	r, e := OmniMigrationExitRequest(91, "request-exit-withdraw", "核对")
	require.NoError(t, e)
	require.NoError(t, OmniMigrationExitResolve(91, 91, r.ID, "withdraw", "撤回"))
	require.NoError(t, DB.First(&s, "user_id = ?", 91).Error)
	require.Empty(t, s.ExitApplicationID)
	require.EqualValues(t, 123, s.WeekUsed)
	require.Equal(t, end, s.End)
	require.Error(t, OmniMigrationExitResolve(1, 91, r.ID, "approve", "迟到审批"))
}
func TestOmniCommerceMigrationNativeTrialUsesExactOriginalDebit(t *testing.T) {
	p := commerceFixture(t)
	now := time.Now().Unix()
	plan := SubscriptionPlan{Title: "OmniAPI 体验包", Currency: "USD", PriceAmount: 2.815273, DurationUnit: "day", DurationValue: 30, QuotaResetPeriod: "never"}
	require.NoError(t, DB.Create(&plan).Error)
	native := UserSubscription{UserId: 91, PlanId: plan.Id, AmountTotal: 1407636, StartTime: now - 86400, EndTime: now + 29*86400, Status: "active", Source: "balance"}
	require.NoError(t, DB.Create(&native).Error)
	require.NoError(t, DB.Create(&SubscriptionOrder{UserId: 91, PlanId: plan.Id, TradeNo: "native-original-paid", Status: "success", PaymentMethod: "balance", CompleteTime: native.CreatedAt, ProviderPayload: `{"charged_quota":1407637}`}).Error)
	cfg := OmniCommerceSettings()
	cfg.SalesEnabled = false
	commerceOption(t, "OmniAPICommerceV3", cfg)
	require.NoError(t, DB.Delete(&OmniCommerceRelease{}, "id = ?", "unified-20261104").Error)
	require.NoError(t, OmniMigrationCloseLegacy(1, p.ID, "native-acceptance-record"))
	b, e := OmniMigrationPrepare(1, "migration-native-batch", p.ID)
	require.NoError(t, e)
	require.NoError(t, OmniMigrationApply(1, b.ID, b.PreviewHash))
	var s OmniSubscription
	require.NoError(t, DB.First(&s, "user_id = ?", 91).Error)
	require.Equal(t, native.StartTime, s.Start)
	require.Equal(t, native.EndTime, s.End)
	require.True(t, s.FiveEnabled())
	require.Equal(t, "v3_19", s.PlanKey)
	require.NoError(t, DB.First(&native, native.Id).Error)
	require.Equal(t, "migrated", native.Status)
	r, e := OmniMigrationExitRequest(91, "native-refund-request", "核对原币")
	require.NoError(t, e)
	expected, _ := omniIntRatio(1407637, max(int64(0), native.EndTime-r.RequestedAt), native.EndTime-native.StartTime, false)
	require.Equal(t, expected, r.RefundQuota)
}

func TestOmniCommerceMigrationExitWaitsForOriginalRequestAndKeepsWallet(t *testing.T) {
	p, old := migrationFixture(t)
	batch, e := OmniMigrationPrepare(1, "inflight-migration-batch", p.ID)
	require.NoError(t, e)
	require.NoError(t, OmniMigrationApply(1, batch.ID, batch.PreviewHash))
	original, e := OmniReserveAllowance(91, "original-inflight-request", "gpt-6.1-sol", 500)
	require.NoError(t, e)
	require.Equal(t, "omni_subscription", original.Source)
	request, e := OmniMigrationExitRequest(91, "inflight-exit-request", "核对")
	require.NoError(t, e)
	require.Error(t, OmniMigrationExitResolve(1, 91, request.ID, "approve", "等待结算"))
	wallet, e := OmniReservePersonalMode(91, "independent-wallet-request", "gpt-6.1-sol", 100, true, "wallet_only")
	require.NoError(t, e)
	require.Equal(t, "wallet", wallet.Source)
	require.NoError(t, OmniAdjust(wallet.RequestID, 25, true))
	require.NoError(t, OmniAdjust(original.RequestID, 200, true))
	require.NoError(t, OmniMigrationExitResolve(91, 91, request.ID, "withdraw", "撤回"))
	var s OmniSubscription
	require.NoError(t, DB.First(&s, "user_id = ?", 91).Error)
	require.EqualValues(t, 200, s.WeekUsed)
	require.Equal(t, old.End, s.End)
}
