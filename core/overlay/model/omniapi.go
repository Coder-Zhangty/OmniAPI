package model

// OmniAPI v2 financial records live in the gateway database. Integer quota and
// cents are used throughout; row locks serialize reservations and reset cards.
import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrOmniInsufficient = errors.New("subscription quota insufficient")
var ErrOmniAllowancePending = errors.New("subscription allowance is reserved by an unfinished request")

type OmniPlan struct {
	Key           string `json:"key"`
	Title         string `json:"title"`
	MonthlyCents  int64  `json:"monthly_cents"`
	AnnualCents   int64  `json:"annual_cents"`
	FiveHourCents int64  `json:"five_hour_cents"`
	WeekCents     int64  `json:"week_cents"`
}

// API retail is twice procurement; monthly subscription equivalent targets 1.1x.
// Week cents = round(monthly cents * 14 / 33), five-hour cents = round(monthly cents * 7 / 66).
// Both windows retain their original periods; monthly comparison uses 30 days.
const OmniQuotaVersion = 2

var OmniPlans = []OmniPlan{{"starter", "体验", 1900, 18240, 202, 806}, {"standard", "标准", 3900, 37440, 414, 1655}, {"pro", "专业", 9900, 95040, 1050, 4200}}

type OmniModelPolicy struct {
	Eligible           bool    `json:"eligible"`
	WorstCostRatio     float64 `json:"worst_cost_ratio"`
	VerifiedAt         string  `json:"verified_at"`
	ReserveInputFactor float64 `json:"reserve_input_factor"`
}
type OmniPayment struct {
	Mode    string `json:"mode"`
	Enabled bool   `json:"enabled"`
	Contact string `json:"contact"`
	QRPath  string `json:"qr_path"`
}
type OmniPolicy struct {
	BenefitCardsEnabled    *bool                      `json:"benefit_cards_enabled,omitempty"`
	TopupPromotion         OmniTopupPromotion         `json:"topup_promotion"`
	CustomerOveragePolicy  string                     `json:"customer_overage_policy,omitempty"`
	CustomerOverageUserIDs []int                      `json:"customer_overage_user_ids,omitempty"`
	ModelScope             string                     `json:"model_scope,omitempty"`
	Payment                OmniPayment                `json:"payment"`
	RolloutUserIDs         []int                      `json:"rollout_user_ids,omitempty"`
	SalesEnabled           bool                       `json:"sales_enabled"`
	BillingEnabled         bool                       `json:"billing_enabled"`
	ReferralsEnabled       bool                       `json:"referrals_enabled"`
	MembershipEnabled      bool                       `json:"membership_enabled"`
	ReferralStartID        int                        `json:"referral_start_id"`
	AnnouncementsEnabled   bool                       `json:"announcements_enabled"`
	Models                 map[string]OmniModelPolicy `json:"models"`
}

func GetOmniPolicy() OmniPolicy {
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap["OmniAPICommercial"]
	common.OptionMapRWMutex.RUnlock()
	var p OmniPolicy
	if json.Unmarshal([]byte(raw), &p) != nil {
		return OmniPolicy{}
	}
	return p
}
func OmniEligible(name string) bool {
	p := GetOmniPolicy()
	if p.ModelScope == "all_available" {
		status, _, _ := OmniPriceAvailability(name)
		return p.BillingEnabled && status == "available"
	}
	m, ok := p.Models[name]
	return p.BillingEnabled && ok && m.Eligible && m.VerifiedAt != "" && m.WorstCostRatio > 0 && m.WorstCostRatio <= 0.4
}
func OmniUserInRollout(uid int) bool {
	ids := GetOmniPolicy().RolloutUserIDs
	if len(ids) == 0 {
		return true
	}
	for _, id := range ids {
		if id == uid {
			return true
		}
	}
	return false
}
func OmniSalesReady() bool {
	p := GetOmniPolicy()
	if !p.SalesEnabled || !p.BillingEnabled {
		return false
	}
	if p.ModelScope == "all_available" {
		return p.Payment.Enabled && (p.Payment.Mode == "wallet_balance" || (p.Payment.Contact != "" && p.Payment.QRPath != "")) && len(GetOmniPrices().Models) > 0
	}
	for name := range p.Models {
		if OmniEligible(name) {
			return true
		}
	}
	return false
}
func OmniReserveInputFactor(name string) float64 {
	m := GetOmniPolicy().Models[name]
	if m.ReserveInputFactor >= 1 && m.ReserveInputFactor <= 10 {
		return m.ReserveInputFactor
	}
	return 1
}
func omniPlan(key string) (OmniPlan, error) {
	for _, p := range OmniPlans {
		if p.Key == key {
			return p, nil
		}
	}
	return OmniPlan{}, errors.New("unknown plan")
}
func OmniQuota(cents, fxMicros int64) int64 {
	if cents <= 0 || cents > 10000000 || fxMicros < 1000000 || fxMicros > 20000000 {
		return 0
	}
	// USD truncated to six decimals, then quota floored: same as topup_requests.
	usdMicros := cents * 10000 * 1000000 / fxMicros
	return int64(math.Floor(float64(usdMicros) * common.QuotaPerUnit / 1000000))
}
func OmniCalendarEnd(start int64, months int) int64 {
	zone := time.FixedZone("Asia/Shanghai", 8*3600)
	t := time.Unix(start, 0).In(zone)
	y, m, _ := t.Date()
	first := time.Date(y, m+time.Month(months), 1, t.Hour(), t.Minute(), t.Second(), 0, zone)
	last := first.AddDate(0, 1, -1).Day()
	day := t.Day()
	if day > last {
		day = last
	}
	return time.Date(first.Year(), first.Month(), day, t.Hour(), t.Minute(), t.Second(), 0, zone).Unix()
}

