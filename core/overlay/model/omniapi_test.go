package model

import (
	"encoding/json"
	"errors"
	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"net/url"
	"os"
	"testing"
	"time"
)

func omniTestDB(t *testing.T) {
	t.Helper()
	old := DB
	kind := common.MainDatabaseType()
	dialect := gorm.Dialector(sqlite.Open(":memory:"))
	dsn := os.Getenv("OMNI_TEST_DSN")
	if dsn != "" {
		parsed, err := url.Parse(dsn)
		if err != nil || parsed.Path != "/omniapi_v2_test" {
			t.Fatal("only dedicated omniapi_v2_test database is allowed")
		}
		dialect = postgres.Open(dsn)
		common.SetMainDatabaseType(common.DatabaseTypePostgreSQL)
	}
	db, err := gorm.Open(dialect, &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sql, _ := db.DB()
	if dsn == "" {
		sql.SetMaxOpenConns(1)
	} else {
		sql.SetMaxOpenConns(20)
		if err = db.Migrator().DropTable(&OmniAllowanceWindow{}, &OmniMigrationExit{}, &OmniMigrationItem{}, &OmniMigrationBatch{}, &OmniCommerceRelease{}, &SubscriptionOrder{}, &OmniSharedProcess{}, &OmniSharedRecovery{}, &OmniEmailCounter{}, &OmniEmailChallenge{}, &OmniCommerceTerm{}, &OmniCommerceProfile{}, &OmniSharedEvent{}, &OmniSharedHold{}, &OmniSharedBatch{}, &OmniSharedPeriod{}, &OmniSharedMember{}, &OmniSharedPool{}, &Token{}, &UserSession{}, &OmniTopup{}, &OmniLegacyPause{}, &OmniLegacyFlight{}, &OmniAttempt{}, &OmniNotice{}, &OmniCard{}, &OmniLedger{}, &OmniReservation{}, &OmniSubscription{}, &OmniOrder{}, &OmniAccount{}, &SubscriptionPlan{}, &UserSubscription{}, &User{}); err != nil {
			t.Fatal(err)
		}
	}
	DB = db
	t.Cleanup(func() { DB = old; common.SetMainDatabaseType(kind); sql.Close() })
	if err = DB.AutoMigrate(&User{}, &Option{}, &UserSubscription{}, &SubscriptionPlan{}); err != nil {
		t.Fatal(err)
	}
	if err = OmniMigrate(); err != nil {
		t.Fatal(err)
	}
	common.OptionMapRWMutex.Lock()
	prev := common.OptionMap["OmniAPICommercial"]
	p := OmniPolicy{SalesEnabled: true, BillingEnabled: true, MembershipEnabled: true, ReferralsEnabled: true, Models: map[string]OmniModelPolicy{"test-model": {Eligible: true, WorstCostRatio: 0.4, VerifiedAt: "2026-10-01"}}}
	data, _ := json.Marshal(p)
	if common.OptionMap == nil {
		common.OptionMap = map[string]string{}
	}
	common.OptionMap["OmniAPICommercial"] = string(data)
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap["OmniAPICommercial"] = prev
		common.OptionMapRWMutex.Unlock()
	})
	common.QuotaPerUnit = 500000
	if err = DB.Create(&User{Id: 91, Username: "omni-test", Quota: 10000000}).Error; err != nil {
		t.Fatal(err)
	}
}
func TestOmniQuotaAndCalendar(t *testing.T) {
	common.QuotaPerUnit = 500000
	if got := OmniQuota(133, 6748900); got != 98534 {
		t.Fatalf("quota %d", got)
	}
	start := time.Date(2026, 1, 31, 12, 0, 0, 0, time.UTC).Unix()
	end := time.Unix(OmniCalendarEnd(start, 1), 0)
	if end.Month() != time.February || end.Day() != 28 {
		t.Fatal(end)
	}
}
func TestOmniDualReservationRefundAndReset(t *testing.T) {
	omniTestDB(t)
	if err := OmniActivate("sub-1", 91, "starter", 1, 1900, 6748900); err != nil {
		t.Fatal(err)
	}
	r, err := OmniReserve(91, "request-one", "test-model", 90000)
	if err != nil || r.Source != "omni_subscription" {
		t.Fatal(r, err)
	}
	duplicate, err := OmniReserve(91, "request-one", "test-model", 90000)
	if err != nil || duplicate.Reserved != 90000 {
		t.Fatal(err)
	}
	codes, err := OmniGrantCards(91, "five_hour", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = OmniRedeem(91, codes[0]); err == nil {
		t.Fatal("reset admitted during request")
	}
	if err = OmniAdjust("request-one", 40000, true); err != nil {
		t.Fatal(err)
	}
	if err = OmniAdjust("request-one", 0, true); err != nil {
		t.Fatal(err)
	}
	var s OmniSubscription
	DB.First(&s, "user_id = ?", 91)
	if s.FiveUsed != 40000 || s.WeekUsed != 40000 {
		t.Fatal(s)
	}
	if err = OmniRedeem(91, codes[0]); err != nil {
		t.Fatal(err)
	}
	if err = OmniRedeem(91, codes[0]); err == nil {
		t.Fatal("card replay")
	}
	DB.First(&s, "user_id = ?", 91)
	if s.FiveUsed != 0 || s.WeekUsed != 40000 {
		t.Fatal(s)
	}
}
func TestOmniEpochRefundCannotCreditNewWindow(t *testing.T) {
	omniTestDB(t)
	OmniActivate("sub-1", 91, "starter", 1, 1900, 6748900)
	_, err := OmniReserve(91, "cross-window", "test-model", 90000)
	if err != nil {
		t.Fatal(err)
	}
	// Shift the subscription's anchor to emulate advancing time across five hours.
	DB.Model(&OmniSubscription{}).Where("user_id = ?", 91).Update("five_epoch", -1)
	if err = OmniAdjust("cross-window", 0, true); err != nil {
		t.Fatal(err)
	}
	var s OmniSubscription
	DB.First(&s, "user_id = ?", 91)
	if s.FiveUsed != 0 || s.WeekUsed != 0 {
		t.Fatal(s)
	}
}
func TestOmniOverflowPreferenceAndIdempotentCredit(t *testing.T) {
	omniTestDB(t)
	OmniActivate("sub-1", 91, "starter", 1, 1900, 6748900)
	p, _ := omniPlan("starter")
	_, err := OmniReserve(91, "fills", "test-model", OmniQuota(p.FiveHourCents, 6748900)-500)
	if err != nil {
		t.Fatal(err)
	}
	DB.Model(&OmniAccount{}).Where("user_id = ?", 91).Update("wallet_overflow", false)
	if _, err = OmniReserve(91, "blocked", "test-model", 1000); !errors.Is(err, ErrOmniInsufficient) {
		t.Fatal(err)
	}
	DB.Model(&OmniAccount{}).Where("user_id = ?", 91).Update("wallet_overflow", true)
	r, err := OmniReserve(91, "overflow", "test-model", 1000)
	if err != nil || r.Source != "wallet" {
		t.Fatal(r, err)
	}
	if err = OmniAdjust("overflow", 0, true); err != nil {
		t.Fatal(err)
	}
	credit, err := OmniCredit("receipt-200", 91, 20000, 100000)
	if err != nil || credit.Bonus != 3000 {
		t.Fatal(credit, err)
	}
	again, err := OmniCredit("receipt-200", 91, 20000, 100000)
	if err != nil || again.Bonus != 3000 {
		t.Fatal(err)
	}
	if _, err = OmniCredit("receipt-200", 91, 20100, 100000); err == nil {
		t.Fatal("receipt conflict accepted")
	}
	if err = OmniRefundCredit("receipt-200"); err != nil {
		t.Fatal(err)
	}
	if err = OmniRefundCredit("receipt-200"); err != nil {
		t.Fatal(err)
	}
	var a OmniAccount
	DB.First(&a, "user_id = ?", 91)
	if a.NetRechargeCents != 0 {
		t.Fatal(a)
	}
}
func TestOmniAnnualBenefitsAndWeekBudget(t *testing.T) {
	omniTestDB(t)
	OmniCredit("svip", 91, 100000, 100000)
	if err := OmniActivate("annual", 91, "pro", 12, 95040, 6748900); err != nil {
		t.Fatal(err)
	}
	codes, err := OmniClaimCards(91)
	if err != nil || len(codes) != 3 {
		t.Fatal(codes, err)
	}
	again, err := OmniClaimCards(91)
	if err != nil || len(again) != 0 {
		t.Fatal(again, err)
	}
	if _, err = OmniGrantCards(91, "week", 1); err == nil {
		t.Fatal("extra weekly card escaped budget")
	}
	if err = OmniActivate("renew", 91, "pro", 1, 9900, 6748900); err != nil {
		t.Fatal(err)
	}
	var s OmniSubscription
	DB.First(&s, "user_id = ?", 91)
	if s.End != OmniCalendarEnd(s.Start, 13) {
		t.Fatal(s)
	}
}
func TestOmniReferralMigrationExactlyOnce(t *testing.T) {
	omniTestDB(t)
	DB.Model(&User{}).Where("id = ?", 91).Updates(map[string]any{"aff_quota": 120, "aff_history": 240})
	if err := OmniMigrateReferral(91); err != nil {
		t.Fatal(err)
	}
	if err := OmniMigrateReferral(91); err != nil {
		t.Fatal(err)
	}
	var u User
	DB.First(&u, 91)
	if u.Quota != 10000120 || u.AffQuota != 0 || u.AffHistoryQuota != 240 {
		t.Fatal(u.Quota, u.AffQuota, u.AffHistoryQuota)
	}
}

func TestOmniRolloutKeepsOtherWalletUsersNative(t *testing.T) {
	omniTestDB(t)
	p := GetOmniPolicy()
	p.RolloutUserIDs = []int{92}
	data, _ := json.Marshal(p)
	common.OptionMapRWMutex.Lock()
	common.OptionMap["OmniAPICommercial"] = string(data)
	common.OptionMapRWMutex.Unlock()
	if active, err := OmniActive(91); err != nil || active {
		t.Fatalf("outside gray cohort: %v %v", active, err)
	}
	if active, err := OmniActive(92); err != nil || !active {
		t.Fatalf("gray wallet user excluded: %v %v", active, err)
	}
	if _, err := OmniCreateOrder(91, "starter", 1); err == nil {
		t.Fatal("outside cohort bought plan")
	}
}
