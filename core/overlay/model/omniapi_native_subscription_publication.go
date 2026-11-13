package model

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
)

// A native subscription supplement enrolls already verified wallet routes.
// It cannot change their order, procurement rates, or protocol bindings.
func omniValidateNativeSubscriptionPublication(input OmniCapabilityPublicationInput, before map[string]string, registry OmniModelRegistry, oldScope, scope map[string][]int, oldMatrix, matrix OmniCapabilityMatrix, worstCostPPM int64) error {
	const native = "/v1/messages"
	if input.Pricing != before["OmniAPIPricing"] || input.Routing != before["OmniAPIRouting"] ||
		input.Registry != before["OmniAPIModelRegistry"] || len(oldScope) != len(scope) ||
		!slices.Equal(oldMatrix.ApprovedModels, matrix.ApprovedModels) {
		return fmt.Errorf("原生订阅发布不能改变模型、线路或价格")
	}
	var routing struct {
		Models map[string][]int                   `json:"models"`
		Costs  map[string]map[int]json.RawMessage `json:"costs"`
	}
	var pricing struct {
		Models map[string]omniPrimaryPrice `json:"models"`
	}
	if json.Unmarshal([]byte(input.Routing), &routing) != nil || json.Unmarshal([]byte(input.Pricing), &pricing) != nil {
		return fmt.Errorf("原生订阅发布缺少线路或采购成本")
	}
	markers := make(map[string]int, len(oldMatrix.NativeMessageOnly)+len(input.NativeSubscriptionRoutes))
	maps.Copy(markers, oldMatrix.NativeMessageOnly)
	for name, old := range oldScope {
		expected := old
		if id, ok := input.NativeSubscriptionRoutes[name]; ok {
			routes := routing.Models[name]
			if id <= 0 || len(routes) == 0 || routes[0] != id || slices.Contains(old, id) || markers[name] != 0 {
				return fmt.Errorf("%s 原生订阅通道必须是尚未纳入订阅的现有首选", name)
			}
			expected = append([]int{id}, old...)
			markers[name] = id
		}
		if !slices.Equal(scope[name], expected) || len(matrix.Bounds[name]) != len(oldMatrix.Bounds[name])+len(expected)-len(old) {
			return fmt.Errorf("%s 原生订阅发布必须保留既有备用范围", name)
		}
	}
	if !maps.Equal(matrix.NativeMessageOnly, markers) {
		return fmt.Errorf("原生订阅发布的能力范围与声明不符")
	}
	for name, id := range input.NativeSubscriptionRoutes {
		if len(oldScope[name]) == 0 {
			return fmt.Errorf("%s 缺少既有订阅线路", name)
		}
		proof, ok := matrix.Bounds[name][id][native]
		if !ok || len(matrix.Bounds[name][id]) != 1 || proof.AcceptanceHash != input.EvidenceHash ||
			!proof.RequestBasedReservation || proof.InputImages || proof.InputOverheadTokens <= 0 {
			return fmt.Errorf("%s 缺少受限原生订阅预留证书", name)
		}
		verified := false
		for _, binding := range registry.Models[name].Bindings {
			limits, hard := binding.HardLimitsFor(native)
			if binding.ChannelID == id && binding.UpstreamModel == name && binding.Status == "verified" &&
				binding.VerifiedPathsOnly && slices.Equal(binding.Protocols, []string{"claude"}) &&
				len(binding.RequestLimits) == 1 && hard && limits.MaxRequestBytes > 0 && limits.MaxOutputTokens > 0 &&
				proof.MaxRequestBytes <= limits.MaxRequestBytes && proof.MaxOutputTokens <= limits.MaxOutputTokens {
				verified = true
				break
			}
		}
		if !verified {
			return fmt.Errorf("%s 缺少原生协议验收绑定", name)
		}
		price := pricing.Models[name]
		if !omniJSONSame(price.Costs[id], routing.Costs[name][id]) {
			return fmt.Errorf("%s 采购价与线路成本不一致", name)
		}
		if err := omniValidatePrimaryCost(price.Costs[id], price, math.Min(.8, float64(worstCostPPM)/1000000)); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}
