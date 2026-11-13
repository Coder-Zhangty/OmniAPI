package service

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func omniUserDiscountFixture(t *testing.T) *relaycommon.RelayInfo {
	old := common.OptionMap
	rule := model.OmniUserDiscountRule{Enabled: true, Basis: "procurement_margin", TargetMargin: .4, FundingScope: "wallet", ModelScope: "text", PublicLabel: "价格优惠"}
	policy, _ := json.Marshal(model.OmniUserDiscountPolicy{Version: "discount-frozen-test", Users: map[string]model.OmniUserDiscountRule{"16": rule}})
	common.OptionMap = map[string]string{model.OmniUserDiscountOptionKey: string(policy), "OmniAPIModelRegistry": `{"enabled":true,"version":"discount-text-registry","models":{"text-model":{"version":"test","capability":"text","bindings":[{"channel_id":1,"upstream_model":"text-model","status":"verified","protocols":["openai"]},{"channel_id":6,"upstream_model":"text-model","status":"verified","protocols":["claude"]}]},"image-model":{"version":"test","capability":"image","bindings":[]}}}`}
	t.Cleanup(func() { common.OptionMap = old })
	expression := `v1:tier("base", p * 10 + c * 50 + cr * 1 + cc * 12.5 + cc1h * 20)`
	return &relaycommon.RelayInfo{UserId: 16, OriginModelName: "text-model", Request: &dto.GeneralOpenAIRequest{Model: "text-model"}, OmniRegistryVersion: "discount-text-registry", OmniPriceExpression: expression, ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 1}, BillingSource: "wallet", OmniPriceQuote: []byte(`{"costs":{"1":{"complete_prices_verified":true,"input_usd_per_million":0.6,"output_usd_per_million":3,"cache_read_usd_per_million":0.06,"cache_write_usd_per_million":0.75,"cache_write_1h_usd_per_million":1.2},"6":{"complete_prices_verified":true,"input_usd_per_million":1.2,"output_usd_per_million":6,"cache_read_usd_per_million":0.12,"cache_write_usd_per_million":1.5,"cache_write_1h_usd_per_million":2.4}}}`), TieredBillingSnapshot: &billingexpr.BillingSnapshot{BillingMode: "tiered_expr", ExprString: expression, ExprHash: billingexpr.ExprHashString(expression), ExprVersion: 1, QuotaPerUnit: common.QuotaPerUnit, GroupRatio: 1}}
}

func TestOmniUserDiscountWalletFrozenSelectedRouteAndCaches(t *testing.T) {
	info := omniUserDiscountFixture(t)
	originalExpression := info.OmniPriceExpression
	require.NoError(t, OmniPrepareUserDiscount(info))
	require.Equal(t, originalExpression, info.OmniPriceExpression)
	params := billingexpr.TokenParams{P: 100000, C: 10000, CR: 100000, CC: 100000, CC1h: 100000, Len: 400000}
	ok, quota, _ := TryTieredSettle(info, params)
	require.True(t, ok)
	expected := common.QuotaFromFloat((.06 + .03 + .006 + .075 + .12) / .6 * common.QuotaPerUnit)
	require.Equal(t, expected, quota)
	info.ChannelId = 6
	_, fallbackQuota, _ := TryTieredSettle(info, params)
	require.Equal(t, 2*quota, fallbackQuota)
	common.OptionMap[model.OmniUserDiscountOptionKey] = `{"version":"disabled","users":{}}`
	info.OmniPriceQuote = []byte(`{}`)
	require.NoError(t, OmniPrepareUserDiscount(info))
	_, frozenQuota, _ := TryTieredSettle(info, params)
	require.Equal(t, fallbackQuota, frozenQuota)
	require.Equal(t, originalExpression, info.TieredBillingSnapshot.ExprString)
}

func TestOmniUserDiscountSubscriptionAndModelScopesRemainPublic(t *testing.T) {
	info := omniUserDiscountFixture(t)
	require.NoError(t, OmniPrepareUserDiscount(info))
	params := billingexpr.TokenParams{P: 1000000, Len: 1000000}
	public, err := billingexpr.ComputeTieredQuota(info.TieredBillingSnapshot, params)
	require.NoError(t, err)
	for _, source := range []string{"omni_subscription", "subscription", ""} {
		info.BillingSource = source
		_, quota, _ := TryTieredSettle(info, params)
		require.Equal(t, public.ActualQuotaAfterGroup, quota)
		require.False(t, info.OmniUserDiscount.Applied)
	}
	info = omniUserDiscountFixture(t)
	info.OriginModelName = "image-model"
	require.NoError(t, OmniPrepareUserDiscount(info))
	require.False(t, info.OmniUserDiscount.Applicable)
	info = omniUserDiscountFixture(t)
	info.Request = &dto.GeneralOpenAIRequest{Model: "text-model"}
	require.NoError(t, OmniPrepareUserDiscount(info))
	require.True(t, info.OmniUserDiscount.Applicable, "text scope includes vision/tool payloads without unsafe-pure-text filtering")
	info = omniUserDiscountFixture(t)
	info.UserId = 17
	require.NoError(t, OmniPrepareUserDiscount(info))
	require.Nil(t, info.OmniUserDiscount)
	info = omniUserDiscountFixture(t)
	common.OptionMap["OmniAPIPrivateBilling"] = `{"16":{"enabled":true,"multiplier":1.1}}`
	require.ErrorContains(t, OmniPrepareUserDiscount(info), "冲突")
}

