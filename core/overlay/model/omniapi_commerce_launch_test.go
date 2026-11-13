package model

import (
	"encoding/json"
	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

func commerceLaunchTestProfile(t *testing.T, p OmniCommerceProfile) OmniCommerceProfile {
	routing := map[string][]int{}
	prices := map[string]any{}
	matrix := OmniCapabilityMatrix{AllExistingCapabilities: true, InventoryHash: strings.Repeat("1", 64), Bounds: map[string]map[int]map[string]OmniCapabilityBound{}}
	for _, name := range OmniCommerceLaunchModels() {
		routing[name] = []int{7, 8}
		prices[name] = map[string]any{"costs": map[string]any{"7": map[string]any{"complete_prices_verified": true}, "8": map[string]any{"complete_prices_verified": true}}}
		matrix.Bounds[name] = map[int]map[string]OmniCapabilityBound{}
		for _, id := range []int{7, 8} {
			matrix.Bounds[name][id] = map[string]OmniCapabilityBound{"/v1/chat/completions": {MaxQuota: 10000, MaxRequestBytes: 1000000, MaxOutputTokens: 4096, ProviderHardCapEvidence: "SYNTHETIC TEST ONLY", AcceptanceHash: strings.Repeat("2", 64), VerifiedAt: time.Now().Unix()}}
		}
	}
	commerceOption(t, "OmniAPIRouting", map[string]any{"models": routing})
	commerceOption(t, "OmniAPIPricing", map[string]any{"enabled": true, "models": prices})
	matrix.PricingHash = omniOptionHash("OmniAPIPricing")
	matrix.RoutingHash = omniOptionHash("OmniAPIRouting")
	for name, channels := range matrix.Bounds {
		for id, paths := range channels {
			base := paths["/v1/chat/completions"]
			for _, path := range []string{"/v1/responses", "/v1/messages", "/v1/images/generations"} {
				matrix.Bounds[name][id][path] = base
			}
		}
	}
	raw, e := json.Marshal(matrix)
	require.NoError(t, e)
	p.CapabilityMatrix = string(raw)
	raw, e = json.Marshal(routing)
	require.NoError(t, e)
	p.Models = string(raw)
	return p
}

func TestOmniCommerceRestrictedSharedNeedsExplicitScopeAndLocalControls(t *testing.T) {
	p := commerceFixture(t)
	var matrix OmniCapabilityMatrix
	require.NoError(t, json.Unmarshal([]byte(p.CapabilityMatrix), &matrix))
	matrix.AllExistingCapabilities = false
	matrix.SharedScope = "bounded_text"
	matrix.ScopeApproval = strings.Repeat("a", 64)
	for name, channels := range matrix.Bounds {
		for id, paths := range channels {
			for path, bound := range paths {
				bound.ProviderHardCapEvidence = ""
				bound.LocalControlEvidence = "SYNTHETIC TEST ONLY: strict local text/output controls"
				matrix.Bounds[name][id][path] = bound
			}
		}
	}
	set := func() { raw, _ := json.Marshal(matrix); p.CapabilityMatrix = string(raw) }
	set()
	require.NoError(t, OmniCommerceLaunchReady(p))
	matrix.ScopeApproval = ""
	set()
	require.Error(t, OmniCommerceLaunchReady(p))
	matrix.ScopeApproval = strings.Repeat("a", 64)
	delete(matrix.Bounds["claude-fable-5"][7], "/v1/chat/completions")
	set()
	require.Error(t, OmniCommerceLaunchReady(p))
}

func TestOmniCommerceMultiplierEstimateDoesNotForgeProcurementVerification(t *testing.T) {
	p := commerceFixture(t)
	p.ID = "synthetic-operator-estimate"
	common.OptionMapRWMutex.Lock()
	var prices map[string]any
	require.NoError(t, json.Unmarshal([]byte(common.OptionMap["OmniAPIPricing"]), &prices))
	for _, row := range prices["models"].(map[string]any) {
		for _, cost := range row.(map[string]any)["costs"].(map[string]any) {
			cost.(map[string]any)["complete_prices_verified"] = false
		}
	}
	raw, _ := json.Marshal(prices)
	common.OptionMap["OmniAPIPricing"] = string(raw)
	common.OptionMapRWMutex.Unlock()
	require.Error(t, OmniCommerceApprove(p))
	p.Evidence = `{"worksheet":{"cost_basis":"platform_multiplier_estimate","estimation_authorization":{"mode":"platform_multiplier_estimate","approved":true,"source":"synthetic operator","operator_instruction":"use current multipliers"}}}`
	require.True(t, p.UsesMultiplierEstimate())
	require.NoError(t, OmniCommerceApprove(p))
	common.OptionMapRWMutex.RLock()
	require.Contains(t, common.OptionMap["OmniAPIPricing"], `"complete_prices_verified":false`)
	common.OptionMapRWMutex.RUnlock()
	p.Evidence = strings.Replace(p.Evidence, `"approved":true`, `"approved":false`, 1)
	require.False(t, p.UsesMultiplierEstimate())
}

func TestOmniCommerceFrozen32ScopeDoesNotAutoIncludeExcludedModels(t *testing.T) {
	names := OmniCommerceLaunchModels()
	require.Len(t, names, 32)
	p := commerceFixture(t)
	for _, name := range []string{"deepseek-v4-flash", "deepseek-v4-pro", "grok-4.7-build-fast", "gemini-3.6-flash", "gpt-image-2-pro", "gemini-3.1-flash-image-preview"} {
		require.NotContains(t, names, name)
		require.False(t, p.Allows(name, 0))
	}
	var scope map[string][]int
	require.NoError(t, json.Unmarshal([]byte(p.Models), &scope))
	scope["new-model-not-approved"] = []int{7}
	raw, err := json.Marshal(scope)
	require.NoError(t, err)
	p.Models = string(raw)
	require.Error(t, OmniCommerceLaunchReady(p))
}
