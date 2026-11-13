package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

// A purchased term keeps its immutable, approved rights even if sales close.
type OmniCommerceTerm struct {
	NotBefore    int64  `json:"not_before"`
	PriceVersion string `json:"price_version"`
	ID           string `json:"id" gorm:"primaryKey;size:160"`
	UserID       int    `json:"user_id" gorm:"index"`
	PlanKey      string `json:"plan_key"`
	Months       int    `json:"months"`
	Cents        int64  `json:"cents"`
	ProfileJSON  string `json:"profile_snapshot" gorm:"type:text"`
	FiveEnabled  bool   `json:"five_enabled"`
	FiveLimit    int64  `json:"five_limit"`
	WeekLimit    int64  `json:"week_limit"`
	ResetCents   int64  `json:"reset_cents"`
	CreatedAt    int64  `json:"created_at"`
}

func (s OmniSubscription) FiveEnabled() bool { return s.EntitlementVersion < 3 || s.FiveWindowEnabled }
func (s OmniSubscription) available() int64 {
	if s.ExitApplicationID != "" {
		return 0
	}
	q := s.WeekLimit - s.WeekUsed
	if s.FiveEnabled() {
		q = min(q, s.FiveLimit-s.FiveUsed)
	}
	return max(int64(0), q)
}
func (s OmniSubscription) paused() bool {
	return s.ExitApplicationID != "" || s.WeekPaused || s.FiveEnabled() && s.FivePaused
}

func (s *OmniSubscription) advanceCommerce(now int64) error {
	if s.ExitApplicationID != "" {
		return nil
	}
	var queue []OmniCommerceTerm
	if s.CommerceQueue != "" && json.Unmarshal([]byte(s.CommerceQueue), &queue) != nil {
		return errors.New("invalid purchased term queue")
	}
	for s.End <= now && s.PendingMonths == 0 && len(queue) > 0 {
		term := queue[0]
		if term.NotBefore > now {
			break
		}
		queue = queue[1:]
		start := s.End
		start = max(start, term.NotBefore)
		if start <= 0 {
			start = now
		}
		var p OmniCommerceProfile
		if json.Unmarshal([]byte(term.ProfileJSON), &p) != nil {
			return errors.New("invalid purchased profile")
		}
		raw, _ := json.Marshal(queue)
		*s = OmniSubscription{UserID: s.UserID, PlanKey: term.PlanKey, Start: start, End: OmniCalendarEnd(start, term.Months), MonthsPurchased: term.Months, LastPurchaseMonths: term.Months, FxMicros: p.FXMicros, FiveLimit: term.FiveLimit, WeekLimit: term.WeekLimit, FiveWindowEnabled: term.FiveEnabled, EntitlementVersion: 3, QuotaVersion: OmniQuotaVersion, BillingVersion: 2, CommerceProfile: term.ProfileJSON, CommerceQueue: string(raw), ResetPriceCents: term.ResetCents}
	}
	return nil
}