func TestOmniUserDiscountMissingUnverifiedAndPublicCap(t *testing.T) {
	for _, quote := range []string{`{}`, `{"costs":{"1":{"input_usd_per_million":.6}}}`, `{"costs":{"1":{"complete_prices_verified":false,"input_usd_per_million":0.6,"output_usd_per_million":3,"cache_read_usd_per_million":0.06,"cache_write_usd_per_million":0.75,"cache_write_1h_usd_per_million":1.2}}}`} {
		info := omniUserDiscountFixture(t)
		info.OmniPriceQuote = []byte(quote)
		require.NoError(t, OmniPrepareUserDiscount(info))
		params := billingexpr.TokenParams{P: 1000, Len: 1000}
		public, err := billingexpr.ComputeTieredQuota(info.TieredBillingSnapshot, params)
		require.NoError(t, err)
		_, quota, _ := TryTieredSettle(info, params)
		require.Equal(t, public.ActualQuotaAfterGroup, quota)
		require.False(t, info.OmniUserDiscount.Applied)
	}
	info := omniUserDiscountFixture(t)
	info.TieredBillingSnapshot.ExprString = `v1:tier("base", p * 0.1 + c * 0.1)`
	info.TieredBillingSnapshot.ExprHash = billingexpr.ExprHashString(info.TieredBillingSnapshot.ExprString)
	require.NoError(t, OmniPrepareUserDiscount(info))
	params := billingexpr.TokenParams{P: 1000000, Len: 1000000}
	_, quota, _ := TryTieredSettle(info, params)
	require.Equal(t, common.QuotaFromFloat(.1*common.QuotaPerUnit), quota)
	require.Equal(t, "public_price_cap", info.OmniUserDiscount.AdminReason)
}

func TestOmniUserDiscountTieredProcurementAndVisionNormalization(t *testing.T) {
	info := omniUserDiscountFixture(t)
	info.OmniPriceQuote = []byte(`{"costs":{"1":{"complete_prices_verified":true,"input_usd_per_million":0.6,"output_usd_per_million":3,"cache_read_usd_per_million":0.06,"cache_write_usd_per_million":0.75,"cache_write_1h_usd_per_million":1.2,"tiers":[{"min_tokens":200001,"max_tokens":null,"prices":{"input_usd_per_million":1.2,"output_usd_per_million":6,"cache_read_usd_per_million":0.12,"cache_write_usd_per_million":1.5,"cache_write_1h_usd_per_million":2.4}}]}}}`)
	require.NoError(t, OmniPrepareUserDiscount(info))
	params := billingexpr.TokenParams{P: 250000, Img: 50000, Len: 300000}
	_, quota, _ := TryTieredSettle(info, params)
	require.Equal(t, common.QuotaFromFloat(.3*1.2/.6*common.QuotaPerUnit), quota, "vision input remains included in procurement context tier")
}

func TestOmniUserDiscountKeepsPublicGroupAndToolSurcharges(t *testing.T) {
	info := omniUserDiscountFixture(t)
	info.TieredBillingSnapshot.GroupRatio = .5
	require.NoError(t, OmniPrepareUserDiscount(info))
	_, quota, result := TryTieredSettle(info, billingexpr.TokenParams{P: 1000000, Len: 1000000})
	require.Equal(t, common.QuotaFromFloat(1*common.QuotaPerUnit), quota)
	summary := textQuotaSummary{ToolCallSurchargeQuota: decimal.NewFromInt(12345)}
	require.Equal(t, quota+12345, composeTieredTextQuota(info, summary, quota, result), "tool surcharge stays undiscounted and group ratio is not applied twice")
	require.Equal(t, .5, info.TieredBillingSnapshot.GroupRatio)
}

type omniDiscountBillingFixture struct{ err error }

func (b *omniDiscountBillingFixture) Settle(int) error         { return b.err }
func (b *omniDiscountBillingFixture) Refund(*gin.Context)      {}
func (b *omniDiscountBillingFixture) NeedsRefund() bool        { return false }
func (b *omniDiscountBillingFixture) GetPreConsumedQuota() int { return 0 }
func (b *omniDiscountBillingFixture) Reserve(int) error        { return nil }

