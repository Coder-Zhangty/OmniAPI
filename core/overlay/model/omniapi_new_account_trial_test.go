package model

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

func trialFixture(t *testing.T, uid int) {
	t.Helper()
	db := riskV2Fixture(t)
	riskV2User(t, db, uid)
	require.NoError(t, db.Model(&OmniRiskPolicyV2{}).Where("id = 1").
		Updates(map[string]any{"shadow": false, "new_user_protection": true}).Error)
	common.OptionMapRWMutex.Lock()
	if common.OptionMap == nil {
		common.OptionMap = make(map[string]string)
	}
	old, existed := common.OptionMap[OmniTrialOption]
	common.OptionMap[OmniTrialOption] = fmt.Sprintf(`{"mode":"pilot","user_ids":[%d]}`, uid)
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		defer common.OptionMapRWMutex.Unlock()
		if existed {
			common.OptionMap[OmniTrialOption] = old
		} else {
			delete(common.OptionMap, OmniTrialOption)
		}
	})
}

func TestOmniTrialRequestAndAccountBounds(t *testing.T) {
	const uid = 2081
	trialFixture(t, uid)
	a, err := OmniRiskReserveV2(uid, "logical", 1, 5000000, "quote")
	require.NoError(t, err)
	require.NoError(t, OmniRiskFinishV2(a.ID, 0, false, false))
	_, err = OmniRiskReserveV2(uid, "logical", 2, 1, "quote")
	var rejection *OmniTrialRejection
	require.ErrorAs(t, err, &rejection)
	require.Equal(t, "trial_request_budget", rejection.Code)
	b, err := OmniRiskReserveV2(uid, "other", 2, 5000000, "quote")
	require.NoError(t, err)
	_, err = OmniRiskReserveV2(uid, "third", 2, 1, "quote")
	require.ErrorAs(t, err, &rejection)
	require.Equal(t, "trial_account_budget", rejection.Code)
	require.Zero(t, rejection.RemainingMicros)
	require.NoError(t, OmniRiskFinishV2(b.ID, 0, false, true))
	_, err = OmniRiskReserveV2(uid, "third", 2, 10, "quote")
	require.NoError(t, err)
	require.NoError(t, OmniRiskAdmission(DB, uid))
	status, err := OmniTrialStatusFor(uid)
	require.NoError(t, err)
	require.True(t, status.Active)
	require.EqualValues(t, 4999990, status.RemainingMicros)
}

func TestOmniTrialConcurrencyIsAtomic(t *testing.T) {
	const uid = 2082
	trialFixture(t, uid)
	var wins, denied atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 15; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			row, err := OmniRiskReserveV2(uid, fmt.Sprintf("parallel-%d", i), 1, 500000, "quote")
			var rejection *OmniTrialRejection
			switch {
			case err == nil && row != nil:
				wins.Add(1)
			case errors.As(err, &rejection) && rejection.Code == "trial_concurrency_limit":
				denied.Add(1)
			default:
				t.Errorf("unexpected reservation result %v", err)
			}
		}(i)
	}
	wg.Wait()
	require.EqualValues(t, 10, wins.Load())
	require.EqualValues(t, 5, denied.Load())
	require.NoError(t, OmniRiskAdmission(DB, uid))
}

func TestOmniTrialRestoredBudgetRemainsEligible(t *testing.T) {
	const uid = 2083
	trialFixture(t, uid)
	require.NoError(t, OmniRiskBudgetPause(uid, "quote_unavailable"))
	control, err := OmniRiskReadControl(DB, uid)
	require.NoError(t, err)
	require.NoError(t, OmniRiskRestoreV2(uid, 1, control.Version, "budget-only trial test", false))
	status, err := OmniTrialStatusFor(uid)
	require.NoError(t, err)
	require.True(t, status.Active)
	_, err = OmniRiskReserveV2(uid, "after-restore", 1, 5000001, "quote")
	var rejection *OmniTrialRejection
	require.ErrorAs(t, err, &rejection)
	require.Equal(t, "trial_request_budget", rejection.Code)
}
