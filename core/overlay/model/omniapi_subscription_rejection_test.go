package model

import (
	"errors"
	"testing"
	"time"
)

func TestOmniSubscriptionRejectionPreservesSnapshot(t *testing.T) {
	omniTestDB(t)
	now := time.Now().Unix()
	s := OmniSubscription{UserID: 91, PlanKey: "standard", Start: now - 18001, End: now + 86400, FxMicros: 6735100, FiveLimit: 1000, WeekLimit: 4000, FiveUsed: 800, WeekUsed: 3700, FiveEpoch: 1, QuotaVersion: 2}
	if err := DB.Create(&s).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := omniAccount(DB, 91); err != nil {
		t.Fatal(err)
	}
	if err := DB.Model(&OmniAccount{}).Where("user_id = ?", 91).Update("wallet_overflow", false).Error; err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		included bool
		required int64
	}{{"test-model", true, 500}, {"not-eligible", false, 100}} {
		_, err := OmniReserve(91, "rejection-"+test.name, test.name, test.required)
		var detail *OmniSubscriptionQuotaError
		if !errors.As(err, &detail) || !errors.Is(err, ErrOmniInsufficient) {
			t.Fatalf("missing typed rejection: %v", err)
		}
		if detail.Required != test.required || detail.FiveAvailable != 200 || detail.WeekAvailable != 300 || detail.ModelIncluded != test.included || detail.FiveResetAt != s.Start+36000 || detail.WeekResetAt != s.Start+604800 {
			t.Fatalf("wrong snapshot: %+v", detail)
		}
	}
	var user User
	DB.First(&user, 91)
	var after OmniSubscription
	DB.First(&after, "user_id = ?", 91)
	var count int64
	DB.Model(&OmniReservation{}).Count(&count)
	if user.Quota != 10000000 || after.FiveUsed != 800 || after.WeekUsed != 3700 || count != 0 {
		t.Fatal("rejection mutated billing state")
	}
}
