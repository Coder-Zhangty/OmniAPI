package model

import (
	"github.com/stretchr/testify/require"
	"testing"
)

// Existing monthly welfare permits at most one weekly and two five-hour
// resets. They cost quota too, even when no extra wallet payment is collected.
func TestOmniCommerceFreeBenefitsIncludedInCost(t *testing.T) {
	p := commerceFixture(t)
	plans, err := p.Plans()
	require.NoError(t, err)
	for _, plan := range plans {
		for _, months := range []int{1, 3, 6, 12} {
			grant, e := p.GrantCents(plan.Prices[months])
			require.NoError(t, e)
			natural := plan.WeekCents * map[int]int64{1: 5, 3: 14, 6: 27, 12: 53}[months]
			free := plan.WeekCents * int64(months)
			if plan.FiveEnabled {
				free += plan.FiveCents * 2 * int64(months)
			}
			require.LessOrEqual(t, natural+free, grant, "%s/%d months must cover the existing free reset ceiling", plan.Key, months)
		}
	}
}
