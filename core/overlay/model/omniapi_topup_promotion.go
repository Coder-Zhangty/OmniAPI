package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// Payment evidence is supplied only by a verified provider response or an
// authenticated operator. Missing evidence must never delay principal credit.
type OmniPaymentEvidence struct {
	PaidAt int64  `json:"paid_at"`
	Source string `json:"paid_at_source"`
}
type OmniTopupPromotion struct {
	ID       string `json:"id"`
	Enabled  bool   `json:"enabled"`
	StartsAt int64  `json:"starts_at"`
	EndsAt   int64  `json:"ends_at"`
	BonusBPS int64  `json:"bonus_bps"`
}

func (p OmniTopupPromotion) Valid() bool {
	return p.Enabled && p.ID != "" && len(p.ID) <= 64 && p.StartsAt > 0 && p.EndsAt > p.StartsAt && p.BonusBPS > 0 && p.BonusBPS <= 10000
}
func (p OmniTopupPromotion) BonusFor(quota, paidAt int64) int64 {
	if !p.Valid() || quota <= 0 || paidAt < p.StartsAt || paidAt >= p.EndsAt {
		return 0
	}
	// Exact floor without multiplying a potentially large quota by 1500.
	return quota/10000*p.BonusBPS + quota%10000*p.BonusBPS/10000
}
func (p OmniPolicy) BenefitsEnabled() bool {
	if p.BenefitCardsEnabled != nil {
		return *p.BenefitCardsEnabled
	}
	return p.MembershipEnabled
}
func omniPaymentEvidence(evidence []OmniPaymentEvidence) (OmniPaymentEvidence, error) {
	if len(evidence) == 0 {
		return OmniPaymentEvidence{}, nil
	}
	e := evidence[0]
	if e.PaidAt == 0 && e.Source == "" {
		return e, nil
	}
	if e.PaidAt <= 0 || e.PaidAt > time.Now().Unix()+300 || len(e.Source) > 80 {
		return e, errors.New("invalid verified payment time")
	}
	switch e.Source {
	case "xorpay:signed_notify", "xorpay:authenticated_query", "manual:verified_transaction":
		return e, nil
	}
	return e, errors.New("untrusted payment time source")
}

// Caller holds the account lock. The existing receipt owns idempotency for both
// normal settlement and later campaign backfill. Refunds use the same lock.
func omniSupplementPromotion(tx *gorm.DB, r *OmniLedger, e OmniPaymentEvidence, p OmniTopupPromotion) error {
	if r.Refunded {
		return nil
	}
	if e.PaidAt > 0 {
		if r.PaidAt > 0 && r.PaidAt != e.PaidAt {
			return errors.New("payment time conflicts with frozen evidence")
		}
		r.PaidAt = e.PaidAt
		r.PaidAtSource = e.Source
	}
	if !p.Valid() {
		return tx.Save(r).Error
	}
	if r.CampaignID != "" && r.CampaignID != p.ID {
		return errors.New("receipt belongs to another promotion")
	}
	r.CampaignID = p.ID
	if r.PaidAt == 0 {
		r.PromotionState = "pending_payment_time"
		return tx.Save(r).Error
	}
	totalTarget := p.BonusFor(r.Quota, r.PaidAt)
	expected := max(int64(0), totalTarget-(r.Bonus-r.CampaignBonus))
	if r.CampaignBonus > expected {
		return errors.New("campaign bonus reconciliation conflict")
	}
	delta := expected - r.CampaignBonus
	if delta > 0 {
		if r.Bonus > math.MaxInt64-delta || r.Quota > math.MaxInt64-r.Bonus-delta {
			return errors.New("bonus exceeds integer balance range")
		}
		res := tx.Model(&User{}).Where("id = ? AND quota <= ?", r.UserID, int64(math.MaxInt64)-delta).UpdateColumn("quota", gorm.Expr("quota + ?", delta))
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errors.New("wallet balance exceeds integer range")
		}
		r.Bonus += delta
		r.CampaignBonus = expected
		body := fmt.Sprintf("双十一充值加送15%%已到账：本金 US$%.6f，赠额 US$%.6f，总到账 US$%.6f。", float64(r.Quota)/common.QuotaPerUnit, float64(r.Bonus)/common.QuotaPerUnit, float64(r.Quota+r.Bonus)/common.QuotaPerUnit)
		if err := omniNotice(tx, "promotion:"+p.ID+":"+r.Key, r.UserID, "双十一充值福利已到账", body); err != nil {
			return err
		}
	}
	if r.PaidAt >= p.StartsAt && r.PaidAt < p.EndsAt {
		r.PromotionState = "awarded"
	} else {
		r.PromotionState = "outside_payment_window"
	}
	return tx.Save(r).Error
}

// Read decoration is independent of factual discount_applied and the ledger.
// UID16 has an account-level negotiated rule, including requests capped at the
// ordinary price and historic subscription logs.
func OmniDecorateDiscountLabel(uid int, raw string) string {
	if uid != 16 || !GetOmniUserDiscounts().Users["16"].Enabled {
		return raw
	}
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &m) != nil || m == nil {
		m = map[string]json.RawMessage{}
	}
	m["price_discount_rule_enabled"] = json.RawMessage("true")
	m["price_discount_label"] = json.RawMessage(`"价格优惠"`)
	b, _ := json.Marshal(m)
	return string(b)
}
