package model

import (
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
	"time"
)

// Topups are separate from subscription purchase orders. Money, identity and
// conversion are immutable once persisted, including during query recovery.
type OmniTopup struct {
	PaidAt          int64   `json:"paid_at"`
	PaidAtSource    string  `json:"paid_at_source"`
	CampaignID      string  `json:"campaign_id"`
	CampaignBonus   int64   `json:"campaign_bonus"`
	PromotionState  string  `json:"promotion_state"`
	ID              string  `json:"id" gorm:"primaryKey;size:64"`
	UserID          int     `json:"user_id" gorm:"uniqueIndex:topup_request"`
	RequestID       string  `json:"-" gorm:"uniqueIndex:topup_request;size:80"`
	AID             string  `json:"-" gorm:"size:32"`
	AOID            *string `json:"-" gorm:"uniqueIndex;size:100"`
	Method          string  `json:"method"`
	PaymentFlow     string  `json:"payment_flow,omitempty" gorm:"size:32"`
	CashierForm     string  `json:"-" gorm:"type:text"`
	Cents           int64   `json:"cents"`
	USD             string  `json:"usd"`
	Quota           int64   `json:"quota"`
	FxRate          string  `json:"fx_rate"`
	FxSource        string  `json:"fx_source"`
	FxAsOf          string  `json:"fx_as_of"`
	FxNote          string  `json:"fx_note"`
	State           string  `json:"state" gorm:"index"`
	QR              string  `json:"qr,omitempty" gorm:"type:text"`
	AttemptedAt     int64   `json:"-"`
	CreatedAt       int64   `json:"created_at" gorm:"index"`
	ExpiresAt       int64   `json:"expires_at"`
	CreditedAt      int64   `json:"credited_at"`
	LastCheckAt     int64   `json:"last_check_at"`
	ProviderStatus  string  `json:"provider_status"`
	CreateStatus    string  `json:"create_status,omitempty" gorm:"size:64"`
	CreateErrorCode string  `json:"create_error_code,omitempty" gorm:"size:64"`
	ErrorCode       string  `json:"error_code,omitempty"`
	Bonus           int64   `json:"bonus"`
}

func OmniTopupForUser(id string, uid int) (OmniTopup, error) {
	var o OmniTopup
	err := DB.Where("id = ? AND user_id = ?", id, uid).First(&o).Error
	return o, err
}

// Successful query2 results are only admissible for our previously attempted
// signed request. Signed notifications additionally bind amount and AOID.
func OmniFulfillTopup(id, aid, aoid string, paidCents int64, notification bool, evidence ...OmniPaymentEvidence) (OmniTopup, error) {
	var out OmniTopup
	var conflict error
	err := DB.Session(&gorm.Session{Logger: logger.Discard}).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&out, "id = ?", id).Error; err != nil {
			return err
		}
		if out.AID != aid || out.AttemptedAt == 0 || out.Cents <= 0 || out.Quota <= 0 {
			return errors.New("payment order conflict")
		}
		if notification && (paidCents != out.Cents || aoid == "" || len(aoid) > 100 || (out.AOID != nil && *out.AOID != aoid)) {
			conflict = errors.New("payment receipt conflict")
			if out.State != "credited" {
				out.State = "review"
				out.ErrorCode = "notify_conflict"
				return tx.Save(&out).Error
			}
			return nil
		}
		if !notification && out.ErrorCode == "notify_conflict" {
			return errors.New("payment requires manual review")
		}
		if notification && out.AOID == nil {
			out.AOID = &aoid
		}
		receipt, err := omniCreditTx(tx, "xorpay:"+out.AID+":"+out.ID, out.UserID, out.Cents, out.Quota, GetOmniPolicy().MembershipEnabled, evidence...)
		if err != nil {
			return err
		}
		// Audit data is server-derived, never taken from unsigned notify metadata.
		if err = tx.Model(&OmniLedger{}).Where("key = ?", receipt.Key).Updates(map[string]any{"fx_source": out.FxSource, "fx_as_of": out.FxAsOf, "audit_json": "{\"provider\":\"xorpay\",\"fee_basis\":\"gross\"}"}).Error; err != nil {
			return err
		}
		out.State = "credited"
		if out.CreditedAt == 0 {
			out.CreditedAt = time.Now().Unix()
		}
		out.PaidAt = receipt.PaidAt
		out.PaidAtSource = receipt.PaidAtSource
		out.CampaignID = receipt.CampaignID
		out.CampaignBonus = receipt.CampaignBonus
		out.PromotionState = receipt.PromotionState
		out.Bonus = receipt.Bonus
		out.ErrorCode = ""
		return tx.Save(&out).Error
	})
	if err == nil && conflict != nil {
		return out, conflict
	}
	if err == nil {
		invalidateUserCache(out.UserID)
	}
	return out, err
}
