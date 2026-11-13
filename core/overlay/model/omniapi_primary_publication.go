package model

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

type omniPrimaryPrice struct {
	Routes []int                   `json:"routes"`
	Retail map[string]string       `json:"retail"`
	Costs  map[int]json.RawMessage `json:"costs"`
	Tiers  []struct {
		Retail map[string]string `json:"retail"`
	} `json:"tiers"`
}

// A primary publication only prepends evidenced channels. All existing retail
// expressions, route costs, model identities and fallback order are preserved.
func omniValidatePrimaryPublication(input OmniCapabilityPublicationInput, before map[string]string, oldRegistry, registry OmniModelRegistry, oldScope, scope map[string][]int, worstCostPPM int64) error {
	var oldRouting, routing struct {
		Models             map[string][]int                   `json:"models"`
		NativeClaudeModels map[string]bool                    `json:"native_claude_models"`
		Costs              map[string]map[int]json.RawMessage `json:"costs"`
	}
	var oldPricing, pricing struct {
		Models map[string]json.RawMessage `json:"models"`
		Quotes map[string]json.RawMessage `json:"pricing_quotes"`
	}
	for _, value := range []struct {
		raw string
		dst any
	}{
		{before["OmniAPIRouting"], &oldRouting}, {input.Routing, &routing},
		{before["OmniAPIPricing"], &oldPricing}, {input.Pricing, &pricing},
	} {
		if err := json.Unmarshal([]byte(value.raw), value.dst); err != nil {
			return err
		}
	}
	if !omniOnlyChanges(before["OmniAPIRouting"], input.Routing, "models", "costs", "native_claude_models") ||
		!omniOnlyChanges(before["OmniAPIPricing"], input.Pricing, "models", "pricing_quotes") ||
		!omniOnlyChanges(before["OmniAPIModelRegistry"], input.Registry, "models", "version") ||
		len(oldRouting.Models) != len(routing.Models) || len(oldPricing.Models) != len(pricing.Models) ||
		len(oldRegistry.Models) != len(registry.Models) || len(oldScope) != len(scope) {
		return fmt.Errorf("换线发布只能修改明确列出的既有模型首选线路")
	}
	for name, enabled := range oldRouting.NativeClaudeModels {
		if enabled && !routing.NativeClaudeModels[name] {
			return fmt.Errorf("不能移除原生调度：%s", name)
		}
	}
	for name, enabled := range routing.NativeClaudeModels {
		if enabled && !oldRouting.NativeClaudeModels[name] && (!strings.HasPrefix(name, "claude-") || input.PrimaryRoutes[name] <= 0) {
			return fmt.Errorf("原生调度尚未验收：%s", name)
		}
	}
	for name, oldRoutes := range oldRouting.Models {
		id, switching := input.PrimaryRoutes[name]
		expected := oldRoutes
		if switching {
			refresh := input.RefreshPrimaryRoutes[name] == id && id > 0
			if id <= 0 || len(oldRoutes) == 0 || (!refresh && slices.Contains(oldRoutes, id)) || (refresh && oldRoutes[0] != id) {
				return fmt.Errorf("%s 首选线路必须是新的已验收通道", name)
			}
			if !refresh {
				expected = append([]int{id}, oldRoutes...)
			}
		}
		if !slices.Equal(routing.Models[name], expected) {
			return fmt.Errorf("%s 必须保留既有备用顺序", name)
		}
	}
	for name, oldRoutes := range oldScope {
		id, switching := input.PrimaryRoutes[name]
		if input.RefreshPrimaryRoutes[name] == id && id > 0 && slices.Contains(oldRoutes, id) && !slices.Equal(scope[name], oldRoutes) {
			return fmt.Errorf("%s 已验收订阅线路顺序必须保留", name)
		}
		if !slices.Equal(scope[name], oldRoutes) && (!switching || !slices.Equal(scope[name], append([]int{id}, oldRoutes...))) {
			return fmt.Errorf("%s 不能改变既有订阅模型及备用通道", name)
		}
	}
	for name, id := range input.PrimaryRoutes {
		oldIdentity, known := oldRegistry.Models[name]
		identity := registry.Models[name]
		refresh := input.RefreshPrimaryRoutes[name] == id
		if refresh {
			oldBindings := make([]OmniModelBinding, 0, len(oldIdentity.Bindings))
			for _, binding := range oldIdentity.Bindings {
				if binding.ChannelID != id {
					oldBindings = append(oldBindings, binding)
				}
			}
			if len(oldBindings) != len(oldIdentity.Bindings)-1 {
				return fmt.Errorf("%s 缺少待复核绑定", name)
			}
			oldIdentity.Bindings = oldBindings
			delete(oldRouting.Costs[name], id)
		}
		if !known || len(routing.Models[name]) == 0 || routing.Models[name][0] != id || len(oldPricing.Models[name]) == 0 || len(identity.Bindings) != len(oldIdentity.Bindings)+1 {
			return fmt.Errorf("%s 缺少既有身份、新绑定或普通价", name)
		}
		bindings := make([]OmniModelBinding, 0, len(oldIdentity.Bindings))
		verified := false
		for _, binding := range identity.Bindings {
			if binding.ChannelID == id {
				verified = binding.Status == "verified" && binding.UpstreamModel == name && binding.VerifiedPathsOnly && len(binding.RequestLimits) > 0 && binding.Evidence != ""
				for path := range binding.RequestLimits {
					_, accepted := binding.AdmissionLimitsFor(path)
					verified = verified && accepted && (path == "/v1/chat/completions" || path == "/v1/messages" || path == "/v1/responses")
				}
				continue
			}
			bindings = append(bindings, binding)
		}
		identity.Bindings = bindings
		if !verified || !reflect.DeepEqual(identity, oldIdentity) {
			return fmt.Errorf("%s 缺少精确同名验收绑定或改动了既有绑定", name)
		}
		var price omniPrimaryPrice
		if json.Unmarshal(pricing.Models[name], &price) != nil || !slices.Equal(price.Routes, routing.Models[name]) ||
			!omniPrimaryRowMatches(oldPricing.Models[name], pricing.Models[name], id, refresh) {
			return fmt.Errorf("%s 采购报价与路由不一致或改动了普通价", name)
		}
		if quote := pricing.Quotes[name]; len(quote) != 0 || len(oldPricing.Quotes[name]) != 0 {
			var projection omniPrimaryPrice
			if json.Unmarshal(quote, &projection) != nil || !slices.Equal(projection.Routes, price.Routes) ||
				!omniJSONSame(projection.Costs[id], price.Costs[id]) || !omniPrimaryRowMatches(oldPricing.Quotes[name], quote, id, refresh) {
				return fmt.Errorf("%s 采购报价投影不一致", name)
			}
		}
		cost := price.Costs[id]
		if !omniJSONSame(cost, routing.Costs[name][id]) {
			return fmt.Errorf("%s 路由与定价的采购成本不一致", name)
		}
		limit := 0.8
		if slices.Contains(scope[name], id) {
			limit = math.Min(limit, float64(worstCostPPM)/1000000)
		}
		if err := omniValidatePrimaryCost(cost, price, limit); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		delete(routing.Costs[name], id)
		if len(routing.Costs[name]) == 0 && oldRouting.Costs[name] == nil {
			delete(routing.Costs, name)
		}
	}
	if !omniJSONSame(routing.Costs, oldRouting.Costs) {
		return fmt.Errorf("不能改动既有采购成本")
	}
	for name, identity := range oldRegistry.Models {
		if _, switching := input.PrimaryRoutes[name]; !switching && !reflect.DeepEqual(identity, registry.Models[name]) {
			return fmt.Errorf("%s 不在换线范围", name)
		}
	}
	for name, quote := range oldPricing.Quotes {
		if _, switching := input.PrimaryRoutes[name]; !switching && !omniJSONSame(quote, pricing.Quotes[name]) {
			return fmt.Errorf("%s 不在换线报价范围", name)
		}
	}
	if len(oldPricing.Quotes) != len(pricing.Quotes) {
		return fmt.Errorf("不能添加或移除既有采购报价投影")
	}
	return nil
}

