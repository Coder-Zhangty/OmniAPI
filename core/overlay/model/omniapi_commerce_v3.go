package model

// New commerce is deliberately independent of the legacy plan constants.
// Merely deploying its tables must neither reprice old purchases nor open sales.
import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrOmniCommerceClosed = errors.New("新商品尚未完成核价与上线验收")

type OmniCommerceSwitches struct {
	Enabled      bool   `json:"enabled"`
	SalesEnabled bool   `json:"sales_enabled"`
	ProfileID    string `json:"profile_id"`
	EmailReady   bool   `json:"email_ready"`
	AcceptanceID string `json:"acceptance_id"`
}

type OmniCommerceProfile struct {
	PriceVersion             string `json:"price_version"`
	PriceSnapshot            string `json:"price_snapshot" gorm:"type:text"`
	CapabilityMatrix         string `json:"capability_matrix" gorm:"type:text"`
	ID                       string `json:"id" gorm:"primaryKey;size:64"`
	ApprovedBy               int    `json:"approved_by"`
	ApprovedAt               int64  `json:"approved_at"`
	AuditExpiresAt           int64  `json:"audit_expires_at"`
	Evidence                 string `json:"evidence" gorm:"type:text"`
	PricingHash              string `json:"pricing_hash"`
	RoutingHash              string `json:"routing_hash"`
	Models                   string `json:"models" gorm:"type:text"` // model -> allowed channel IDs
	FXMicros                 int64  `json:"fx_micros"`
	WorstCostPPM             int64  `json:"worst_cost_ppm"`
	PaymentFeePPM            int64  `json:"payment_fee_ppm"`
	RiskReservePPM           int64  `json:"risk_reserve_ppm"`
	FixedFeeCents            int64  `json:"fixed_fee_cents"`
	ContentHash              string `json:"content_hash"`
	CapabilityReleaseVersion string `json:"capability_release_version,omitempty" gorm:"-"`
}

type OmniCommercePlan struct {
	Key          string        `json:"key"`
	Title        string        `json:"title"`
	MonthlyCents int64         `json:"monthly_cents"`
	Prices       map[int]int64 `json:"prices"`
	FiveEnabled  bool          `json:"five_enabled"`
	FiveCents    int64         `json:"five_cents"`
	WeekCents    int64         `json:"week_cents"`
	ResetCents   int64         `json:"reset_cents"`
}