func OmniCommercePurchase(uid int, planKey string, months int, profileID, requestKey string, expected ...int64) error {
	if len(requestKey) < 16 || len(requestKey) > 80 {
		return errors.New("invalid purchase key")
	}
	defer invalidateUserCache(uid)
	key := fmt.Sprintf("v3-sub:%d:%s", uid, requestKey)
	return DB.Transaction(func(tx *gorm.DB) error {
		if _, err := omniAccount(tx, uid); err != nil {
			return err
		}
		if err := OmniRiskBusinessMutation(tx, uid, expected...); err != nil {
			return err
		}
		var prior OmniCommerceTerm
		if err := tx.First(&prior, "id = ?", key).Error; err == nil {
			var p OmniCommerceProfile
			_ = json.Unmarshal([]byte(prior.ProfileJSON), &p)
			if prior.PlanKey != planKey || prior.Months != months || p.ID != profileID {
				return errors.New("purchase key conflict")
			}
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		p, err := OmniCommerceSaleProfile(tx)
		if err != nil {
			return err
		}
		if p.ID != profileID {
			return errors.New("核价版本已变化，请重新确认")
		}
		plans, err := p.Plans()
		if err != nil {
			return err
		}
		var plan OmniCommercePlan
		for _, r := range plans {
			if r.Key == planKey {
				plan = r
			}
		}
		cents, ok := plan.Prices[months]
		if !ok {
			return errors.New("未知套餐或期限")
		}
		debit, err := omniCommerceWallet(tx, uid, cents, p.FXMicros)
		if err != nil {
			return err
		}
		five, err := omniCommerceQuota(plan.FiveCents, p.FXMicros)
		if err != nil {
			return err
		}
		week, err := omniCommerceQuota(plan.WeekCents, p.FXMicros)
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(p)
		term := OmniCommerceTerm{PriceVersion: p.PriceVersion, ID: key, UserID: uid, PlanKey: plan.Key, Months: months, Cents: cents, ProfileJSON: string(raw), FiveEnabled: plan.FiveEnabled, FiveLimit: five, WeekLimit: week, ResetCents: plan.ResetCents, CreatedAt: time.Now().Unix()}
		var s OmniSubscription
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&s, "user_id = ?", uid).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if s.UserID == 0 {
			s.UserID = uid
		}
		now := time.Now().Unix()
		if s.End <= now {
			var legacyEnd int64
			if err = tx.Model(&UserSubscription{}).Where("user_id = ? AND status = ? AND end_time > ?", uid, "active", now).Select("COALESCE(MAX(end_time),0)").Scan(&legacyEnd).Error; err != nil {
				return err
			}
			if legacyEnd > now {
				s = OmniSubscription{UserID: uid, PlanKey: "legacy_native", Start: now, End: legacyEnd, QuotaVersion: OmniQuotaVersion, BillingVersion: 2, NativeLegacyAwait: true}
			}
		}
		if s.Start > 0 {
			if err = s.advance(now); err != nil {
				return err
			}
		}
		var queue []OmniCommerceTerm
		if s.CommerceQueue != "" && json.Unmarshal([]byte(s.CommerceQueue), &queue) != nil {
			return errors.New("invalid term queue")
		}
		if s.ExitApplicationID != "" {
			return errors.New("请先处理迁移退出申请")
		}
		if s.MigrationID == "" && s.End > now && s.EntitlementVersion == 3 && s.PlanKey == plan.Key && s.CommerceProfile == term.ProfileJSON && len(queue) == 0 && s.PendingMonths == 0 {
			s.MonthsPurchased += months
			s.End = OmniCalendarEnd(s.Start, s.MonthsPurchased)
			s.LastPurchaseMonths = months
		} else {
			term.NotBefore = s.End
			for _, queued := range queue {
				term.NotBefore = OmniCalendarEnd(max(term.NotBefore, queued.NotBefore), queued.Months)
			}
			queue = append(queue, term)
			q, _ := json.Marshal(queue)
			s.CommerceQueue = string(q)
			if s.End <= now {
				busy, e := omniBusy(tx, uid)
				if e != nil {
					return e
				}
				if busy {
					return errors.New("已有请求正在结算，请稍后购买")
				}
				if err = omniPauseLegacy(tx, uid, now); err != nil {
					return err
				}
				s.End = now
				if err = s.advanceCommerce(now); err != nil {
					return err
				}
			}
		}
		if err = tx.Save(&s).Error; err != nil {
			return err
		}
		if err = tx.Create(&term).Error; err != nil {
			return err
		}
		if err = tx.Create(&OmniOrder{ID: "V3-" + omniHash(key)[:24], PriceVersion: p.PriceVersion, UserID: uid, PlanKey: plan.Key, Months: months, Cents: cents, State: "fulfilled", Receipt: key, PaymentMethod: "wallet", CreatedAt: now}).Error; err != nil {
			return err
		}
		return tx.Create(&OmniLedger{Key: key, UserID: uid, Kind: "subscription:" + plan.Key, Cents: cents, Quota: debit, FxMicros: p.FXMicros, CreatedAt: now}).Error
	})
}

