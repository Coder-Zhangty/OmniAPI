package model

import (
	"encoding/json"
	"errors"
	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"sync"
	"testing"
	"time"
)

func topupFixture(t *testing.T, id string, cents int64) OmniTopup {
	t.Helper()
	o := OmniTopup{ID: id, RequestID: id, UserID: 91, AID: "123", Cents: cents, Quota: 100000, State: "pending", AttemptedAt: time.Now().Unix(), CreatedAt: time.Now().Unix(), ExpiresAt: time.Now().Unix() - 10, FxRate: "6.7195", FxSource: "test", FxAsOf: "2026-10-22"}
	if err := DB.Create(&o).Error; err != nil {
		t.Fatal(err)
	}
	return o
}
func TestOmniXorTopupCreditPolicy(t *testing.T) {
	omniTestDB(t)
	common.OptionMapRWMutex.Lock()
	p := OmniPolicy{MembershipEnabled: false}
	raw, _ := json.Marshal(p)
	common.OptionMap["OmniAPICommercial"] = string(raw)
	common.OptionMapRWMutex.Unlock()
	o := topupFixture(t, "xp_policy", 20000)
	got, err := OmniFulfillTopup(o.ID, "123", "provider_1", 20000, true)
	if err != nil || got.Bonus != 0 || got.State != "credited" {
		t.Fatal(got, err)
	}
	common.OptionMapRWMutex.Lock()
	p.MembershipEnabled = true
	raw, _ = json.Marshal(p)
	common.OptionMap["OmniAPICommercial"] = string(raw)
	common.OptionMapRWMutex.Unlock()
	if _, err = OmniFulfillTopup(o.ID, "123", "", 0, false); err != nil {
		t.Fatal(err)
	}
	var user User
	DB.First(&user, 91)
	if user.Quota != 10100000 {
		t.Fatal(user.Quota)
	}
	var account OmniAccount
	DB.First(&account, 91)
	if account.NetRechargeCents != 20000 {
		t.Fatal(account)
	}
	second := topupFixture(t, "xp_policy_2", 100)
	got, err = OmniFulfillTopup(second.ID, "123", "provider_2", 100, true)
	if err != nil || got.Bonus != 3000 {
		t.Fatal(got, err)
	}
	if _, err = OmniFulfillTopup(o.ID, "123", "another", 20000, true); err == nil {
		t.Fatal("accepted conflicting provider receipt")
	}
}
func TestOmniXorTopupConcurrentQueryAndNotify(t *testing.T) {
	omniTestDB(t)
	o := topupFixture(t, "xp_concurrent", 100)
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := OmniFulfillTopup(o.ID, "123", "receipt_concurrent", 100, i%2 == 0)
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var n int64
	DB.Model(&OmniLedger{}).Where("key = ?", "xorpay:123:"+o.ID).Count(&n)
	if n != 1 {
		t.Fatal(n)
	}
	var user User
	DB.First(&user, 91)
	if user.Quota != 10100000 {
		t.Fatal(user.Quota)
	}
}
func TestOmniXorTopupRollbackAndQuarantine(t *testing.T) {
	omniTestDB(t)
	o := topupFixture(t, "xp_rollback", 100)
	DB.Callback().Create().Before("gorm:create").Register("test:fail_notice", func(tx *gorm.DB) {
		if tx.Statement.Table == "omni_notices" {
			tx.AddError(errors.New("injected notification storage failure"))
		}
	})
	if _, err := OmniFulfillTopup(o.ID, "123", "receipt_rollback", 100, true); err == nil {
		t.Fatal("expected rollback")
	}
	DB.Callback().Create().Remove("test:fail_notice")
	var user User
	DB.First(&user, 91)
	if user.Quota != 10000000 {
		t.Fatal("partial wallet update", user.Quota)
	}
	var n int64
	DB.Model(&OmniLedger{}).Count(&n)
	if n != 0 {
		t.Fatal("partial ledger", n)
	}
	if _, err := OmniFulfillTopup(o.ID, "123", "receipt_rollback", 99, true); err == nil {
		t.Fatal("amount mismatch accepted")
	}
	if _, err := OmniFulfillTopup(o.ID, "123", "", 0, false); err == nil {
		t.Fatal("query bypassed signed conflict quarantine")
	}
	if _, err := OmniFulfillTopup(o.ID, "123", "receipt_rollback", 100, true); err != nil {
		t.Fatal(err)
	}
	if _, err := OmniTopupForUser(o.ID, 92); err == nil {
		t.Fatal("cross-user access")
	}
}
