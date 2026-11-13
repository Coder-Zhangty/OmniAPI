package model

import (
	"github.com/QuantumNous/new-api/common"
	"time"
)

// Service registers the callback before main starts maintenance. Keeping the
// hook here avoids a model -> service import cycle.
var OmniTopupReconcile func(string) error

func StartOmniTopupMaintenance() {
	if !common.IsMasterNode {
		return
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		if DB == nil || OmniTopupReconcile == nil {
			continue
		}
		var rows []OmniTopup
		// Oldest check first prevents a busy queue from starving older payments.
		if DB.Where("attempted_at > 0 AND last_check_at < ? AND ((state <> ? AND created_at >= ?) OR (state = ? AND COALESCE(paid_at,0)=0 AND credited_at >= ? AND created_at < ?))", time.Now().Add(-time.Minute).Unix(), "credited", time.Now().Add(-24*time.Hour).Unix(), "credited", GetOmniPolicy().TopupPromotion.StartsAt, GetOmniPolicy().TopupPromotion.EndsAt).Order("last_check_at ASC").Limit(100).Find(&rows).Error != nil {
			continue
		}
		for _, o := range rows {
			_ = OmniTopupReconcile(o.ID)
		}
	}
}
