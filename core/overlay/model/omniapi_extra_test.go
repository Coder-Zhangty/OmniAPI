package model

import (
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"sync"
	"testing"
	"time"
)
func TestOmniConcurrentWalletReservationsAndRefund(t *testing.T) {
 omniTestDB(t)
 DB.Model(&User{}).Where("id = ?",91).Update("quota",100000)
 var wg sync.WaitGroup;var mu sync.Mutex;accepted:=[]string{}
 for i:=0;i<20;i++{wg.Add(1);go func(i int){defer wg.Done();id:=fmt.Sprintf("wallet-concurrent-%d",i);if _,err:=OmniReserve(91,id,"",60000);err==nil{mu.Lock();accepted=append(accepted,id);mu.Unlock()}}(i)}
 wg.Wait();if len(accepted)!=1{t.Fatalf("accepted %d instead of one wallet reservation",len(accepted))}
 var user User;DB.First(&user,91);if user.Quota!=40000{t.Fatalf("wallet quota %d",user.Quota)}
 if err:=OmniAdjust(accepted[0],0,true);err!=nil{t.Fatal(err)}
 if err:=OmniAdjust(accepted[0],0,true);err!=nil{t.Fatal(err)}
 DB.First(&user,91);if user.Quota!=100000{t.Fatalf("idempotent refund quota %d",user.Quota)}
}