func omniOnlyChanges(before, after string, keys ...string) bool {
	var old, next map[string]json.RawMessage
	if json.Unmarshal([]byte(before), &old) != nil || json.Unmarshal([]byte(after), &next) != nil || old == nil || next == nil {
		return false
	}
	for _, key := range keys {
		delete(old, key)
		delete(next, key)
	}
	return omniJSONSame(old, next)
}

func omniValidatePrimaryBounds(before, after OmniCapabilityMatrix, refreshed ...map[string]int) error {
	if before.SharedScope != after.SharedScope || before.ScopeApproval != after.ScopeApproval || before.AllExistingCapabilities != after.AllExistingCapabilities {
		return fmt.Errorf("换线不能更改订阅已批准能力范围")
	}
	for name, routes := range before.Bounds {
		for id, paths := range routes {
			if len(refreshed) > 0 && refreshed[0][name] == id {
				continue
			}
			if !reflect.DeepEqual(paths, after.Bounds[name][id]) {
				return fmt.Errorf("%s 换线不能改动既有订阅预留证书", name)
			}
		}
	}
	return nil
}

func omniValidateScopedChannelPaths(input OmniCapabilityPublicationInput, registry OmniModelRegistry, before, after OmniCapabilityMatrix) error {
	for name, channels := range before.ScopedChannelPaths {
		for id, paths := range channels {
			for _, path := range paths {
				if !slices.Contains(after.ScopedChannelPaths[name][id], path) {
					return fmt.Errorf("%s 不能撤销已售订阅线路的协议证书", name)
				}
			}
		}
	}
	for name, channels := range after.ScopedChannelPaths {
		for id, paths := range channels {
			if !slices.Equal(paths, before.ScopedChannelPaths[name][id]) && input.PrimaryRoutes[name] != id {
				return fmt.Errorf("%s 的受限订阅通道须随已验收主线路发布", name)
			}
			for _, path := range paths {
				verified := false
				for _, binding := range registry.Models[name].Bindings {
					_, accepted := binding.AdmissionLimitsFor(path)
					if binding.ChannelID == id && binding.Status == "verified" && binding.VerifiedPathsOnly && binding.UpstreamModel == name && accepted {
						verified = true
						break
					}
				}
				proof := after.Bounds[name][id][path]
				isNew := !slices.Contains(before.ScopedChannelPaths[name][id], path)
				if !verified || !proof.RequestBasedReservation || proof.InputImages || (isNew && proof.AcceptanceHash != input.EvidenceHash) {
					return fmt.Errorf("%s 的 %s 缺少独立协议或预留验收", name, path)
				}
			}
		}
	}
	return nil
}

