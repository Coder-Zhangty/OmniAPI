package service

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

func OmniPrepareUserDiscount(info *relaycommon.RelayInfo) error {
	if info == nil || info.OmniUserDiscountCaptured {
		return nil
	}
	info.OmniUserDiscountCaptured = true
	policy := model.GetOmniUserDiscounts()
	rule, ok := policy.Users[strconv.Itoa(info.UserId)]
	if !ok || !rule.Enabled {
		return nil
	}
	if model.OmniUserHasLegacyPrivateBilling(info.UserId) || info.OmniPrivateCostMultiplier > 0 {
		return errors.New("用户计费规则冲突，请联系管理员")
	}
	frozen := &relaycommon.OmniUserDiscountSnapshot{Version: policy.Version, TargetMargin: rule.TargetMargin, Quote: append([]byte(nil), info.OmniPriceQuote...)}
	info.OmniUserDiscount = frozen
	registry := model.OmniRegistryAt(info.OmniRegistryVersion)
	identity, known := registry.Models[registry.Canonical(info.OriginModelName)]
	if !registry.Enabled || !known || (identity.Capability != "text" && identity.Capability != "text+image") || !omniUserDiscountTextRequest(info) {
		frozen.AdminReason = "model_scope_excluded"
		return nil
	}
	frozen.Applicable = true
	return nil
}

func omniUserDiscountTextRequest(info *relaycommon.RelayInfo) bool {
	switch info.Request.(type) {
	case *dto.GeneralOpenAIRequest, *dto.ClaudeRequest, *dto.OpenAIResponsesRequest, *dto.GeminiChatRequest:
	default:
		return false
	}
	data, err := json.Marshal(info.Request)
	if err != nil {
		return false
	}
	var raw map[string]any
	if json.Unmarshal(data, &raw) != nil || raw == nil {
		return false
	}
	// Image understanding is text input; generated images/audio are separate
	// products even when a provider's model catalog labels the model as text.
	var outputOnlyText func(map[string]any) bool
	outputOnlyText = func(row map[string]any) bool {
		if row["audio"] != nil {
			return false
		}
		for _, key := range []string{"modalities", "response_modalities", "responseModalities"} {
			if values, ok := row[key].([]any); ok {
				for _, v := range values {
					if v != "text" && v != "TEXT" {
						return false
					}
				}
			}
		}
		for _, key := range []string{"generationConfig", "generation_config"} {
			if child, ok := row[key].(map[string]any); ok && !outputOnlyText(child) {
				return false
			}
		}
		return true
	}
	return outputOnlyText(raw)
}

func omniDiscountPriceDimensions(raw json.RawMessage) bool {
	var v map[string]json.RawMessage
	if json.Unmarshal(raw, &v) != nil {
		return false
	}
	for _, dim := range []string{"input", "output", "cache_read", "cache_write", "cache_write_1h"} {
		var number *float64
		data, ok := v[dim+"_usd_per_million"]
		if !ok || json.Unmarshal(data, &number) != nil || number == nil || math.IsNaN(*number) || math.IsInf(*number, 0) || *number < 0 || ((dim == "input" || dim == "output") && *number <= 0) {
			return false
		}
	}
	return true
}

func omniDiscountVerifiedCost(raw json.RawMessage, inputLength float64) bool {
	var cost struct {
		Complete bool `json:"complete_prices_verified"`
		Tiers    []struct {
			Min    int             `json:"min_tokens"`
			Max    *int            `json:"max_tokens"`
			Prices json.RawMessage `json:"prices"`
		} `json:"tiers"`
	}
	if json.Unmarshal(raw, &cost) != nil || !cost.Complete || !omniDiscountPriceDimensions(raw) {
		return false
	}
	previousMax := -1
	for i, tier := range cost.Tiers {
		if tier.Min < 0 || !omniDiscountPriceDimensions(tier.Prices) || tier.Max != nil && *tier.Max < tier.Min || i > 0 && (previousMax < 0 || tier.Min <= previousMax) {
			return false
		}
		if tier.Max == nil {
			previousMax = -1
		} else {
			previousMax = *tier.Max
		}
	}
	return inputLength >= 0 && !math.IsNaN(inputLength) && !math.IsInf(inputLength, 0)
}