func OmniCommerceSettings() OmniCommerceSwitches {
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap["OmniAPICommerceV3"]
	common.OptionMapRWMutex.RUnlock()
	var out OmniCommerceSwitches
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

func omniOptionHash(key string) string {
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap[key]
	common.OptionMapRWMutex.RUnlock()
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

func omniIntRatio(n, mul, div int64, ceil bool) (int64, error) {
	if n < 0 || mul < 0 || div <= 0 {
		return 0, errors.New("invalid monetary ratio")
	}
	x := new(big.Int).Mul(big.NewInt(n), big.NewInt(mul))
	if ceil && x.Sign() > 0 {
		x.Add(x, big.NewInt(div-1))
	}
	x.Quo(x, big.NewInt(div))
	if !x.IsInt64() {
		return 0, errors.New("monetary overflow")
	}
	return x.Int64(), nil
}

func OmniCommercePrice(monthly int64, months int) (int64, error) {
	return (OmniCommerceProfile{}).Price(monthly, months)
}

func (p OmniCommerceProfile) Validate() error {
	if p.ID == "" || len(p.ID) > 64 || p.ApprovedBy <= 0 || p.ApprovedAt <= 0 || p.AuditExpiresAt <= p.ApprovedAt || p.AuditExpiresAt > p.ApprovedAt+7*86400 || p.Evidence == "" || p.PricingHash == "" || p.RoutingHash == "" {
		return errors.New("核价证据或批准记录不完整")
	}
	if p.FXMicros < 1000000 || p.FXMicros > 20000000 || p.WorstCostPPM <= 0 || p.WorstCostPPM > 100000000 || p.PaymentFeePPM < 0 || p.PaymentFeePPM >= 800000 || p.RiskReservePPM < 0 || p.RiskReservePPM >= 800000 || p.PaymentFeePPM+p.RiskReservePPM >= 800000 || p.FixedFeeCents < 0 || p.FixedFeeCents > 1000000 {
		return errors.New("核价费用参数无效")
	}
	var models map[string][]int
	if json.Unmarshal([]byte(p.Models), &models) != nil || len(models) == 0 {
		return errors.New("缺少已核价模型及主备通道")
	}
	for name, routes := range models {
		if name == "" || len(routes) == 0 {
			return errors.New("不完整的模型核价范围")
		}
		seen := map[int]bool{}
		for _, id := range routes {
			if id <= 0 || seen[id] {
				return errors.New("核价通道无效")
			}
			seen[id] = true
		}
	}
	return nil
}

func (p OmniCommerceProfile) Allows(name string, channel int) bool {
	return p.EffectiveCapabilities().allowsSnapshot(name, channel)
}

func (p OmniCommerceProfile) allowsSnapshot(name string, channel int) bool {
	var models map[string][]int
	if json.Unmarshal([]byte(p.Models), &models) != nil {
		return false
	}
	ids, ok := models[name]
	if !ok {
		return false
	}
	if channel == 0 {
		return true
	}
	for _, id := range ids {
		if id == channel {
			return true
		}
	}
	return false
}

func (p OmniCommerceProfile) GrantCents(paid int64) (int64, error) {
	if err := p.Validate(); err != nil {
		return 0, err
	}
	budget, err := omniIntRatio(paid, 800000-p.PaymentFeePPM-p.RiskReservePPM, 1000000, false)
	if err != nil {
		return 0, err
	}
	if budget <= p.FixedFeeCents {
		return 0, errors.New("该金额无法覆盖核价成本")
	}
	return omniIntRatio(budget-p.FixedFeeCents, 1000000, p.WorstCostPPM, false)
}

func (p OmniCommerceProfile) Plans() ([]OmniCommercePlan, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	out := []OmniCommercePlan{}
	for i, price := range []int64{1900, 3900, 6900, 9900, 19900} {
		row := OmniCommercePlan{Key: fmt.Sprintf("v3_%d", price/100), Title: fmt.Sprintf("%d元套餐", price/100), MonthlyCents: price, Prices: map[int]int64{}, FiveEnabled: i < 2}
		for _, months := range []int{1, 3, 6, 12} {
			r, err := p.Price(price, months)
			if err != nil {
				return nil, err
			}
			row.Prices[months] = r
			grant, err := p.GrantCents(r)
			if err != nil {
				return nil, err
			}
			// Cover existing monthly welfare as well as natural windows. The
			// current rules cap free grants at one week and two five-hour resets
			// per subscription month; a five-hour reset is at most 1/4 week.
			windows := map[int]int64{1: 5, 3: 14, 6: 27, 12: 53}[months]
			benefitQuarters := int64(months) * 4
			if row.FiveEnabled {
				benefitQuarters += int64(months) * 2
			}
			week, err := omniIntRatio(grant, 4, windows*4+benefitQuarters, false)
			if err != nil {
				return nil, err
			}
			if row.WeekCents == 0 || week < row.WeekCents {
				row.WeekCents = week
			}
		}
		if row.WeekCents <= 0 {
			return nil, errors.New("核价后无可售额度")
		}
		if row.FiveEnabled {
			row.FiveCents = row.WeekCents / 4
			if row.FiveCents <= 0 {
				return nil, errors.New("五小时额度不足")
			}
		}
		base, err := omniIntRatio(row.WeekCents, p.WorstCostPPM, 1000000, true)
		if err != nil {
			return nil, err
		}
		row.ResetCents, err = omniIntRatio(base+p.FixedFeeCents, 1000000, 800000-p.PaymentFeePPM-p.RiskReservePPM, true)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, nil
}

func OmniCommerceSaleProfile(tx *gorm.DB) (OmniCommerceProfile, error) {
	cfg := OmniCommerceSettings()
	var p OmniCommerceProfile
	if !cfg.Enabled || !cfg.SalesEnabled || cfg.AcceptanceID == "" || cfg.ProfileID == "" {
		return p, ErrOmniCommerceClosed
	}
	var release OmniCommerceRelease
	if tx.First(&release, "id = ? AND state = ? AND profile_id = ? AND acceptance_id = ?", "unified-20261104", "active", cfg.ProfileID, cfg.AcceptanceID).Error != nil {
		return p, ErrOmniCommerceClosed
	}
	if err := tx.First(&p, "id = ?", cfg.ProfileID).Error; err != nil {
		return p, ErrOmniCommerceClosed
	}
	if p.Validate() != nil || p.AuditExpiresAt <= time.Now().Unix() || p.PricingHash != omniOptionHash("OmniAPIPricing") || p.RoutingHash != omniOptionHash("OmniAPIRouting") {
		return p, ErrOmniCommerceClosed
	}
	if err := OmniCommerceLaunchReady(p); err != nil {
		return p, err
	}
	return p, nil
}

func omniCommerceID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b[:])
}

func omniCommerceWallet(tx *gorm.DB, uid int, cents, fx int64) (int64, error) {
	if cents <= 0 {
		return 0, errors.New("invalid charge")
	}
	// Ceiling debits prevent fractional quota from turning into free money.
	q, err := omniIntRatio(cents, int64(common.QuotaPerUnit)*10000, fx, true)
	if err != nil || q <= 0 {
		return 0, errors.New("invalid wallet conversion")
	}
	if err = OmniRiskBusinessMutation(tx, uid); err != nil {
		return 0, err
	}
	r := tx.Model(&User{}).Where("id = ? AND status = ? AND quota >= ?", uid, common.UserStatusEnabled, q).UpdateColumn("quota", gorm.Expr("quota - ?", q))
	if r.Error != nil {
		return 0, r.Error
	}
	if r.RowsAffected != 1 {
		return 0, errors.New("钱包余额不足")
	}
	return q, nil
}

func omniCommercePool(tx *gorm.DB, id string) (OmniSharedPool, error) {
	var p OmniSharedPool
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&p, "id = ?", id).Error
	return p, err
}