type OmniAccount struct {
	UserID           int   `json:"user_id" gorm:"primaryKey;autoIncrement:false"`
	NetRechargeCents int64 `json:"net_recharge_cents"`
	WalletOverflow   bool  `json:"wallet_overflow" gorm:"default:true"`
	OverflowNotified bool  `json:"overflow_notified"`
}
type OmniSubscription struct {
	MigrationID        string `json:"migration_id"`
	ExitApplicationID  string `json:"exit_application_id"`
	NativeLegacyAwait  bool   `json:"native_legacy_await" gorm:"not null;default:false"`
	EntitlementVersion int    `json:"entitlement_version" gorm:"not null;default:0"`
	FiveWindowEnabled  bool   `json:"five_window_enabled" gorm:"not null;default:false"`
	CommerceProfile    string `json:"-" gorm:"type:text"`
	CommerceQueue      string `json:"-" gorm:"type:text"`
	ResetPriceCents    int64  `json:"reset_price_cents"`

	FivePaused            bool   `json:"-" gorm:"not null;default:false"`
	WeekPaused            bool   `json:"-" gorm:"not null;default:false"`
	LastPurchaseMonths    int    `json:"last_purchase_months"`
	ActivationPending     bool   `json:"activation_pending"`
	UserID                int    `json:"user_id" gorm:"primaryKey;autoIncrement:false"`
	PlanKey               string `json:"plan_key"`
	Start                 int64  `json:"start"`
	End                   int64  `json:"end"`
	MonthsPurchased       int    `json:"months_purchased"`
	FxMicros              int64  `json:"fx_micros"`
	FiveLimit             int64  `json:"five_limit"`
	WeekLimit             int64  `json:"week_limit"`
	FiveUsed              int64  `json:"five_used"`
	WeekUsed              int64  `json:"week_used"`
	FiveEpoch             int64  `json:"five_epoch"`
	WeekEpoch             int64  `json:"week_epoch"`
	PendingPlan           string `json:"pending_plan"`
	PendingMonths         int    `json:"pending_months"`
	PendingIncludedModels string `json:"-" gorm:"type:text"`
	PendingFx             int64  `json:"pending_fx"`
	QuotaVersion          int    `json:"quota_version" gorm:"not null;default:0"`
	BillingVersion        int    `json:"billing_version" gorm:"default:2"`
	IncludedModels        string `json:"-" gorm:"type:text"`
}
type OmniReservation struct {
	BillingPolicy        string `json:"billing_policy" gorm:"size:40;not null;default:''"`
	PayerID              int    `json:"-"`
	AllowanceActual      int64  `json:"allowance_actual"`
	WalletActual         int64  `json:"wallet_actual"`
	WalletCNYMicros      int64  `json:"wallet_cny_micros"`
	BillingFXMicros      int64  `json:"billing_fx_micros"`
	BillingPriceVersion  string `json:"billing_price_version" gorm:"size:160"`
	BillingPriceSnapshot string `json:"-" gorm:"type:text"`
	BillingTokenID       int    `json:"-"`
	BillingBound         bool   `json:"-" gorm:"not null;default:false"`
	AllowanceFiveEnabled bool   `json:"-"`
	CommerceProfile      string `json:"-" gorm:"type:text"`
	SharedPoolID         string `json:"-" gorm:"size:64"`
	SharedTokenID        int    `json:"-"`

	FlexibleAllowance bool   `json:"-" gorm:"not null;default:false"`
	EstimatedQuota    int64  `json:"-" gorm:"not null;default:0"`
	PlatformQuota     int64  `json:"-" gorm:"not null;default:0"`
	RequestID         string `json:"request_id" gorm:"primaryKey;size:128"`
	UserID            int    `json:"user_id" gorm:"index"`
	Source            string `json:"source"`
	SubscriptionStart int64  `json:"subscription_start"`
	FiveEpoch         int64  `json:"five_epoch"`
	WeekEpoch         int64  `json:"week_epoch"`
	Reserved          int64  `json:"reserved"`
	Actual            int64  `json:"actual"`
	State             string `json:"state" gorm:"index"`
	CreatedAt         int64  `json:"created_at"`
}
type OmniLedger struct {
	PaidAt         int64  `json:"paid_at"`
	PaidAtSource   string `json:"paid_at_source"`
	CampaignID     string `json:"campaign_id"`
	CampaignBonus  int64  `json:"campaign_bonus"`
	PromotionState string `json:"promotion_state"`
	AuditJSON      string `json:"-" gorm:"type:text"`
	Key            string `json:"key" gorm:"primaryKey;size:160"`
	UserID         int    `json:"user_id" gorm:"index"`
	Kind           string `json:"kind"`
	Cents          int64  `json:"cents"`
	FxMicros       int64  `json:"fx_micros"`
	FxSource       string `json:"fx_source" gorm:"size:256"`
	FxAsOf         string `json:"fx_as_of" gorm:"size:64"`
	Quota          int64  `json:"quota"`
	Bonus          int64  `json:"bonus"`
	Refunded       bool   `json:"refunded"`
	Imported       bool   `json:"imported"`
	CreatedAt      int64  `json:"created_at"`
}
type OmniOrder struct {
	PriceVersion     string `json:"price_version"`
	ID               string `json:"id" gorm:"primaryKey;size:64"`
	UserID           int    `json:"user_id" gorm:"index"`
	PlanKey          string `json:"plan_key"`
	Months           int    `json:"months"`
	Cents            int64  `json:"cents"`
	State            string `json:"state" gorm:"index"`
	Receipt          string `json:"receipt"`
	PaymentReference string `json:"payment_reference"`
	PaymentMethod    string `json:"payment_method"`
	CreatedAt        int64  `json:"created_at"`
}
type OmniCard struct {
	Hash              string `json:"-" gorm:"primaryKey;size:64"`
	ID                string `json:"id" gorm:"uniqueIndex;size:64"`
	UserID            int    `json:"user_id" gorm:"index"`
	PlanKey           string `json:"plan_key"`
	SubscriptionStart int64  `json:"subscription_start"`
	Kind              string `json:"kind"`
	Period            int64  `json:"period"`
	Expires           int64  `json:"expires"`
	State             string `json:"state"`
	CostQuota         int64  `json:"cost_quota"`
	CreatedAt         int64  `json:"created_at"`
}
type OmniNotice struct {
	Key       string `json:"key" gorm:"primaryKey;size:160"`
	UserID    int    `json:"user_id" gorm:"index"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Read      bool   `json:"read"`
	CreatedAt int64  `json:"created_at"`
}

func OmniMigrate() error {
	if err := OmniGiftMigrate(DB); err != nil {
		return err
	}
	if err := OmniEmailMigrate(DB); err != nil {
		return err
	}
	if err := OmniCommerceMigrate(DB); err != nil {
		return err
	}
	if err := DB.AutoMigrate(&OmniTopup{}); err != nil {
		return err
	}
	if err := OmniRiskMigrate(DB); err != nil {
		return err
	}
	if err := DB.AutoMigrate(&OmniDiagnosticAttempt{}); err != nil {
		return err
	}

	return DB.AutoMigrate(&OmniAllowanceWindow{}, &OmniLegacyPause{}, &OmniLegacyFlight{}, &OmniAccount{}, &OmniSubscription{}, &OmniReservation{}, &OmniLedger{}, &OmniOrder{}, &OmniCard{}, &OmniNotice{}, &OmniAttempt{}, &OmniKuaiQuarantine{})
}
func omniAccount(tx *gorm.DB, uid int) (OmniAccount, error) {
	a := OmniAccount{UserID: uid, WalletOverflow: true}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&a).Error; err != nil {
		return a, err
	}
	err := lockForUpdate(tx).First(&a, "user_id = ?", uid).Error
	return a, err
}

// Commit the preference under the same account lock used by request admission.
// Requests admitted after this returns read the committed value directly.
func OmniSetWalletOverflow(uid int, enabled bool) (bool, error) {
	err := DB.Transaction(func(tx *gorm.DB) error {
		a, err := omniAccount(tx, uid)
		if err != nil {
			return err
		}
		return tx.Model(&a).Update("wallet_overflow", enabled).Error
	})
	return enabled, err
}
func OmniLevel(cents int64) (string, int64) {
	if cents >= 100000 {
		return "svip", 5
	}
	if cents >= 20000 {
		return "vip", 3
	}
	return "regular", 0
}
func omniNotice(tx *gorm.DB, key string, uid int, title, body string) error {
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&OmniNotice{Key: key, UserID: uid, Title: title, Body: body, CreatedAt: time.Now().Unix()}).Error
}
func (s *OmniSubscription) advance(now int64) error {
	if err := s.advanceCommerce(now); err != nil {
		return err
	}
	if now < s.Start {
		return nil
	}
	if s.End <= now && s.PendingMonths > 0 {
		p, err := omniPlan(s.PendingPlan)
		if err != nil {
			return err
		}
		start := s.End
		months := s.PendingMonths
		fx := s.PendingFx
		included := s.PendingIncludedModels
		queue := s.CommerceQueue
		*s = OmniSubscription{UserID: s.UserID, PlanKey: p.Key, Start: start, End: OmniCalendarEnd(start, months), LastPurchaseMonths: months, MonthsPurchased: months, FxMicros: fx, FiveLimit: OmniQuota(p.FiveHourCents, fx), WeekLimit: OmniQuota(p.WeekCents, fx), QuotaVersion: OmniQuotaVersion, BillingVersion: 2, IncludedModels: included, CommerceQueue: queue}
	}
	if err := s.advanceCommerce(now); err != nil {
		return err
	}
	if s.Start <= 0 {
		return errors.New("invalid subscription start")
	}
	f := (now - s.Start) / 18000
	w := (now - s.Start) / 604800
	if f != s.FiveEpoch {
		s.FiveEpoch = f
		s.FiveUsed = 0
		s.FivePaused = false
	}
	if w != s.WeekEpoch {
		s.WeekEpoch = w
		s.WeekUsed = 0
		s.WeekPaused = false
	}
	return nil
}
func OmniActive(uid int) (bool, error) {
	common.OptionMapRWMutex.RLock()
	configured := common.OptionMap["OmniAPICommercial"] != ""
	common.OptionMapRWMutex.RUnlock()
	if !configured {
		return false, nil
	}
	if err := OmniReconcile(uid); err != nil {
		return false, err
	}
	var activation OmniSubscription
	if DB.First(&activation, "user_id = ?", uid).Error == nil && activation.ActivationPending {
		return false, errors.New("订阅生效处理中，请稍后重试")
	}

	now := time.Now().Unix()
	var legacy int64
	if err := DB.Model(&UserSubscription{}).Where("user_id = ? AND status = ? AND end_time > ?", uid, "active", now).Count(&legacy).Error; err != nil {
		return false, err
	}
	if legacy > 0 {
		return false, nil
	}
	if GetOmniPolicy().BillingEnabled && OmniUserInRollout(uid) {
		return true, nil
	}
	var n int64
	err := DB.Model(&OmniSubscription{}).Where("user_id = ? AND start <= ? AND (\"end\" > ? OR pending_months > 0)", uid, now, now).Count(&n).Error
	return n > 0, err
}
func OmniReserve(uid int, requestID, modelName string, amount int64, snapshots ...map[string]string) (*OmniReservation, error) {
	return omniReserve(uid, requestID, modelName, amount, false, snapshots...)
}

// OmniAdminUsage is a read-only projection. Advancing a local copy avoids
// showing a previous allowance window without changing any financial record.
type OmniAdminUsage struct {
	ServerTime     int64             `json:"server_time"`
	Status         string            `json:"status"`
	PlanKey        string            `json:"plan_key"`
	Start          int64             `json:"start"`
	End            int64             `json:"end"`
	PendingPlan    string            `json:"pending_plan,omitempty"`
	WalletOverflow *bool             `json:"wallet_overflow"`
	Windows        []OmniAdminWindow `json:"windows"`
	FxMicros       int64             `json:"fx_micros"`
	QuotaPerUnit   float64           `json:"quota_per_unit"`
}
type OmniAdminWindow struct {
	Enabled   bool   `json:"enabled"`
	Key       string `json:"key"`
	Limit     int64  `json:"limit"`
	Used      int64  `json:"used"`
	Remaining int64  `json:"remaining"`
	ResetAt   int64  `json:"reset_at"`
	Paused    bool   `json:"paused"`
}

func OmniReadAdminUsage(uid int) (*OmniAdminUsage, error) {
	now := time.Now().Unix()
	out := &OmniAdminUsage{ServerTime: now, Status: "none", Windows: []OmniAdminWindow{}, QuotaPerUnit: common.QuotaPerUnit}
	var account OmniAccount
	if err := DB.First(&account, "user_id = ?", uid).Error; err == nil {
		out.WalletOverflow = &account.WalletOverflow
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	var subscription OmniSubscription
	if err := DB.First(&subscription, "user_id = ?", uid).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return out, nil
		}
		return nil, err
	}
	out.PlanKey = subscription.PlanKey
	out.Start = subscription.Start
	out.End = subscription.End
	out.PendingPlan = subscription.PendingPlan
	if subscription.ActivationPending || subscription.NativeLegacyAwait || subscription.Start > now {
		out.Status = "processing"
		return out, nil
	}
	if err := subscription.advance(now); err != nil {
		return nil, err
	}
	if subscription.End <= now {
		out.Status = "expired"
		return out, nil
	}
	out.Status = "active"
	out.PlanKey = subscription.PlanKey
	out.Start = subscription.Start
	out.End = subscription.End
	out.PendingPlan = subscription.PendingPlan
	out.FxMicros = subscription.FxMicros
	out.Windows = []OmniAdminWindow{
		{Key: "five", Enabled: subscription.FiveEnabled(), Limit: subscription.FiveLimit, Used: subscription.FiveUsed, Remaining: max(int64(0), subscription.FiveLimit-subscription.FiveUsed), ResetAt: subscription.Start + (subscription.FiveEpoch+1)*18000, Paused: subscription.FivePaused},
		{Key: "week", Enabled: true, Limit: subscription.WeekLimit, Used: subscription.WeekUsed, Remaining: max(int64(0), subscription.WeekLimit-subscription.WeekUsed), ResetAt: subscription.Start + (subscription.WeekEpoch+1)*604800, Paused: subscription.WeekPaused},
	}
	if !subscription.FiveEnabled() {
		out.Windows = out.Windows[1:]
	}
	return out, nil
}

// Synchronous subscription requests reserve at most their available allowance;
// a theoretical output maximum is not proof of the eventual actual charge.
func OmniReserveAllowance(uid int, requestID, modelName string, amount int64, snapshots ...map[string]string) (*OmniReservation, error) {
	return omniReserve(uid, requestID, modelName, amount, true, snapshots...)
}

func omniReserve(uid int, requestID, modelName string, amount int64, flexible bool, snapshots ...map[string]string) (*OmniReservation, error) {
	return OmniReservePersonalMode(uid, requestID, modelName, amount, flexible, "auto", snapshots...)
}
func OmniReservePersonalMode(uid int, requestID, modelName string, amount int64, flexible bool, mode string, snapshots ...map[string]string) (*OmniReservation, error) {
	return omniReservePersonalAuthorized(uid, requestID, modelName, amount, flexible, mode, "", 0, snapshots...)
}
func OmniSharedPersonalReserve(uid int, requestID, modelName string, amount int64, poolID string, tokenID int, snapshots ...map[string]string) (*OmniReservation, error) {
	return omniReservePersonalAuthorized(uid, requestID, modelName, amount, true, "off", poolID, tokenID, snapshots...)
}
func omniReservePersonalAuthorized(uid int, requestID, modelName string, amount int64, flexible bool, mode, poolID string, tokenID int, snapshots ...map[string]string) (*OmniReservation, error) {
	defer invalidateUserCache(uid)
	if amount <= 0 || len(requestID) == 0 || len(requestID) > 128 {
		return nil, errors.New("invalid reservation")
	}
	var out OmniReservation
	estimated := amount
	err := omniRequestDB(snapshots...).Transaction(func(tx *gorm.DB) error {
		a, err := omniAccount(tx, uid)
		if err != nil {
			return err
		}
		if err := OmniEmailConsumptionAllowed(tx, uid); err != nil {
			return err
		}
		if mode == "subscription_only" {
			a.WalletOverflow = false
		}
		if mode == "subscription_then_wallet" || mode == "wallet_only" {
			a.WalletOverflow = true
		}
		var sharedProfile string
		if poolID != "" {
			var err error
			mode, sharedProfile, err = omniSharedFallbackTx(tx, uid, tokenID, poolID, modelName)
			if mode == "subscription_only" {
				a.WalletOverflow = false
			}
			if mode == "subscription_then_wallet" || mode == "wallet_only" {
				a.WalletOverflow = true
			}
			if err != nil {
				return err
			}
			var hold OmniSharedHold
			e := tx.First(&hold, "request_id = ?", requestID).Error
			if e == nil {
				return errors.New("request already bound to shared funding")
			}
			if !errors.Is(e, gorm.ErrRecordNotFound) {
				return e
			}
		}
		if err := OmniRiskAdmission(tx, uid); err != nil {
			return err
		}
		var status int
		if err := tx.Model(&User{}).Select("status").Where("id = ?", uid).Scan(&status).Error; err != nil {
			return err
		}
		if status != common.UserStatusEnabled {
			return ErrOmniRiskHold
		}
		err = tx.First(&out, "request_id = ?", requestID).Error
		if err == nil {
			if out.UserID != uid || out.State != "pending" || out.SharedPoolID != poolID || out.SharedTokenID != tokenID {
				return errors.New("reservation already finalized")
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if OmniCustomerBillingFor(uid) {
			return omniCustomerReserveTx(tx, uid, requestID, modelName, estimated, mode, poolID, tokenID, sharedProfile, a, &out)
		}
		var s OmniSubscription
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&s, "user_id = ?", uid).Error
		now := time.Now().Unix()
		active := err == nil
		if active {
			if err = s.advance(now); err != nil {
				return err
			}
			if s.ActivationPending {
				return errors.New("订阅生效处理中，请稍后重试")
			}
			active = s.Start <= now && s.End > now
			if err = tx.Save(&s).Error; err != nil {
				return err
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if active && poolID == "" && mode != "wallet_only" && s.CommerceProfile != "" && !s.Includes(modelName) {
			return errors.New("该模型不在订阅核价范围，不会转扣钱包")
		}
		source := "wallet"
		if mode == "wallet_only" {
			active = false
		}
		subscriptionPaused := active && s.Includes(modelName) && s.paused()
		if subscriptionPaused && !a.WalletOverflow {
			return omniAllowanceQuotaError(s, modelName, estimated)
		}
		if flexible && active && s.Includes(modelName) && !subscriptionPaused {
			available := s.available()
			if available > 0 {
				amount = min(amount, available)
			} else {
				// Pending holds are not actual consumption. Wait for settlement rather
				// than reject a concurrent request or unexpectedly select the wallet.
				var pending struct{ Five, Week int64 }
				if err := tx.Model(&OmniReservation{}).Where("user_id = ? AND source = ? AND state = ? AND subscription_start = ?", uid, "omni_subscription", "pending", s.Start).Select("COALESCE(SUM(CASE WHEN five_epoch = ? THEN reserved ELSE 0 END),0) AS five, COALESCE(SUM(CASE WHEN week_epoch = ? THEN reserved ELSE 0 END),0) AS week", s.FiveEpoch, s.WeekEpoch).Scan(&pending).Error; err != nil {
					return err
				}
				if (!s.FiveEnabled() || s.FiveUsed-pending.Five < s.FiveLimit) && s.WeekUsed-pending.Week < s.WeekLimit {
					return ErrOmniAllowancePending
				}
				if !a.WalletOverflow {
					return omniAllowanceQuotaError(s, modelName, estimated)
				}
			}
		}
		if active && s.Includes(modelName) && !subscriptionPaused && s.available() >= amount {
			source = "omni_subscription"
			if s.FiveEnabled() {
				s.FiveUsed += amount
			}
			s.WeekUsed += amount
			if err = tx.Save(&s).Error; err != nil {
				return err
			}
		}
		if source == "wallet" {
			if mode == "subscription_only" {
				return ErrOmniInsufficient
			}
			if active && !a.WalletOverflow {
				return omniSubscriptionQuotaError(s, modelName, amount)
			}
			if flexible {
				var user User
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "quota").First(&user, uid).Error; err != nil {
					return err
				}
				if user.Quota <= 0 {
					return omniWalletQuotaError(tx, uid, 1)
				}
				amount = 0
			}
			res := tx.Model(&User{}).Where("id = ? AND quota >= ?", uid, amount).UpdateColumn("quota", gorm.Expr("quota - ?", amount))
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected != 1 {
				return omniWalletQuotaError(tx, uid, amount)
			}
			if active && !a.OverflowNotified {
				a.OverflowNotified = true
				if err = tx.Save(&a).Error; err != nil {
					return err
				}
				if err = omniNotice(tx, fmt.Sprintf("overflow:%d:%d", uid, s.Start), uid, "已使用钱包余额", "本次请求未使用订阅额度，已按API价格扣钱包。可在钱包关闭余额续用。"); err != nil {
					return err
				}
			}
		}
		scope := sharedProfile
		if scope == "" && source == "omni_subscription" {
			scope = s.CommerceProfile
		}
		scope = omniFreezeCapabilityProfileFrom(scope, omniRequestOptionsFor(tx))
		out = OmniReservation{CommerceProfile: scope, SharedPoolID: poolID, SharedTokenID: tokenID, RequestID: requestID, UserID: uid, Source: source, SubscriptionStart: s.Start, FiveEpoch: s.FiveEpoch, WeekEpoch: s.WeekEpoch, Reserved: amount, State: "pending", CreatedAt: now, FlexibleAllowance: flexible, EstimatedQuota: estimated}
		return tx.Create(&out).Error
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}
func OmniAdjust(requestID string, actual int64, final bool) error {
	_, err := OmniAdjustOnce(requestID, actual, final)
	return err
}
func OmniAdjustOnce(requestID string, actual int64, final bool) (bool, error) {
	changed := false
	if actual < 0 {
		return false, errors.New("negative actual quota")
	}
	var owner OmniReservation
	if err := DB.First(&owner, "request_id = ?", requestID).Error; err != nil {
		return false, err
	}
	if owner.Source == "omni_shared" {
		if !final {
			if actual > owner.Reserved {
				return false, errors.New("shared task cannot exceed its reservation")
			}
			return false, nil
		}
		return OmniSharedSettleOnce(requestID, actual)
	}
	if owner.BillingPolicy == OmniCustomerBillingV1 {
		return omniCustomerAdjust(requestID, actual, final)
	}
	defer invalidateUserCache(owner.UserID)
	defer func() {
		if final {
			_ = OmniReconcile(owner.UserID)
		}
	}()
	err := DB.Transaction(func(tx *gorm.DB) error {
		var lookup OmniReservation
		if err := tx.First(&lookup, "request_id = ?", requestID).Error; err != nil {
			return err
		}
		if _, err := omniAccount(tx, lookup.UserID); err != nil {
			return err
		}
		var r OmniReservation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&r, "request_id = ?", requestID).Error; err != nil {
			return err
		}
		if r.State != "pending" {
			return nil
		}
		delta := actual - r.Reserved
		if r.Source == "wallet" {
			if r.FlexibleAllowance {
				// New synchronous wallet requests debit only confirmed final usage.
				// Legacy holds retain their original settlement and refund behavior.
				if !final {
					return nil
				}
				var user User
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "quota").First(&user, r.UserID).Error; err != nil {
					return err
				}
				charge := min(actual, int64(max(0, user.Quota)))
				r.PlatformQuota = actual - charge
				if err := tx.Model(&User{}).Where("id = ?", r.UserID).UpdateColumn("quota", gorm.Expr("quota - ?", charge)).Error; err != nil {
					return err
				}
			} else {
				res := tx.Model(&User{}).Where("id = ? AND quota >= ?", r.UserID, max(delta, 0)).UpdateColumn("quota", gorm.Expr("quota - ?", delta))
				if res.Error != nil {
					return res.Error
				}
				if res.RowsAffected != 1 {
					return omniWalletQuotaError(tx, r.UserID, max(delta, 0))
				}
			}
		} else {
			var s OmniSubscription
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&s, "user_id = ?", r.UserID).Error; err != nil {
				return err
			}
			if err := s.advance(time.Now().Unix()); err != nil {
				return err
			}
			// Epochs bind refunds to the original windows. A completed old request
			// cannot create credit in a newly reset window.
			grace := final && r.FlexibleAllowance
			if grace && delta > 0 {
				available := s.available()
				if s.Start != r.SubscriptionStart || s.FiveEnabled() && s.FiveEpoch != r.FiveEpoch || s.WeekEpoch != r.WeekEpoch {
					available = 0
				}
				r.PlatformQuota = max(0, delta-available)
			}
			if delta > 0 && !grace && (s.Start != r.SubscriptionStart || s.FiveEnabled() && s.FiveEpoch != r.FiveEpoch || s.WeekEpoch != r.WeekEpoch) {
				return errors.New("reservation cannot grow after window transition")
			}
			if s.FiveEnabled() && s.Start == r.SubscriptionStart && s.FiveEpoch == r.FiveEpoch {
				if delta > 0 && !grace && s.FiveUsed+delta > s.FiveLimit {
					return ErrOmniInsufficient
				}
				if grace && delta > 0 && s.FiveUsed+delta > s.FiveLimit {
					s.FivePaused = true
				}
				s.FiveUsed = max(0, s.FiveUsed+delta)
			}
			if s.Start == r.SubscriptionStart && s.WeekEpoch == r.WeekEpoch {
				if delta > 0 && !grace && s.WeekUsed+delta > s.WeekLimit {
					return ErrOmniInsufficient
				}
				if grace && delta > 0 && s.WeekUsed+delta > s.WeekLimit {
					s.WeekPaused = true
				}
				s.WeekUsed = max(0, s.WeekUsed+delta)
			}
			if err := tx.Save(&s).Error; err != nil {
				return err
			}
		}
		r.Reserved = actual
		r.Actual = actual
		if final {
			r.State = "settled"
			if actual == 0 {
				r.State = "refunded"
			}
		}
		if err := tx.Save(&r).Error; err != nil {
			return err
		}
		changed = true
		return nil
	})
	return changed, err
}

// Payment verification calls this endpoint instead of separately mutating a
// wallet and a membership file. Receipt, balance, bonus and tier commit once.
func OmniCredit(key string, uid int, cents, quota int64, evidence ...OmniPaymentEvidence) (OmniLedger, error) {
	defer invalidateUserCache(uid)
	out := OmniLedger{}
	if key == "" || len(key) > 120 || uid <= 0 || cents <= 0 || quota <= 0 {
		return out, errors.New("invalid verified receipt")
	}
	err := DB.Transaction(func(tx *gorm.DB) error {
		var err error
		out, err = omniCreditTx(tx, key, uid, cents, quota, GetOmniPolicy().MembershipEnabled, evidence...)
		return err
	})
	return out, err
}

// Caller owns the transaction, including any payment-order state change.
// Real receipts always count; membership controls bonuses, never base credit.
func omniCreditTx(tx *gorm.DB, key string, uid int, cents, quota int64, membership bool, evidence ...OmniPaymentEvidence) (OmniLedger, error) {
	out := OmniLedger{}
	e, err := omniPaymentEvidence(evidence)
	if err != nil {
		return out, err
	}
	promotion := GetOmniPolicy().TopupPromotion
	if promotion.Valid() {
		membership = false
	}
	apply := func() error {
		a, err := omniAccount(tx, uid)
		if err != nil {
			return err
		}
		err = tx.First(&out, "key = ?", key).Error
		if err == nil {
			if out.UserID != uid || out.Cents != cents || out.Quota != quota || out.Kind != "topup" {
				return errors.New("receipt conflict")
			}
			return omniSupplementPromotion(tx, &out, e, promotion)
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		a.NetRechargeCents += cents
		level, pct := OmniLevel(a.NetRechargeCents)
		if !membership {
			pct = 0
		}
		bonus := quota/100*pct + quota%100*pct/100
		if quota > math.MaxInt64-bonus || a.NetRechargeCents < cents {
			return errors.New("receipt exceeds integer range")
		}
		if err = tx.Save(&a).Error; err != nil {
			return err
		}
		res := tx.Model(&User{}).Where("id = ? AND quota <= ?", uid, int64(math.MaxInt64)-quota-bonus).UpdateColumn("quota", gorm.Expr("quota + ?", quota+bonus))
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errors.New("user not found")
		}
		out = OmniLedger{Key: key, UserID: uid, Kind: "topup", Cents: cents, Quota: quota, Bonus: bonus, CreatedAt: time.Now().Unix()}
		if err = tx.Create(&out).Error; err != nil {
			return err
		}
		if err = omniSupplementPromotion(tx, &out, e, promotion); err != nil {
			return err
		}
		var balance User
		if err = tx.Select("quota").First(&balance, "id = ?", uid).Error; err != nil {
			return err
		}
		message := fmt.Sprintf("充值 ¥%.2f，钱包余额 $%.6f。", float64(cents)/100, float64(balance.Quota)/common.QuotaPerUnit)
		if membership {
			message = fmt.Sprintf("净充值 ¥%.2f，等级 %s，本笔赠额 $%.6f，钱包余额 $%.6f。", float64(cents)/100, level, float64(bonus)/common.QuotaPerUnit, float64(balance.Quota)/common.QuotaPerUnit)
		}
		return omniNotice(tx, "credit:"+key, uid, "充值已到账", message)
	}
	err = apply()
	return out, err
}
func OmniRefundCredit(key string) error {
	var owner OmniLedger
	if err := DB.First(&owner, "key = ?", key).Error; err != nil {
		return err
	}
	defer invalidateUserCache(owner.UserID)
	return DB.Transaction(func(tx *gorm.DB) error {
		var r OmniLedger
		if err := tx.First(&r, "key = ? AND kind = ?", key, "topup").Error; err != nil {
			return err
		}
		a, err := omniAccount(tx, r.UserID)
		if err != nil {
			return err
		}
		if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&r, "key = ?", key).Error; err != nil {
			return err
		}
		if r.Refunded {
			return nil
		}
		a.NetRechargeCents = max(0, a.NetRechargeCents-r.Cents)
		if err = tx.Save(&a).Error; err != nil {
			return err
		}
		if err = tx.Model(&User{}).Where("id = ?", r.UserID).UpdateColumn("quota", gorm.Expr("quota - ?", r.Quota+r.Bonus)).Error; err != nil {
			return err
		}
		r.Refunded = true
		return tx.Save(&r).Error
	})
}
func OmniActivate(key string, uid int, planKey string, months int, paidCents, fx int64, orderIDs ...string) error {
	var user User
	if uid <= 0 || DB.Select("id").First(&user, "id = ?", uid).Error != nil {
		return errors.New("user not found")
	}
	if !OmniSalesReady() || !OmniUserInRollout(uid) {
		return errors.New("subscription sales disabled")
	}
	p, err := omniPlan(planKey)
	if err != nil {
		return err
	}
	expected := p.MonthlyCents
	if months == 12 {
		expected = p.AnnualCents
	} else if months != 1 {
		return errors.New("invalid duration")
	}
	if paidCents != expected || OmniQuota(p.WeekCents, fx) <= 0 || key == "" || len(key) > 120 {
		return errors.New("invalid subscription receipt")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		return omniActivateTx(tx, key, uid, planKey, months, paidCents, fx, orderIDs...)
	})
}
func omniActivateTx(tx *gorm.DB, key string, uid int, planKey string, months int, paidCents, fx int64, orderIDs ...string) error {
	if err := omniLegacySaleGuard(tx); err != nil {
		return err
	}
	p, err := omniPlan(planKey)
	if err != nil {
		return err
	}

	a, err := omniAccount(tx, uid)
	if err != nil {
		return err
	}
	var exists OmniLedger
	err = tx.First(&exists, "key = ?", key).Error
	if err == nil {
		if exists.UserID != uid || exists.Kind != "subscription:"+planKey || exists.Cents != paidCents {
			return errors.New("receipt conflict")
		}
		if len(orderIDs) > 0 && orderIDs[0] != "" {
			var verifiedOrder OmniOrder
			if err := tx.First(&verifiedOrder, "id = ? AND user_id = ?", orderIDs[0], uid).Error; err != nil {
				return err
			}
			if verifiedOrder.State != "fulfilled" || verifiedOrder.Receipt != key {
				return errors.New("receipt already belongs to another order")
			}
		}
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if len(orderIDs) > 0 && orderIDs[0] != "" {
		var order OmniOrder
		if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&order, "id = ? AND user_id = ?", orderIDs[0], uid).Error; err != nil {
			return err
		}
		if order.State != "pending" || order.PlanKey != planKey || order.Months != months || order.Cents != paidCents {
			return errors.New("order conflict")
		}
		order.State = "fulfilled"
		order.Receipt = key
		if err = tx.Save(&order).Error; err != nil {
			return err
		}
	}
	var s OmniSubscription
	err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&s, "user_id = ?", uid).Error
	now := time.Now().Unix()
	if err == nil {
		if err = s.advance(now); err != nil {
			return err
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if s.End > now {
		if s.PlanKey == planKey {
			if s.MonthsPurchased <= 0 {
				for OmniCalendarEnd(s.Start, s.MonthsPurchased) < s.End {
					s.MonthsPurchased++
				}
			}
			s.MonthsPurchased += months
			s.End = OmniCalendarEnd(s.Start, s.MonthsPurchased)
		} else if s.PendingMonths == 0 || s.PendingPlan == planKey {
			if s.PendingMonths == 0 {
				s.PendingIncludedModels = OmniModelSnapshot()
			}
			s.PendingPlan = planKey
			s.PendingMonths += months
			s.PendingFx = fx
		} else {
			return errors.New("a different plan is already queued")
		}
	} else {
		var pending int64
		if err = tx.Model(&OmniReservation{}).Where("user_id = ? AND state = ?", uid, "pending").Count(&pending).Error; err != nil {
			return err
		}
		if pending > 0 { /* activation waits for original-source settlement */
		}
		busy, err := omniBusy(tx, uid)
		if err != nil {
			return err
		}
		if !busy {
			if err := omniPauseLegacy(tx, uid, now); err != nil {
				return err
			}
		}
		start := now

		s = OmniSubscription{ActivationPending: busy, UserID: uid, PlanKey: planKey, Start: start, End: OmniCalendarEnd(start, months), LastPurchaseMonths: months, MonthsPurchased: months, FxMicros: fx, FiveLimit: OmniQuota(p.FiveHourCents, fx), WeekLimit: OmniQuota(p.WeekCents, fx), QuotaVersion: OmniQuotaVersion, BillingVersion: 2, IncludedModels: OmniModelSnapshot()}
		a.OverflowNotified = false
		if err = tx.Save(&a).Error; err != nil {
			return err
		}
	}
	if s.PlanKey == planKey {
		s.LastPurchaseMonths = months
	}
	if err = tx.Save(&s).Error; err != nil {
		return err
	}
	ledger := OmniLedger{Key: key, UserID: uid, Kind: "subscription:" + planKey, Cents: paidCents, FxMicros: fx, CreatedAt: now}
	if len(orderIDs) > 2 {
		ledger.FxSource = orderIDs[1]
		ledger.FxAsOf = orderIDs[2]
	}
	if err := tx.Create(&ledger).Error; err != nil {
		return err
	}
	return omniLifecycleTx(tx, uid, now)
}
func OmniCreateOrder(uid int, planKey string, months int) (OmniOrder, error) {
	if err := omniLegacySaleGuard(DB); err != nil {
		return OmniOrder{}, err
	}
	if OmniCommerceSettings().Enabled {
		return OmniOrder{}, ErrOmniCommerceClosed
	}
	if GetOmniPolicy().Payment.Mode == "wallet_balance" {
		return OmniOrder{}, errors.New("订阅只能使用钱包余额购买；扫码支付仅用于充值钱包")
	}
	out := OmniOrder{}
	if !OmniSalesReady() || !OmniUserInRollout(uid) {
		return out, errors.New("subscription sales disabled")
	}
	p, err := omniPlan(planKey)
	if err != nil {
		return out, err
	}
	price := p.MonthlyCents
	if months == 12 {
		price = p.AnnualCents
	} else if months != 1 {
		return out, errors.New("invalid duration")
	}
	err = DB.Transaction(func(tx *gorm.DB) error {
		if _, err := omniAccount(tx, uid); err != nil {
			return err
		}
		err := tx.First(&out, "user_id = ? AND plan_key = ? AND months = ? AND state IN ? AND created_at > ?", uid, planKey, months, []string{"awaiting_payment", "pending"}, time.Now().Unix()-600).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		out = OmniOrder{ID: "S-" + common.GetRandomString(24), UserID: uid, PlanKey: planKey, Months: months, Cents: price, State: "awaiting_payment", CreatedAt: time.Now().Unix()}
		return tx.Create(&out).Error
	})
	return out, err
}
func omniHash(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}
func omniPeriod(s OmniSubscription, now int64) int64 {
	var period int64
	for OmniCalendarEnd(s.Start, int(period+1)) <= now {
		period++
	}
	return period
}
func omniIssue(tx *gorm.DB, s OmniSubscription, kind, id string, period, now int64) (string, error) {
	if kind == "five_hour" && !s.FiveEnabled() {
		return "", errors.New("当前订阅没有五小时窗口")
	}
	if kind != "five_hour" && kind != "week" {
		return "", errors.New("invalid reset type")
	}
	var old OmniCard
	if tx.First(&old, "id = ?", id).Error == nil {
		return "", nil
	}
	cost := s.FiveLimit
	limit := 2 * s.FiveLimit
	if kind == "week" {
		cost = s.WeekLimit
		limit = s.WeekLimit
	}
	var total int64
	if err := tx.Model(&OmniCard{}).Where("user_id = ? AND subscription_start = ? AND period = ? AND kind = ? AND state <> ?", s.UserID, s.Start, period, kind, "revoked").Select("COALESCE(SUM(cost_quota),0)").Scan(&total).Error; err != nil {
		return "", err
	}
	if total+cost > limit {
		return "", errors.New("monthly reset budget exceeded")
	}
	code := "OMNI-" + common.GetRandomString(32)
	card := OmniCard{Hash: omniHash(code), ID: id, UserID: s.UserID, PlanKey: s.PlanKey, SubscriptionStart: s.Start, Kind: kind, Period: period, Expires: now + 2592000, State: "unused", CostQuota: cost, CreatedAt: now}
	return code, tx.Create(&card).Error
}
func OmniGrantCards(uid int, kind string, count int) ([]string, error) {
	if count < 1 || count > 20 {
		return nil, errors.New("invalid batch size")
	}
	codes := []string{}
	if _, err := OmniClaimCards(uid); err != nil {
		return nil, err
	} // reserve promised membership benefits first
	err := DB.Transaction(func(tx *gorm.DB) error {
		if _, err := omniAccount(tx, uid); err != nil {
			return err
		}
		var s OmniSubscription
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&s, "user_id = ?", uid).Error; err != nil {
			return err
		}
		now := time.Now().Unix()
		if err := s.advance(now); err != nil {
			return err
		}
		if s.ExitApplicationID != "" || s.Start > now || s.End <= now {
			return errors.New("no active subscription")
		}
		for i := 0; i < count; i++ {
			code, err := omniIssue(tx, s, kind, "admin:"+common.GetRandomString(24), omniPeriod(s, now), now)
			if err != nil {
				return err
			}
			codes = append(codes, code)
		}
		return tx.Save(&s).Error
	})
	return codes, err
}
func OmniClaimCards(uid int, expected ...int64) ([]string, error) {
	codes := []string{}
	if !GetOmniPolicy().BenefitsEnabled() {
		return codes, errors.New("benefit cards disabled")
	}
	err := DB.Transaction(func(tx *gorm.DB) error {
		a, err := omniAccount(tx, uid)
		if err != nil {
			return err
		}
		if e := OmniRiskBusinessMutation(tx, uid, expected...); e != nil {
			return e
		}
		level, _ := OmniLevel(a.NetRechargeCents)
		if level == "regular" {
			return nil
		}
		var s OmniSubscription
		if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&s, "user_id = ?", uid).Error; err != nil {
			return err
		}
		now := time.Now().Unix()
		if err = s.advance(now); err != nil {
			return err
		}
		if s.ExitApplicationID != "" || s.Start > now || s.End <= now {
			return errors.New("no active subscription")
		}
		period := omniPeriod(s, now)
		kinds := []string{}
		if s.FiveEnabled() {
			kinds = append(kinds, "five_hour")
		}
		if level == "svip" {
			if s.FiveEnabled() {
				kinds = append(kinds, "five_hour")
			}
			kinds = append(kinds, "week")
		}
		expected := map[string]int64{}
		issued := map[string]int64{}
		for _, kind := range kinds {
			expected[kind]++
		}
		for kind := range expected {
			var count int64
			if err = tx.Model(&OmniCard{}).Where("user_id = ? AND subscription_start = ? AND period = ? AND kind = ? AND state <> ?", uid, s.Start, period, kind, "revoked").Count(&count).Error; err != nil {
				return err
			}
			issued[kind] = count
		}
		for i, kind := range kinds {
			if issued[kind] >= expected[kind] {
				continue
			}
			code, err := omniIssue(tx, s, kind, fmt.Sprintf("benefit:%d:%d:%d:%d", uid, s.Start, period, i), period, now)
			if err != nil {
				return err
			}
			if code != "" {
				codes = append(codes, code)
				issued[kind]++
			}
		}
		return tx.Save(&s).Error
	})
	return codes, err
}
func OmniRedeem(uid int, code string) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		if _, err := omniAccount(tx, uid); err != nil {
			return err
		}
		var s OmniSubscription
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&s, "user_id = ?", uid).Error; err != nil {
			return err
		}
		now := time.Now().Unix()
		if err := s.advance(now); err != nil {
			return err
		}
		var card OmniCard
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&card, "(hash = ? OR id = ?) AND user_id = ?", omniHash(code), code, uid).Error; err != nil {
			return errors.New("invalid reset card")
		}
		if s.ExitApplicationID != "" || card.State != "unused" || card.Expires <= now || card.PlanKey != s.PlanKey || s.Start > now || s.End <= now {
			return errors.New("reset card unavailable")
		}
		var n int64
		if err := tx.Model(&OmniReservation{}).Where("user_id = ? AND state = ?", uid, "pending").Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return errors.New("requests in progress; retry after completion")
		}
		if card.Kind == "week" {
			s.WeekUsed = 0
			s.WeekPaused = false
		} else {
			s.FiveUsed = 0
			s.FivePaused = false
		}
		card.State = "used"
		if err := omniCustomerResetWindows(tx, s, card.Kind != "week", card.Kind == "week"); err != nil {
			return err
		}
		if err := tx.Save(&s).Error; err != nil {
			return err
		}
		return tx.Save(&card).Error
	})
}
func OmniRewardReferral(inviter, invitee int) error {
	defer invalidateUserCache(inviter)
	if inviter <= 0 || invitee <= 0 || inviter == invitee {
		return errors.New("invalid referral")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := OmniRiskLockRewardOwners(tx, inviter, invitee); err != nil {
			return err
		}
		key := fmt.Sprintf("referral:%d", invitee)
		var old OmniLedger
		err := tx.First(&old, "key = ?", key).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var invited User
		if err = tx.First(&invited, "id = ? AND inviter_id = ?", invitee, inviter).Error; err != nil {
			return err
		}
		amount := int64(common.QuotaForInviter)
		var held bool
		amount, held, err = OmniRiskRewardGate(tx, inviter, invitee, amount)
		if err != nil {
			return err
		}
		if held {
			return nil
		}
		res := tx.Model(&User{}).Where("id = ?", inviter).Updates(map[string]any{"quota": gorm.Expr("quota + ?", amount), "aff_count": gorm.Expr("aff_count + 1"), "aff_history": gorm.Expr("aff_history + ?", amount)})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errors.New("inviter missing")
		}
		if err = tx.Create(&OmniLedger{Key: key, UserID: inviter, Kind: "referral", Quota: amount, CreatedAt: time.Now().Unix()}).Error; err != nil {
			return err
		}
		if OmniRiskV2Ready() {
			if err = tx.Model(&OmniRiskRewardHold{}).Where("invitee = ? AND state = ?", invitee, "released").Updates(map[string]any{"state": "applied", "applied_at": time.Now().Unix()}).Error; err != nil {
				return err
			}
		}
		var balance User
		if err = tx.Select("quota").First(&balance, "id = ?", inviter).Error; err != nil {
			return err
		}
		return omniNotice(tx, key, inviter, "邀请奖励已到账", fmt.Sprintf("18元等值奖励已入账（$%.6f），钱包余额 $%.6f。", float64(amount)/common.QuotaPerUnit, float64(balance.Quota)/common.QuotaPerUnit))
	})
}
func OmniMigrateReferral(uid int) error {
	defer invalidateUserCache(uid)
	return DB.Transaction(func(tx *gorm.DB) error {
		if _, err := omniAccount(tx, uid); err != nil {
			return err
		}
		var u User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&u, "id = ?", uid).Error; err != nil {
			return err
		}
		if u.AffQuota <= 0 {
			return nil
		}
		var migrated int64
		if err := tx.Model(&OmniLedger{}).Where("user_id = ? AND kind = ?", uid, "referral_migration").Select("COALESCE(SUM(quota),0)").Scan(&migrated).Error; err != nil {
			return err
		}
		key := fmt.Sprintf("referral-migrate:%d:%d", uid, migrated+int64(u.AffQuota))
		if err := tx.Model(&u).Updates(map[string]any{"quota": gorm.Expr("quota + ?", u.AffQuota), "aff_quota": 0}).Error; err != nil {
			return err
		}
		if err := tx.Create(&OmniLedger{Key: key, UserID: uid, Kind: "referral_migration", Quota: int64(u.AffQuota), CreatedAt: time.Now().Unix()}).Error; err != nil {
			return err
		}
		return omniNotice(tx, key, uid, "邀请额度已转入钱包", "历史未转入的邀请额度已直接计入钱包，无需手动转入。")
	})
}

// Wallet debit, entitlement and business-key ledger commit or roll back together.
func OmniPurchaseWallet(uid int, planKey string, months int, requestKey string, expected ...int64) error {
	if err := omniLegacySaleGuard(DB); err != nil {
		return err
	}
	if OmniCommerceSettings().Enabled {
		return errors.New("请使用新版订阅商品")
	}
	defer invalidateUserCache(uid)
	if GetOmniPolicy().Payment.Mode != "wallet_balance" || !OmniSalesReady() || !OmniUserInRollout(uid) {
		return errors.New("订阅购买暂不可用")
	}
	if len(requestKey) < 16 || len(requestKey) > 80 {
		return errors.New("invalid purchase key")
	}
	p, err := omniPlan(planKey)
	if err != nil {
		return err
	}
	cents := p.MonthlyCents
	if months == 12 {
		cents = p.AnnualCents
	} else if months != 1 {
		return errors.New("invalid duration")
	}
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap["OmniAPIPricing"]
	common.OptionMapRWMutex.RUnlock()
	var snapshot struct {
		FX     string `json:"cny_per_usd"`
		Source string `json:"fx_source"`
		AsOf   string `json:"fx_as_of"`
	}
	if json.Unmarshal([]byte(raw), &snapshot) != nil {
		return errors.New("汇率暂不可用")
	}
	var rate float64
	if _, err = fmt.Sscan(snapshot.FX, &rate); err != nil {
		return errors.New("汇率暂不可用")
	}
	fx := int64(math.Round(rate * 1000000))
	if fx < 1000000 || fx > 20000000 {
		return errors.New("汇率暂不可用")
	}
	quota := int64(math.Ceil(float64(cents) * 10000 * common.QuotaPerUnit / float64(fx)))
	key := fmt.Sprintf("wallet-sub:%d:%s", uid, requestKey)
	return DB.Transaction(func(tx *gorm.DB) error {
		if _, err := omniAccount(tx, uid); err != nil {
			return err
		}
		if e := OmniRiskBusinessMutation(tx, uid, expected...); e != nil {
			return e
		}
		var existing OmniLedger
		err := tx.First(&existing, "key = ?", key).Error
		if err == nil {
			if existing.UserID != uid || existing.Kind != "subscription:"+planKey || existing.Cents != cents || existing.FxSource != fmt.Sprintf("wallet:%d", months) {
				return errors.New("purchase key conflict")
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		result := tx.Model(&User{}).Where("id = ? AND status = ? AND quota >= ?", uid, 1, quota).UpdateColumn("quota", gorm.Expr("quota - ?", quota))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("钱包余额不足，请先充值钱包")
		}
		if err := omniActivateTx(tx, key, uid, planKey, months, cents, fx); err != nil {
			return err
		}
		order := OmniOrder{ID: "W-" + omniHash(key)[:24], UserID: uid, PlanKey: planKey, Months: months, Cents: cents, State: "fulfilled", Receipt: key, PaymentMethod: "wallet", CreatedAt: time.Now().Unix()}
		if err := tx.Create(&order).Error; err != nil {
			return err
		}
		return tx.Model(&OmniLedger{}).Where("key = ?", key).Updates(map[string]any{"quota": quota, "fx_source": fmt.Sprintf("wallet:%d", months), "fx_as_of": snapshot.AsOf}).Error
	})
}