// The public calculation/reservation is preserved. Only its final token charge
// may be lowered, after the immutable funding source and winning channel exist.
func OmniApplyUserDiscount(info *relaycommon.RelayInfo, publicSnapshot *billingexpr.BillingSnapshot, params billingexpr.TokenParams, request billingexpr.RequestInput, publicResult billingexpr.TieredResult) billingexpr.TieredResult {
	if info == nil || publicSnapshot == nil {
		return publicResult
	}
	frozen := info.OmniUserDiscount
	if frozen == nil {
		return publicResult
	}
	frozen.Applied = false
	frozen.Settled = false
	frozen.PublicQuota = publicResult.ActualQuotaAfterGroup
	frozen.ActualQuota = publicResult.ActualQuotaAfterGroup
	if !frozen.Applicable {
		return publicResult
	}
	if info.BillingSource != "wallet" {
		frozen.AdminReason = "funding_source_excluded"
		return publicResult
	}
	if info.OmniPrivateCostMultiplier > 0 {
		frozen.AdminReason = "legacy_rule_conflict"
		return publicResult
	}
	if publicResult.Clamp != nil {
		frozen.AdminReason = "public_evaluation_clamped"
		return publicResult
	}
	if publicResult.BillingUnit != billingexpr.BillingUnitToken || params.ImgO > 0 || params.AI > 0 || params.AO > 0 {
		frozen.AdminReason = "usage_scope_excluded"
		return publicResult
	}
	var quote struct {
		Costs map[int]json.RawMessage `json:"costs"`
	}
	if json.Unmarshal(frozen.Quote, &quote) != nil {
		frozen.AdminReason = "procurement_unavailable"
		return publicResult
	}
	raw := quote.Costs[info.GetChannelID()]
	if !omniDiscountVerifiedCost(raw, params.Len) {
		frozen.AdminReason = "procurement_unverified_or_incomplete"
		return publicResult
	}
	expression, err := omniPrivateCostExpression(raw, 1/(1-frozen.TargetMargin))
	if err != nil {
		frozen.AdminReason = "procurement_expression_invalid"
		return publicResult
	}
	costSnapshot := *publicSnapshot
	costSnapshot.ExprString = omniPrivateWrap(expression)
	costSnapshot.ExprHash = billingexpr.ExprHashString(costSnapshot.ExprString)
	costSnapshot.GroupRatio = 1
	// Len contains the complete input in both Claude and OpenAI usage semantics.
	// Rebuild the uncached component independently of variables in the public
	// expression, including image-understanding input in ordinary input tokens.
	costParams := params
	if params.Len == 0 && params.P > 0 {
		frozen.AdminReason = "usage_dimensions_incomplete"
		return publicResult
	}
	costParams.P = math.Max(0, params.Len-params.CR-params.CC-params.CC1h)
	target, err := billingexpr.ComputeTieredQuotaWithRequest(&costSnapshot, costParams, request)
	if err != nil || target.Clamp != nil {
		frozen.AdminReason = "procurement_evaluation_failed"
		return publicResult
	}
	if target.ActualQuotaAfterGroup >= publicResult.ActualQuotaAfterGroup {
		frozen.AdminReason = "public_price_cap"
		return publicResult
	}
	result := publicResult
	// Text surcharge composition multiplies this field by the original public
	// snapshot's group ratio. Counterbalance it without changing that snapshot,
	// so tool surcharges retain their existing public-price treatment.
	result.ActualQuotaBeforeGroup = target.ActualQuotaBeforeGroup / publicSnapshot.GroupRatio
	result.ActualQuotaAfterGroup = target.ActualQuotaAfterGroup
	frozen.Applied = true
	frozen.ActualQuota = target.ActualQuotaAfterGroup
	frozen.AdminReason = "wallet_procurement_margin"
	return result
}

func OmniFinalizeUserDiscount(info *relaycommon.RelayInfo, success bool) {
	if info == nil || info.OmniUserDiscount == nil {
		return
	}
	frozen := info.OmniUserDiscount
	frozen.Settled = success && frozen.Applied && info.BillingSource == "wallet"
	if !success {
		frozen.AdminReason = "settlement_failed"
	} else if frozen.Settled {
		frozen.AdminReason = "wallet_procurement_margin"
	}
}

func OmniInjectUserDiscountInfo(other *model.LogOther, info *relaycommon.RelayInfo) {
	if other == nil || info == nil || info.OmniUserDiscount == nil {
		return
	}
	frozen := info.OmniUserDiscount
	if frozen.Applied && frozen.Settled && frozen.ActualQuota < frozen.PublicQuota {
		other.SetPublic("price_discount_applied", true)
	}
	other.SetAdmin("user_price_discount", map[string]any{"rule_version": frozen.Version, "funding_source": info.BillingSource, "reason": frozen.AdminReason, "applied": frozen.Applied && frozen.Settled})
	other.SetRoot("user_price_discount", map[string]any{"rule_version": frozen.Version, "target_margin": frozen.TargetMargin, "funding_source": info.BillingSource, "reason": frozen.AdminReason, "public_token_quota": frozen.PublicQuota, "actual_token_quota": frozen.ActualQuota, "applied": frozen.Applied && frozen.Settled})
}