func TestOmniUserDiscountLogOnlyAfterSuccessfulSettlementAndPrivateMetadata(t *testing.T) {
	for _, success := range []bool{true, false} {
		info := omniUserDiscountFixture(t)
		require.NoError(t, OmniPrepareUserDiscount(info))
		_, quota, _ := TryTieredSettle(info, billingexpr.TokenParams{P: 1000, Len: 1000})
		other := model.NewLogOther()
		OmniInjectUserDiscountInfo(other, info)
		require.NotContains(t, other.Snapshot(), "price_discount_applied")
		failure := error(nil)
		if !success {
			failure = errors.New("settlement fixture failure")
		}
		info.Billing = &omniDiscountBillingFixture{err: failure}
		c, _ := gin.CreateTestContext(nil)
		err := SettleBilling(c, info, quota)
		require.Equal(t, !success, err != nil)
		other = model.NewLogOther()
		OmniInjectUserDiscountInfo(other, info)
		row := other.Snapshot()
		if success {
			require.Equal(t, true, row["price_discount_applied"])
		} else {
			require.NotContains(t, row, "price_discount_applied")
		}
		require.NotContains(t, row, "target_margin")
		require.NotContains(t, row["admin_info"], "target_margin")
		require.Contains(t, row["root_info"], "user_price_discount")
	}
}

func TestOmniUserDiscountDisabledAtStartRemainsDisabled(t *testing.T) {
	info := omniUserDiscountFixture(t)
	policy := common.OptionMap[model.OmniUserDiscountOptionKey]
	common.OptionMap[model.OmniUserDiscountOptionKey] = `{"version":"off","users":{}}`
	require.NoError(t, OmniPrepareUserDiscount(info))
	common.OptionMap[model.OmniUserDiscountOptionKey] = policy
	require.NoError(t, OmniPrepareUserDiscount(info))
	require.Nil(t, info.OmniUserDiscount)
}

func TestOmniUserDiscountRejectsNullAndNonnumericProcurementDimensions(t *testing.T) {
	valid := `{"complete_prices_verified":true,"input_usd_per_million":0.6,"output_usd_per_million":3,"cache_read_usd_per_million":0.06,"cache_write_usd_per_million":0.75,"cache_write_1h_usd_per_million":1.2}`
	for _, value := range []string{"null", `"0.01"`, "true", "-1"} {
		bad := strings.Replace(valid, `"cache_read_usd_per_million":0.06`, `"cache_read_usd_per_million":`+value, 1)
		require.False(t, omniDiscountVerifiedCost(json.RawMessage(bad), 1000), value)
		badTier := strings.TrimSuffix(valid, "}") + `,"tiers":[{"min_tokens":200001,"max_tokens":null,"prices":` + bad + `}]}`
		require.False(t, omniDiscountVerifiedCost(json.RawMessage(badTier), 300000), "tier "+value)
	}
}

func TestOmniUserDiscountPublicClampIsNotLabeledDiscount(t *testing.T) {
	info := omniUserDiscountFixture(t)
	require.NoError(t, OmniPrepareUserDiscount(info))
	params := billingexpr.TokenParams{P: 1000, Len: 1000}
	public, err := billingexpr.ComputeTieredQuota(info.TieredBillingSnapshot, params)
	require.NoError(t, err)
	public.Clamp = &common.QuotaClamp{}
	result := OmniApplyUserDiscount(info, info.TieredBillingSnapshot, params, billingexpr.RequestInput{}, public)
	require.Equal(t, public.ActualQuotaAfterGroup, result.ActualQuotaAfterGroup)
	require.False(t, info.OmniUserDiscount.Applied)
	require.Equal(t, "public_evaluation_clamped", info.OmniUserDiscount.AdminReason)
}

func TestOmniUserDiscountVisionCatalogAndGeneratedImageScope(t *testing.T) {
	info := omniUserDiscountFixture(t)
	common.OptionMap["OmniAPIModelRegistry"] = `{"enabled":true,"version":"discount-vision-registry","models":{"text-model":{"version":"test","capability":"text+image","bindings":[]}}}`
	info.OmniRegistryVersion = "discount-vision-registry"
	require.NoError(t, OmniPrepareUserDiscount(info))
	require.True(t, info.OmniUserDiscount.Applicable, "Sonnet text+image models remain eligible")
	info = omniUserDiscountFixture(t)
	info.Request = &dto.ImageRequest{Model: "text-model", Size: "1024x1024", Quality: "low"}
	require.NoError(t, OmniPrepareUserDiscount(info))
	require.False(t, info.OmniUserDiscount.Applicable, "image generation must be excluded despite an erroneous text catalog capability")
}