func omniSortedModelNames(profile OmniCommerceProfile) []string {
	profile = profile.EffectiveCapabilities()
	var m map[string][]int
	_ = json.Unmarshal([]byte(profile.Models), &m)
	out := []string{}
	for name := range m {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Multiplier estimates retain their real provenance. Never turn the supplier
// pricing snapshot's complete_prices_verified flag on to import an estimate.
func (p OmniCommerceProfile) UsesMultiplierEstimate() bool {
	var evidence struct {
		Worksheet struct {
			CostBasis     string `json:"cost_basis"`
			Authorization struct {
				Mode        string `json:"mode"`
				Approved    bool   `json:"approved"`
				Source      string `json:"source"`
				Instruction string `json:"operator_instruction"`
			} `json:"estimation_authorization"`
		} `json:"worksheet"`
	}
	if json.Unmarshal([]byte(p.Evidence), &evidence) != nil {
		return false
	}
	a := evidence.Worksheet.Authorization
	return evidence.Worksheet.CostBasis == "platform_multiplier_estimate" && a.Mode == "platform_multiplier_estimate" && a.Approved && a.Source != "" && a.Instruction != ""
}

// Importing an immutable audit never opens sales.
func OmniCommerceApprove(p OmniCommerceProfile) error {
	if p.PriceSnapshot == "" {
		p.PriceSnapshot = omniCommercePricesJSON
		table, err := OmniCommercePrices()
		if err != nil {
			return err
		}
		p.PriceVersion = table.Version
	}
	var pricesVersion OmniCommercePriceTable
	if json.Unmarshal([]byte(p.PriceSnapshot), &pricesVersion) != nil || pricesVersion.Validate() != nil || p.PriceVersion != pricesVersion.Version {
		return errors.New("价格版本快照无效")
	}
	p.PricingHash = omniOptionHash("OmniAPIPricing")
	p.RoutingHash = omniOptionHash("OmniAPIRouting")
	if err := p.Validate(); err != nil {
		return err
	}
	if p.AuditExpiresAt <= time.Now().Unix() {
		return errors.New("核价已过期")
	}
	if _, err := p.Plans(); err != nil {
		return err
	}
	var audited map[string][]int
	_ = json.Unmarshal([]byte(p.Models), &audited)
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap["OmniAPIRouting"]
	pricesRaw := common.OptionMap["OmniAPIPricing"]
	common.OptionMapRWMutex.RUnlock()
	var routing struct {
		Models map[string][]int `json:"models"`
	}
	if json.Unmarshal([]byte(raw), &routing) != nil {
		return errors.New("通道配置不可用")
	}
	var prices struct {
		Models map[string]struct {
			Costs map[string]json.RawMessage `json:"costs"`
		} `json:"models"`
	}
	if json.Unmarshal([]byte(pricesRaw), &prices) != nil {
		return errors.New("采购成本快照不可用")
	}
	for name, ids := range audited {
		for _, id := range ids {
			var cost map[string]json.RawMessage
			if json.Unmarshal(prices.Models[name].Costs[fmt.Sprint(id)], &cost) != nil {
				return errors.New("采购成本未核齐")
			}
			var complete bool
			_ = json.Unmarshal(cost["complete_prices_verified"], &complete)
			if !complete && !p.UsesMultiplierEstimate() {
				return errors.New("采购计价维度尚未完整核验")
			}
		}
		actual := routing.Models[name]
		if len(actual) == 0 {
			return errors.New("核价模型没有实际通道")
		}
		for _, id := range actual {
			found := false
			for _, checked := range ids {
				if id == checked {
					found = true
				}
			}
			if !found {
				return errors.New("模型的主备通道尚未全部核价")
			}
		}
		for _, id := range ids {
			found := false
			for _, active := range actual {
				if id == active {
					found = true
				}
			}
			if !found {
				return errors.New("核价通道不在实际路由中")
			}
		}
	}
	p.ContentHash = ""
	rawHash, _ := json.Marshal(p)
	sum := sha256.Sum256(rawHash)
	p.ContentHash = hex.EncodeToString(sum[:])
	return DB.Create(&p).Error
}
