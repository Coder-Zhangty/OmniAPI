package model

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestOmniAllowanceRate(t *testing.T) {
	expected := [][2]int64{{202, 806}, {414, 1655}, {1050, 4200}}
	for i, p := range OmniPlans {
		if p.FiveHourCents != expected[i][0] || p.WeekCents != expected[i][1] {
			t.Fatal(p)
		}
		ratio := float64(p.MonthlyCents) / (float64(p.WeekCents) * 30 / 7) * 2
		if ratio < 1.099 || ratio > 1.101 {
			t.Fatal("monthly procurement factor", ratio)
		}
		if p.AnnualCents*5 != p.MonthlyCents*12*4 {
			t.Fatal("annual discount changed")
		}
	}
}
func TestOmniAllowanceUpgradeKeepsUsageAndReservations(t *testing.T) {
	omniTestDB(t)
	now := time.Now().Unix()
	fx := int64(6748900)
	s := OmniSubscription{UserID: 91, PlanKey: "starter", Start: now - 3600, End: OmniCalendarEnd(now, 1), MonthsPurchased: 1, LastPurchaseMonths: 1, FxMicros: fx, FiveLimit: OmniQuota(133, fx), WeekLimit: OmniQuota(532, fx), FiveUsed: 3000, WeekUsed: 9000, BillingVersion: 2, IncludedModels: OmniModelSnapshot()}
	if e := DB.Create(&s).Error; e != nil {
		t.Fatal(e)
	}
	r, e := OmniReserve(91, "quota-upgrade-flight", "test-model", 1000)
	if e != nil || r.Source != "omni_subscription" {
		t.Fatal(e, r)
	}
	var wallet User
	DB.First(&wallet, 91)
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- OmniReconcile(91) }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var actual OmniSubscription
	DB.First(&actual, "user_id = ?", 91)
	if actual.Start != s.Start || actual.End != s.End || actual.FiveEpoch != s.FiveEpoch || actual.WeekEpoch != s.WeekEpoch || actual.FiveUsed != 4000 || actual.WeekUsed != 10000 || actual.QuotaVersion != OmniQuotaVersion || actual.FiveLimit != OmniQuota(202, fx) || actual.WeekLimit != OmniQuota(806, fx) {
		t.Fatal(actual)
	}
	var count int64
	DB.Model(&OmniLedger{}).Where("kind = ?", "subscription_quota_upgrade").Count(&count)
	if count != 1 {
		t.Fatal("duplicate upgrade", count)
	}
	var audit OmniLedger
	DB.First(&audit, "key = ?", fmt.Sprintf("quota-upgrade:%d:%d:%d", OmniQuotaVersion, 91, s.Start))
	var details map[string]any
	if json.Unmarshal([]byte(audit.AuditJSON), &details) != nil || details["old_five"] == nil {
		t.Fatal("audit missing")
	}
	if e = OmniAdjust(r.RequestID, 800, true); e != nil {
		t.Fatal(e)
	}
	DB.First(&actual, "user_id = ?", 91)
	if actual.FiveUsed != 3800 || actual.WeekUsed != 9800 {
		t.Fatal("settlement lost", actual)
	}
	var after User
	DB.First(&after, 91)
	if after.Quota != wallet.Quota {
		t.Fatal("upgrade charged wallet")
	}
	// A later call refunded during the same windows removes only its own usage.
	r, e = OmniReserve(91, "quota-upgrade-refund", "test-model", 1000)
	if e != nil {
		t.Fatal(e)
	}
	if e = OmniAdjust(r.RequestID, 0, true); e != nil {
		t.Fatal(e)
	}
	DB.First(&actual, "user_id = ?", 91)
	if actual.FiveUsed != 3800 || actual.WeekUsed != 9800 {
		t.Fatal("refund usage", actual)
	}
}
func TestOmniAllowanceDoesNotReduceGrantedLimits(t *testing.T) {
	omniTestDB(t)
	now := time.Now().Unix()
	s := OmniSubscription{UserID: 91, PlanKey: "starter", Start: now - 10, End: now + 86400, FxMicros: 6748900, FiveLimit: 1000000, WeekLimit: 2000000, FiveUsed: 3000, WeekUsed: 9000}
	DB.Create(&s)
	if e := OmniReconcile(91); e != nil {
		t.Fatal(e)
	}
	var a OmniSubscription
	DB.First(&a, "user_id = ?", 91)
	if a.FiveLimit != s.FiveLimit || a.WeekLimit != s.WeekLimit || a.FiveUsed != s.FiveUsed || a.WeekUsed != s.WeekUsed {
		t.Fatal(a)
	}
}