func omniPrimaryRowMatches(before, after json.RawMessage, id int, refreshed ...bool) bool {
	var old, next map[string]json.RawMessage
	var oldCosts, nextCosts map[int]json.RawMessage
	if json.Unmarshal(before, &old) != nil || json.Unmarshal(after, &next) != nil || json.Unmarshal(next["costs"], &nextCosts) != nil {
		return false
	}
	if raw := old["costs"]; len(raw) != 0 && json.Unmarshal(raw, &oldCosts) != nil {
		return false
	}
	delete(nextCosts, id)
	if len(refreshed) > 0 && refreshed[0] {
		delete(oldCosts, id)
	}
	return (len(oldCosts) == 0 && len(nextCosts) == 0 || omniJSONSame(oldCosts, nextCosts)) && omniOnlyChanges(string(before), string(after), "routes", "costs")
}

func omniValidatePrimaryCost(raw json.RawMessage, price omniPrimaryPrice, maxCostRatio float64) error {
	var cost map[string]json.RawMessage
	if json.Unmarshal(raw, &cost) != nil || string(cost["complete_prices_verified"]) != "true" || maxCostRatio <= 0 {
		return fmt.Errorf("采购成本尚未完成实扣核验")
	}
	var tiers []struct {
		MinTokens int                        `json:"min_tokens"`
		MaxTokens *int                       `json:"max_tokens"`
		Prices    map[string]json.RawMessage `json:"prices"`
	}
	if data := cost["tiers"]; len(data) != 0 && json.Unmarshal(data, &tiers) != nil {
		return fmt.Errorf("采购阶梯无效")
	}
	prices := []map[string]json.RawMessage{cost}
	nextMin := 0
	for i, tier := range tiers {
		if tier.MinTokens != nextMin || tier.MaxTokens == nil && i != len(tiers)-1 || tier.MaxTokens != nil && (*tier.MaxTokens < tier.MinTokens || *tier.MaxTokens == math.MaxInt) {
			return fmt.Errorf("采购阶梯必须连续且不重叠")
		}
		if tier.MaxTokens != nil {
			nextMin = *tier.MaxTokens + 1
		}
		prices = append(prices, tier.Prices)
	}
	if len(tiers) != 0 && tiers[len(tiers)-1].MaxTokens != nil {
		return fmt.Errorf("采购阶梯缺少末档")
	}
	for _, dim := range []string{"input", "output", "cache_read", "cache_write", "cache_write_1h"} {
		retail, err := strconv.ParseFloat(price.Retail[dim], 64)
		if err != nil || retail < 0 || math.IsNaN(retail) || math.IsInf(retail, 0) {
			return fmt.Errorf("%s 零售维度未核实", dim)
		}
		for _, tier := range price.Tiers {
			v, err := strconv.ParseFloat(tier.Retail[dim], 64)
			if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
				return fmt.Errorf("%s 零售阶梯未核实", dim)
			}
			retail = math.Min(retail, v)
		}
		for _, row := range prices {
			var amount *float64
			if json.Unmarshal(row[dim+"_usd_per_million"], &amount) != nil || amount == nil || *amount < 0 || *amount > retail*maxCostRatio+1e-12 {
				return fmt.Errorf("%s 缺少采购实扣成本或低于毛利门槛", dim)
			}
		}
	}
	return nil
}