// Quote and execution bind to the current term and windows, so a stale checkout
// cannot reset a replacement subscription or charge an unconfirmed price.
type OmniResetQuote struct {
	Start       int64  `json:"start"`
	FiveEpoch   int64  `json:"five_epoch"`
	WeekEpoch   int64  `json:"week_epoch"`
	FiveEnabled bool   `json:"five_enabled"`
	FiveRestore int64  `json:"five_restore"`
	WeekRestore int64  `json:"week_restore"`
	FiveResetAt int64  `json:"five_reset_at"`
	WeekResetAt int64  `json:"week_reset_at"`
	Cents       int64  `json:"cents"`
	ProfileID   string `json:"profile_id"`
}

func omniResetQuote(s OmniSubscription, p OmniCommerceProfile) (OmniResetQuote, error) {
	if s.ExitApplicationID != "" {
		return OmniResetQuote{}, errors.New("迁移退出审核期间不能重置")
	}
	if s.End <= time.Now().Unix() || s.Start > time.Now().Unix() || s.CommerceProfile == "" {
		return OmniResetQuote{}, errors.New("需有效且已核价的订阅；旧订阅可先自愿转换模型范围")
	}
	var scope OmniCommerceProfile
	if json.Unmarshal([]byte(s.CommerceProfile), &scope) != nil || scope.ID != p.ID {
		return OmniResetQuote{}, errors.New("该订阅模型范围需重新核验成本")
	}
	// Legacy conversions retain their existing caps, so price their full rights.
	weekCents, err := omniIntRatio(s.WeekLimit, p.FXMicros, int64(common.QuotaPerUnit)*10000, true)
	if err != nil {
		return OmniResetQuote{}, err
	}
	cost, err := omniIntRatio(weekCents, p.WorstCostPPM, 1000000, true)
	if err != nil {
		return OmniResetQuote{}, err
	}
	price, err := omniIntRatio(cost+p.FixedFeeCents, 1000000, 800000-p.PaymentFeePPM-p.RiskReservePPM, true)
	if err != nil {
		return OmniResetQuote{}, err
	}
	return OmniResetQuote{Start: s.Start, FiveEpoch: s.FiveEpoch, WeekEpoch: s.WeekEpoch, FiveEnabled: s.FiveEnabled(), FiveRestore: func() int64 {
		if s.FiveEnabled() {
			return min(s.FiveUsed, s.FiveLimit)
		}
		return 0
	}(), WeekRestore: min(s.WeekUsed, s.WeekLimit), FiveResetAt: s.Start + (s.FiveEpoch+1)*18000, WeekResetAt: s.Start + (s.WeekEpoch+1)*604800, Cents: price, ProfileID: p.ID}, nil
}
func OmniCommerceResetQuote(uid int) (OmniResetQuote, error) {
	if err := OmniReconcile(uid); err != nil {
		return OmniResetQuote{}, err
	}
	p, err := OmniCommerceSaleProfile(DB)
	if err != nil {
		return OmniResetQuote{}, err
	}
	var s OmniSubscription
	if err = DB.First(&s, "user_id = ?", uid).Error; err != nil {
		return OmniResetQuote{}, err
	}
	return omniResetQuote(s, p)
}
func OmniCommerceReset(uid int, requestKey string, quote OmniResetQuote, expected ...int64) error {
	if len(requestKey) < 16 || len(requestKey) > 80 {
		return errors.New("invalid reset key")
	}
	defer invalidateUserCache(uid)
	key := fmt.Sprintf("v3-reset:%d:%s", uid, requestKey)
	payload, _ := json.Marshal(quote)
	return DB.Transaction(func(tx *gorm.DB) error {
		if _, err := omniAccount(tx, uid); err != nil {
			return err
		}
		if err := OmniRiskBusinessMutation(tx, uid, expected...); err != nil {
			return err
		}
		var prior OmniLedger
		if err := tx.First(&prior, "key = ?", key).Error; err == nil {
			if prior.AuditJSON != string(payload) {
				return errors.New("reset key conflict")
			}
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		p, err := OmniCommerceSaleProfile(tx)
		if err != nil {
			return err
		}
		var s OmniSubscription
		if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&s, "user_id = ?", uid).Error; err != nil {
			return err
		}
		if err = s.advance(time.Now().Unix()); err != nil {
			return err
		}
		current, err := omniResetQuote(s, p)
		if err != nil {
			return err
		}
		if current != quote {
			return errors.New("恢复量、价格或周期已变化，请重新确认")
		}
		busy, err := omniBusy(tx, uid)
		if err != nil {
			return err
		}
		if busy {
			return errors.New("有进行中请求，无法重置")
		}
		if current.FiveRestore == 0 && current.WeekRestore == 0 {
			return errors.New("当前额度无需重置")
		}
		debit, err := omniCommerceWallet(tx, uid, current.Cents, p.FXMicros)
		if err != nil {
			return err
		}
		if s.FiveEnabled() {
			s.FiveUsed = 0
			s.FivePaused = false
		}
		s.WeekUsed = 0
		s.WeekPaused = false
		if err = omniCustomerResetWindows(tx, s, s.FiveEnabled(), true); err != nil {
			return err
		}
		if err = tx.Save(&s).Error; err != nil {
			return err
		}
		return tx.Create(&OmniLedger{Key: key, UserID: uid, Kind: "subscription:paid_reset", Cents: current.Cents, Quota: debit, FxMicros: p.FXMicros, AuditJSON: string(payload), CreatedAt: time.Now().Unix()}).Error
	})
}
func OmniCommerceConvert(uid int, profileID string, expected ...int64) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		if _, err := omniAccount(tx, uid); err != nil {
			return err
		}
		if err := OmniRiskBusinessMutation(tx, uid, expected...); err != nil {
			return err
		}
		p, err := OmniCommerceSaleProfile(tx)
		if err != nil {
			return err
		}
		if p.ID != profileID {
			return errors.New("核价版本已变化")
		}
		var s OmniSubscription
		if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&s, "user_id = ?", uid).Error; err != nil {
			return err
		}
		if err = s.advance(time.Now().Unix()); err != nil {
			return err
		}
		if s.End <= time.Now().Unix() {
			return errors.New("订阅已到期")
		}
		if s.CommerceProfile != "" {
			var old OmniCommerceProfile
			_ = json.Unmarshal([]byte(s.CommerceProfile), &old)
			if old.ID == p.ID {
				return nil
			}
			return errors.New("本有效期内已转换模型范围")
		}
		busy, err := omniBusy(tx, uid)
		if err != nil {
			return err
		}
		if busy {
			return errors.New("请等待进行中请求结算")
		}
		raw, _ := json.Marshal(p)
		s.CommerceProfile = string(raw)
		if err = tx.Save(&s).Error; err != nil {
			return err
		}
		return tx.Create(&OmniLedger{Key: fmt.Sprintf("scope-convert:%d:%d", uid, s.Start), UserID: uid, Kind: "subscription:scope_convert", AuditJSON: profileID, CreatedAt: time.Now().Unix()}).Error
	})
}

func omniCommerceQuota(cents, fx int64) (int64, error) {
	return omniIntRatio(cents, int64(common.QuotaPerUnit)*10000, fx, false)
}

func OmniCommerceQueueView(s OmniSubscription) []map[string]any {
	var terms []OmniCommerceTerm
	_ = json.Unmarshal([]byte(s.CommerceQueue), &terms)
	out := []map[string]any{}
	anchor := s.End
	if s.PendingMonths > 0 {
		anchor = OmniCalendarEnd(anchor, s.PendingMonths)
	}
	for _, term := range terms {
		anchor = max(anchor, term.NotBefore)
		end := OmniCalendarEnd(anchor, term.Months)
		out = append(out, map[string]any{"plan_key": term.PlanKey, "months": term.Months, "cents": term.Cents, "start": anchor, "end": end})
		anchor = end
	}
	return out
}
