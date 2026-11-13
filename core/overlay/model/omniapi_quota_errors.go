package model

import (
	"fmt"
	"gorm.io/gorm"
)

// Carries the locked subscription snapshot so callers can explain the rejection.
// Keep the sentinel relationship for existing billing callers.
type OmniSubscriptionQuotaError struct {
	FiveDisabled                           bool
	Required, FiveAvailable, WeekAvailable int64
	FiveLimit, WeekLimit                   int64
	FiveResetAt, WeekResetAt               int64
	ModelIncluded                          bool
	ActualAllowance                        bool
}

func (e *OmniSubscriptionQuotaError) Error() string {
	return fmt.Sprintf("subscription quota insufficient: required=%d five_available=%d week_available=%d model_included=%t", e.Required, e.FiveAvailable, e.WeekAvailable, e.ModelIncluded)
}

func (e *OmniSubscriptionQuotaError) Unwrap() error { return ErrOmniInsufficient }

func omniSubscriptionQuotaError(s OmniSubscription, modelName string, required int64) error {
	return &OmniSubscriptionQuotaError{
		FiveDisabled: !s.FiveEnabled(), Required: required, FiveAvailable: max(0, s.FiveLimit-s.FiveUsed), WeekAvailable: max(0, s.WeekLimit-s.WeekUsed),
		FiveLimit: s.FiveLimit, WeekLimit: s.WeekLimit,
		FiveResetAt: s.Start + (s.FiveEpoch+1)*18000, WeekResetAt: s.Start + (s.WeekEpoch+1)*604800,
		ModelIncluded: s.Includes(modelName),
	}
}

func omniAllowanceQuotaError(s OmniSubscription, modelName string, required int64) error {
	e := omniSubscriptionQuotaError(s, modelName, required).(*OmniSubscriptionQuotaError)
	e.ActualAllowance = true
	return e
}

// A wallet funding failure must never be presented as a missing subscription.
type OmniWalletQuotaError struct {
	Available int64
	Required  int64
}

func (e *OmniWalletQuotaError) Error() string {
	return fmt.Sprintf("wallet quota insufficient: available=%d required=%d", e.Available, e.Required)
}

func omniWalletQuotaError(tx *gorm.DB, uid int, required int64) error {
	var user User
	if err := tx.Select("quota").First(&user, uid).Error; err != nil {
		return err
	}
	return &OmniWalletQuotaError{Available: int64(user.Quota), Required: required}
}