func TestOmniConcurrentFullReservations(t *testing.T) {
	omniTestDB(t)
	if err := OmniActivate("start", 91, "starter", 1, 1900, 6748900); err != nil {
		t.Fatal(err)
	}
	DB.Model(&OmniAccount{}).Where("user_id = ?", 91).Update("wallet_overflow", false)
	p,_:=omniPlan("starter")
 amount:=OmniQuota(p.FiveHourCents,6748900)/2+1
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := OmniReserve(91, fmt.Sprintf("concurrent-%d", i), "test-model", amount); err == nil {
				mu.Lock()
				accepted++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if accepted != 1 {
		t.Fatalf("accepted %d instead of one", accepted)
	}
	var s OmniSubscription
	DB.First(&s, 91)
	if s.FiveUsed != amount || s.WeekUsed != amount {
		t.Fatal(s)
	}
}
func TestOmniOrderOwnershipAndPaymentReplay(t *testing.T) {
	omniTestDB(t)
	o, err := OmniCreateOrder(91, "starter", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = OmniActivate("bank-1", 91, "starter", 1, 1900, 6748900, o.ID); err == nil {
		t.Fatal("unconfirmed order activated")
	}
	DB.Model(&OmniOrder{}).Where("id = ?", o.ID).Update("state", "pending")
	if err = OmniActivate("bank-1", 91, "starter", 1, 1900, 6748900, o.ID); err != nil {
		t.Fatal(err)
	}
	if err = OmniActivate("bank-1", 91, "starter", 1, 1900, 6748900, o.ID); err != nil {
		t.Fatal(err)
	}
	if err = OmniActivate("bank-2", 91, "starter", 1, 1900, 6748900, o.ID); err == nil {
		t.Fatal("order paid twice")
	}
	if err = OmniActivate("missing-user", 999, "starter", 1, 1900, 6748900); err == nil {
		t.Fatal("unknown user activated")
	}
}
func TestOmniNewSubscriptionRefundIsolation(t *testing.T) {
	omniTestDB(t)
	OmniActivate("initial", 91, "starter", 1, 1900, 6748900)
	OmniReserve(91, "old-request", "test-model", 50000)
	var s OmniSubscription
	DB.First(&s, 91)
	s.Start -= 18000
	s.FiveUsed = 12000
	s.WeekUsed = 14000
	DB.Save(&s)
	if err := OmniAdjust("old-request", 0, true); err != nil {
		t.Fatal(err)
	}
	DB.First(&s, 91)
	if s.FiveUsed != 0 || s.WeekUsed != 14000 {
		t.Fatal(s)
	}
}
func TestOmniQueuedChangeAndMonthlyCards(t *testing.T) {
	omniTestDB(t)
	OmniCredit("vip", 91, 20000, 100000)
	OmniActivate("initial", 91, "starter", 1, 1900, 6748900)
	if err := OmniActivate("upgrade", 91, "pro", 1, 9900, 6748900); err != nil {
		t.Fatal(err)
	}
	var s OmniSubscription
	DB.First(&s, 91)
	if s.PlanKey != "starter" || s.PendingPlan != "pro" {
		t.Fatal(s)
	}
	if err := s.Project(s.End + 1); err != nil {
		t.Fatal(err)
	}
	if s.PlanKey != "pro" || s.PendingMonths != 0 {
		t.Fatal(s)
	}
	// Annual subscriptions issue benefits per anchored calendar month.
	s.PlanKey = "pro"
	s.Start = time.Now().AddDate(0, -1, 0).Unix()
	s.End = time.Now().AddDate(1, 0, 0).Unix()
	DB.Save(&s)
	first, err := OmniClaimCards(91)
	if err != nil || len(first) != 1 {
		t.Fatal(first, err)
	}
	s.Start = time.Now().AddDate(0, -2, 0).Unix()
	DB.Save(&s)
	second, err := OmniClaimCards(91)
	if err != nil || len(second) != 1 {
		t.Fatal(second, err)
	}
}
func TestOmniHistoricalReceiptNeverCreditsAgain(t *testing.T) {
	omniTestDB(t)
	if err := OmniImportReceipt("legacy:bank-original", 91, 20000, 50000); err != nil {
		t.Fatal(err)
	}
	if err := OmniImportReceipt("legacy:bank-original", 91, 20000, 50000); err != nil {
		t.Fatal(err)
	}
	if _, err := OmniCredit("bank-original", 91, 20000, 50000); err != nil {
		t.Fatal(err)
	}
	var u User
	DB.First(&u, 91)
	if u.Quota != 10000000 {
		t.Fatal("legacy base credited twice")
	}
	var a OmniAccount
	DB.First(&a, 91)
	if level, _ := OmniLevel(a.NetRechargeCents); level != "vip" {
		t.Fatal(a)
	}
}
func TestOmniReferralCutoverAndReplay(t *testing.T) {
	omniTestDB(t)
	if err := OmniEnableReferrals(); err != nil {
		t.Fatal(err)
	}
	if start := GetOmniPolicy().ReferralStartID; start != 92 {
		t.Fatal(start)
	}
	if err := DB.Create(&User{Id: 92, Username: "invited", AffCode: "test-invite-92", InviterId: 91}).Error; err != nil {
		t.Fatal(err)
	}
	if err := OmniRegistrationReward(91, 92); err != nil {
		t.Fatal(err)
	}
	if err := OmniRegistrationReward(91, 92); err != nil {
		t.Fatal(err)
	}
	var u User
	DB.First(&u, 91)
	if u.AffCount != 1 || u.AffQuota != 0 {
		t.Fatal(u.AffCount, u.AffQuota)
	}
	var n int64
	DB.Model(&OmniLedger{}).Where("key = ?", "referral:92").Count(&n)
	if n != 1 {
		t.Fatal(n)
	}
	if err := OmniEnableReferrals(); err != nil {
		t.Fatal(err)
	}
	if start := GetOmniPolicy().ReferralStartID; start != 92 {
		t.Fatal(start)
	}
}
func TestOmniCalendarShanghaiBoundaryAndClippedRenewal(t *testing.T) {
	zone := time.FixedZone("Asia/Shanghai", 8*3600)
	start := time.Date(2026, 5, 1, 0, 30, 0, 0, zone)
	end := time.Unix(OmniCalendarEnd(start.Unix(), 1), 0).In(zone)
	if end.Month() != time.June || end.Day() != 1 || end.Hour() != 0 {
		t.Fatal(end)
	}
	omniTestDB(t)
	anchor := time.Date(2026, 1, 31, 12, 0, 0, 0, zone).Unix()
	s := OmniSubscription{UserID: 91, PlanKey: "starter", Start: anchor, End: OmniCalendarEnd(anchor, 1), MonthsPurchased: 1}
	if err := DB.Save(&s).Error; err != nil {
		t.Fatal(err)
	}
	if got := OmniCalendarEnd(anchor, 2); time.Unix(got, 0).In(zone).Day() != 31 {
		t.Fatal(got)
	}
}

func TestOmniPaidModelSnapshotsSurviveCatalogRemoval(t *testing.T) {
	omniTestDB(t)
	if err := OmniActivate("current", 91, "starter", 1, 1900, 6748900); err != nil {
		t.Fatal(err)
	}
	if err := OmniActivate("next", 91, "pro", 1, 9900, 6748900); err != nil {
		t.Fatal(err)
	}
	common.OptionMapRWMutex.Lock()
	p := common.OptionMap["OmniAPICommercial"]
	common.OptionMap["OmniAPICommercial"] = `{"billing_enabled":true,"models":{}}`
	common.OptionMapRWMutex.Unlock()
	defer func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap["OmniAPICommercial"] = p
		common.OptionMapRWMutex.Unlock()
	}()
	r, err := OmniReserve(91, "paid-model", "test-model", 1000)
	if err != nil || r.Source != "omni_subscription" {
		t.Fatal(r, err)
	}
	var s OmniSubscription
	DB.First(&s, 91)
	if err = s.Project(s.End + 1); err != nil {
		t.Fatal(err)
	}
	if !s.Includes("test-model") {
		t.Fatal("queued paid model was removed")
	}
}
func TestOmniLateLegacyReferralMigration(t *testing.T) {
	omniTestDB(t)
	for _, amount := range []int{120, 60} {
		DB.Model(&User{}).Where("id = ?", 91).Update("aff_quota", amount)
		if err := OmniMigrateReferral(91); err != nil {
			t.Fatal(err)
		}
		if err := OmniMigrateReferral(91); err != nil {
			t.Fatal(err)
		}
	}
	var u User
	DB.First(&u, 91)
	if u.Quota != 10000180 {
		t.Fatal(u.Quota)
	}
}

func TestOmniUnverifiedCatalogCannotSell(t *testing.T) {
	omniTestDB(t)
	common.OptionMapRWMutex.Lock()
	common.OptionMap["OmniAPICommercial"] = `{"sales_enabled":true,"billing_enabled":true,"models":{"unknown":{"eligible":true,"worst_cost_ratio":0.5,"verified_at":"2026-10-01"}}}`
	common.OptionMapRWMutex.Unlock()
	if OmniSalesReady() {
		t.Fatal("over-budget catalog admitted")
	}
	if _, err := OmniCreateOrder(91, "starter", 1); err == nil {
		t.Fatal("unverified order admitted")
	}
}
