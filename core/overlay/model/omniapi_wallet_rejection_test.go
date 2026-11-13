package model

import (
	"errors"
	"strings"
	"testing"
)

func TestOmniWalletRejectionIsNotSubscription(t *testing.T) {
	omniTestDB(t)
	const amount = int64(10000001)
	_, err := OmniReserve(91, "wallet-over-budget", "test-model", amount)
	if err == nil || errors.Is(err, ErrOmniInsufficient) || !strings.Contains(err.Error(), "wallet") {
		t.Fatalf("wallet rejection incorrectly classified: %v", err)
	}
	var user User
	if err := DB.First(&user, 91).Error; err != nil {
		t.Fatal(err)
	}
	if user.Quota != 10000000 {
		t.Fatal("rejected reservation changed wallet")
	}
	var reservations int64
	DB.Model(&OmniReservation{}).Where("request_id = ?", "wallet-over-budget").Count(&reservations)
	if reservations != 0 {
		t.Fatal("rejected request persisted a reservation")
	}
	record, err := OmniReserve(91, "wallet-small-request", "test-model", 1000)
	if err != nil || record.Source != "wallet" {
		t.Fatalf("unsubscribed wallet request blocked: %v", err)
	}
	if err := OmniAdjust(record.RequestID, 0, true); err != nil {
		t.Fatal(err)
	}
	if err := OmniAdjust(record.RequestID, 0, true); err != nil {
		t.Fatal(err)
	}
	DB.First(&user, 91)
	if user.Quota != 10000000 {
		t.Fatal("refund was not idempotent")
	}
}
