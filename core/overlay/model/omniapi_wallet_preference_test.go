package model

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOmniWalletPreferenceImmediatelyControlsNewReservations(t *testing.T) {
	for _, scenario := range []struct {
		name          string
		five, week    bool
		exhausted     bool
		weekExhausted bool
	}{
		{name: "five-exhausted", exhausted: true}, {name: "five-paused", five: true},
		{name: "week-exhausted", weekExhausted: true},
		{name: "week-paused", week: true}, {name: "both-paused", five: true, week: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			omniTestDB(t)
			now := time.Now().Unix()
			subscription := OmniSubscription{UserID: 91, Start: now - 60, End: now + 86400, FiveLimit: 1000, WeekLimit: 4000, FivePaused: scenario.five, WeekPaused: scenario.week, BillingVersion: 2, QuotaVersion: 2}
			if scenario.exhausted {
				subscription.FiveUsed = 1000
			}
			if scenario.weekExhausted {
				subscription.WeekUsed = 4000
			}
			require.NoError(t, DB.Create(&subscription).Error)
			_, err := OmniSetWalletOverflow(91, false)
			require.NoError(t, err)
			_, err = OmniReserveAllowance(91, "disabled", "test-model", 800000)
			require.ErrorIs(t, err, ErrOmniInsufficient)
			enabled, err := OmniSetWalletOverflow(91, true)
			require.NoError(t, err)
			assert.True(t, enabled)
			reservation, err := OmniReserveAllowance(91, "enabled", "test-model", 800000)
			require.NoError(t, err)
			assert.Equal(t, "wallet", reservation.Source)
			assert.EqualValues(t, 0, reservation.Reserved)
			assert.True(t, reservation.FlexibleAllowance)
			_, err = OmniSetWalletOverflow(91, false)
			require.NoError(t, err)
			_, err = OmniReserveAllowance(91, "disabled-again", "test-model", 100)
			require.ErrorIs(t, err, ErrOmniInsufficient)
			require.NoError(t, OmniAdjust("enabled", 123, true))
			require.NoError(t, OmniAdjust("enabled", 123, true))
			var user User
			require.NoError(t, DB.First(&user, 91).Error)
			var current OmniSubscription
			require.NoError(t, DB.First(&current, "user_id = ?", 91).Error)
			assert.Equal(t, 10000000-123, user.Quota)
			assert.Equal(t, subscription.FiveUsed, current.FiveUsed)
			assert.Equal(t, subscription.WeekUsed, current.WeekUsed)
			assert.Equal(t, subscription.FivePaused, current.FivePaused)
			assert.Equal(t, subscription.WeekPaused, current.WeekPaused)
		})
	}
}

func TestOmniWalletPreferenceInsufficientBalanceAndRefund(t *testing.T) {
	omniTestDB(t)
	now := time.Now().Unix()
	require.NoError(t, DB.Create(&OmniSubscription{UserID: 91, Start: now - 60, End: now + 86400, FiveLimit: 1000, WeekLimit: 4000, FivePaused: true, BillingVersion: 2, QuotaVersion: 2}).Error)
	_, err := OmniSetWalletOverflow(91, true)
	require.NoError(t, err)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 91).Update("quota", 0).Error)
	_, err = OmniReserveAllowance(91, "cannot-afford", "test-model", 600)
	var insufficient *OmniWalletQuotaError
	require.ErrorAs(t, err, &insufficient)
	assert.EqualValues(t, 0, insufficient.Available)
	assert.EqualValues(t, 1, insufficient.Required)
	var count int64
	require.NoError(t, DB.Model(&OmniReservation{}).Where("request_id = ?", "cannot-afford").Count(&count).Error)
	assert.Zero(t, count)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 91).Update("quota", 500).Error)
	_, err = OmniReserveAllowance(91, "refundable", "test-model", 500)
	require.NoError(t, err)
	_, err = OmniSetWalletOverflow(91, false)
	require.NoError(t, err)
	require.NoError(t, OmniAdjust("refundable", 0, true))
	require.NoError(t, OmniAdjust("refundable", 0, true))
	var user User
	require.NoError(t, DB.First(&user, 91).Error)
	assert.Equal(t, 500, user.Quota)
}

func TestOmniWalletPreferenceConcurrentCommitsAndAdmission(t *testing.T) {
	omniTestDB(t)
	now := time.Now().Unix()
	require.NoError(t, DB.Create(&OmniSubscription{UserID: 91, Start: now - 60, End: now + 86400, FiveLimit: 1000, WeekLimit: 4000, WeekPaused: true, BillingVersion: 2, QuotaVersion: 2}).Error)
	_, err := OmniSetWalletOverflow(91, false)
	require.NoError(t, err)
	var wg sync.WaitGroup
	failures := make(chan error, 32)
	for i := range 16 {
		wg.Go(func() {
			_, err := OmniSetWalletOverflow(91, i%2 == 0)
			if err != nil {
				failures <- err
			}
		})
		wg.Go(func() {
			reservation, err := OmniReserveAllowance(91, fmt.Sprintf("toggle-race-%d", i), "test-model", 100)
			if errors.Is(err, ErrOmniInsufficient) {
				return
			}
			if err != nil {
				failures <- err
				return
			}
			if reservation.Source != "wallet" {
				failures <- errors.New("paused allowance admitted subscription")
			}
			if err = OmniAdjust(reservation.RequestID, 0, true); err != nil {
				failures <- err
			}
		})
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	_, err = OmniSetWalletOverflow(91, false)
	require.NoError(t, err)
	_, err = OmniReserveAllowance(91, "final-close", "test-model", 100)
	require.ErrorIs(t, err, ErrOmniInsufficient)
	_, err = OmniSetWalletOverflow(91, true)
	require.NoError(t, err)
	_, err = OmniReserveAllowance(91, "final-open", "test-model", 100)
	require.NoError(t, err)
	require.NoError(t, OmniAdjust("final-open", 0, true))
	var user User
	require.NoError(t, DB.First(&user, 91).Error)
	assert.Equal(t, 10000000, user.Quota)
}
