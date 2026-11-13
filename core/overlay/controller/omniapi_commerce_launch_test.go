package controller

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"strings"
	"testing"
	"time"
)

func commerceLaunchControllerProfile(t *testing.T, p model.OmniCommerceProfile) model.OmniCommerceProfile {
	t.Helper()
	routing := map[string][]int{}
	prices := map[string]any{}
	matrix := model.OmniCapabilityMatrix{AllExistingCapabilities: true, InventoryHash: strings.Repeat("1", 64), Bounds: map[string]map[int]map[string]model.OmniCapabilityBound{}}
	for _, name := range model.OmniCommerceLaunchModels() {
		routing[name] = []int{7}
		prices[name] = map[string]any{"costs": map[string]any{"7": map[string]any{"complete_prices_verified": true}}}
		matrix.Bounds[name] = map[int]map[string]model.OmniCapabilityBound{7: {"/v1/chat/completions": {MaxQuota: 10000, MaxRequestBytes: 1000000, MaxOutputTokens: 4096, ProviderHardCapEvidence: "SYNTHETIC TEST ONLY", AcceptanceHash: strings.Repeat("2", 64), VerifiedAt: time.Now().Unix()}}}
	}
	common.OptionMapRWMutex.Lock()
	raw, _ := json.Marshal(map[string]any{"models": routing})
	common.OptionMap["OmniAPIRouting"] = string(raw)
	matrix.RoutingHash = fmt.Sprintf("%x", sha256.Sum256(raw))
	raw, _ = json.Marshal(map[string]any{"enabled": true, "models": prices})
	common.OptionMap["OmniAPIPricing"] = string(raw)
	matrix.PricingHash = fmt.Sprintf("%x", sha256.Sum256(raw))
	common.OptionMapRWMutex.Unlock()
	for name, channels := range matrix.Bounds {
		for id, paths := range channels {
			base := paths["/v1/chat/completions"]
			for _, path := range []string{"/v1/responses", "/v1/messages", "/v1/images/generations"} {
				matrix.Bounds[name][id][path] = base
			}
		}
	}
	raw, _ = json.Marshal(matrix)
	p.CapabilityMatrix = string(raw)
	raw, _ = json.Marshal(routing)
	p.Models = string(raw)
	return p
}
