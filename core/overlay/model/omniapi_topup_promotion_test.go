package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

func promotionFixture(t *testing.T) OmniTopupPromotion {
	t.Helper()
	omniTestDB(t)
	p := GetOmniPolicy()
	p.MembershipEnabled = true
	p.TopupPromotion = OmniTopupPromotion{ID: "double11-2026", Enabled: true, StartsAt: time.Now().Unix() - 7200, EndsAt: time.Now().Unix() - 3600, BonusBPS: 1500}
	b, _ := json.Marshal(p)
	common.OptionMapRWMutex.Lock()
	common.OptionMap["OmniAPICommercial"] = string(b)
	common.OptionMapRWMutex.Unlock()
	return p.TopupPromotion
}
func TestOmniPromotionPaymentBoundaryAndAnyAmount(t *testing.T) {
	p := promotionFixture(t)
	for i, q := range []int64{1, 6, 7, 99, 100, 101, 999, 500000, 9999999999999} {
		for j, at := range []int64{p.StartsAt - 1, p.StartsAt, p.EndsAt - 1, p.EndsAt} {
			r, err := OmniCredit(fmt.Sprintf("boundary:%d:%d", i, j), 91, 100, q, OmniPaymentEvidence{at, "manual:verified_transaction"})
			expected := int64(0)
			if j == 1 || j == 2 {
				expected = q/100*15 + q%100*15/100
			}
			if err != nil || r.Bonus != expected || r.CampaignBonus != expected {
				t.Fatalf("q=%d at=%d got=%+v err=%v", q, at, r, err)
			}
		}
	}
}
func TestOmniPromotionLateSupplementConcurrentAndRefund(t *testing.T) {
	p := promotionFixture(t)
	r, err := OmniCredit("pending", 91, 20000, 500001)
	if err != nil || r.Bonus != 0 || r.PromotionState != "pending_payment_time" {
		t.Fatal(r, err)
	}
	var before User
	DB.First(&before, 91)
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := OmniCredit("pending", 91, 20000, 500001, OmniPaymentEvidence{p.StartsAt, "manual:verified_transaction"})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var after User
	DB.First(&after, 91)
	if after.Quota-before.Quota != 75000 {
		t.Fatal(before.Quota, after.Quota)
	}
	var a OmniAccount
	DB.First(&a, 91)
	if a.NetRechargeCents != 20000 {
		t.Fatal(a)
	}
	var notices int64
	DB.Model(&OmniNotice{}).Where("key = ?", "promotion:"+p.ID+":pending").Count(&notices)
	if notices != 1 {
		t.Fatal(notices)
	}
	for i := 0; i < 2; i++ {
		if err = OmniRefundCredit("pending"); err != nil {
			t.Fatal(err)
		}
	}
	DB.First(&after, 91)
	if after.Quota != before.Quota-500001 {
		t.Fatal(after.Quota, before.Quota)
	}
	if _, err = OmniCredit("pending", 91, 20000, 500001, OmniPaymentEvidence{p.StartsAt, "manual:verified_transaction"}); err != nil {
		t.Fatal(err)
	}
	DB.First(&before, 91)
	if before.Quota != after.Quota {
		t.Fatal("refunded receipt awarded twice")
	}
}
func TestOmniPromotionRollbackAndFrozenEvidence(t *testing.T) {
	p := promotionFixture(t)
	e := OmniPaymentEvidence{p.StartsAt, "xorpay:signed_notify"}
	o := topupFixture(t, "xp_promote", 1000)
	DB.Callback().Create().Before("gorm:create").Register("test:promotion_notice", func(tx *gorm.DB) {
		if tx.Statement.Table == "omni_notices" {
			tx.AddError(errors.New("injected notice failure"))
		}
	})
	if _, err := OmniFulfillTopup(o.ID, "123", "receipt", o.Cents, true, e); err == nil {
		t.Fatal("notice failure must rollback money")
	}
	DB.Callback().Create().Remove("test:promotion_notice")
	var count int64
	DB.Model(&OmniLedger{}).Count(&count)
	if count != 0 {
		t.Fatal(count)
	}
	r, err := OmniFulfillTopup(o.ID, "123", "receipt", o.Cents, true, e)
	if err != nil || r.Bonus != 15000 || r.PaidAt != e.PaidAt {
		t.Fatal(r, err)
	}
	if _, err = OmniFulfillTopup(o.ID, "123", "receipt", o.Cents, true, OmniPaymentEvidence{p.EndsAt, "xorpay:signed_notify"}); err == nil {
		t.Fatal("conflicting payment time accepted")
	}
}
func TestOmniPromotionRefundPendingAndOverflow(t *testing.T) {
	p := promotionFixture(t)
	if _, err := OmniCredit("refund-pending", 91, 100, 2000); err != nil {
		t.Fatal(err)
	}
	if err := OmniRefundCredit("refund-pending"); err != nil {
		t.Fatal(err)
	}
	r, err := OmniCredit("refund-pending", 91, 100, 2000, OmniPaymentEvidence{p.StartsAt, "manual:verified_transaction"})
	if err != nil || r.CampaignBonus != 0 {
		t.Fatal(r, err)
	}
	if _, err = OmniCredit("overflow", 91, 100, 9223372036854775807, OmniPaymentEvidence{p.StartsAt, "manual:verified_transaction"}); err == nil {
		t.Fatal("integer balance overflow accepted")
	}
	if _, err = OmniCredit("untrusted", 91, 100, 100, OmniPaymentEvidence{p.StartsAt, "browser"}); err == nil {
		t.Fatal("untrusted payment time accepted")
	}
}
func TestOmniPromotionBenefitSwitchSeparate(t *testing.T) {
	_ = promotionFixture(t)
	p := GetOmniPolicy()
	enabled := true
	p.BenefitCardsEnabled = &enabled
	p.MembershipEnabled = false
	if !p.BenefitsEnabled() || p.MembershipEnabled {
		t.Fatal(p)
	}
	enabled = false
	p.MembershipEnabled = true
	if p.BenefitsEnabled() {
		t.Fatal("benefits coupled to recharge bonus")
	}
}
func TestOmniPromotionDiscountReadDecoration(t *testing.T) {
	promotionFixture(t)
	common.OptionMapRWMutex.Lock()
	old := common.OptionMap[OmniUserDiscountOption]
	common.OptionMap[OmniUserDiscountOption] = `{"version":"test","users":{"16":{"enabled":true,"basis":"procurement_margin","target_margin":0.6,"funding_scope":"wallet","model_scope":"text","public_label":"价格优惠"}}}`
	common.OptionMapRWMutex.Unlock()
	defer func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap[OmniUserDiscountOption] = old
		common.OptionMapRWMutex.Unlock()
	}()
	raw := `{"price_discount_applied":false,"billing_source":"omni_subscription","charge":123}`
	for _, uid := range []int{16, 17} {
		var m map[string]any
		json.Unmarshal([]byte(OmniPublicLogOther(OmniDecorateDiscountLabel(uid, raw))), &m)
		if m["price_discount_applied"] != false || m["charge"] != float64(123) || ((m["price_discount_rule_enabled"] == true) != (uid == 16)) {
			t.Fatal(uid, m)
		}
	}
}

func TestOmniPromotionBackfillTopsUpLegacyBonusToFifteen(t *testing.T) {
	p := promotionFixture(t)
	policy := GetOmniPolicy()
	policy.TopupPromotion.Enabled = false
	save := func() {
		b, _ := json.Marshal(policy)
		common.OptionMapRWMutex.Lock()
		common.OptionMap["OmniAPICommercial"] = string(b)
		common.OptionMapRWMutex.Unlock()
	}
	save()
	r, err := OmniCredit("legacy-bonus", 91, 20000, 100000)
	if err != nil || r.Bonus != 3000 {
		t.Fatal(r, err)
	}
	policy.TopupPromotion.Enabled = true
	save()
	r, err = OmniCredit("legacy-bonus", 91, 20000, 100000, OmniPaymentEvidence{p.StartsAt, "manual:verified_transaction"})
	if err != nil || r.Bonus != 15000 || r.CampaignBonus != 12000 {
		t.Fatal(r, err)
	}
}
