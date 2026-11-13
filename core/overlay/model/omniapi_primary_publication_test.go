package model

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOmniPrimaryPublicationPreservesFallbackAndFrozenSubscriptionBudget(t *testing.T) {
	const name = "gpt-6.1-sol"
	const retail = `{"input":"1","output":"1","cache_read":"1","cache_write":"1","cache_write_1h":"1"}`
	const cost = `{"complete_prices_verified":true,"input_usd_per_million":0.4,"output_usd_per_million":0.7,"cache_read_usd_per_million":0.2,"cache_write_usd_per_million":0.3,"cache_write_1h_usd_per_million":0.3}`
	const oldPricing = `{"models":{"gpt-6.1-sol":{"routes":[1,2],"retail":` + retail + `}}}`
	const oldRouting = `{"models":{"gpt-6.1-sol":[1,2]},"costs":{}}`
	const newPricing = `{"models":{"gpt-6.1-sol":{"routes":[9,1,2],"retail":` + retail + `,"costs":{"9":` + cost + `}}}}`
	const newRouting = `{"models":{"gpt-6.1-sol":[9,1,2]},"costs":{"gpt-6.1-sol":{"9":` + cost + `}}}`
	oldBinding := OmniModelBinding{ChannelID: 1, UpstreamModel: name, Status: "verified"}
	identity := OmniModelIdentity{Capability: "text", Bindings: []OmniModelBinding{oldBinding}}
	oldRegistry := OmniModelRegistry{Enabled: true, Version: "old", Models: map[string]OmniModelIdentity{name: identity}}
	bound := OmniRequestLimits{Basis: "boundary_verified", MaxInputTokens: 1024, EvidenceHash: strings.Repeat("b", 64), VerifiedAt: time.Now().Unix()}
	identity.Bindings = append(identity.Bindings, OmniModelBinding{ChannelID: 9, UpstreamModel: name, Status: "verified", VerifiedPathsOnly: true, Evidence: "owned probe", RequestLimits: map[string]OmniRequestLimits{"/v1/responses": bound}})
	registry := OmniModelRegistry{Enabled: true, Version: "new", Models: map[string]OmniModelIdentity{name: identity}}
	oldRegistryJSON, err := json.Marshal(oldRegistry)
	require.NoError(t, err)
	newRegistryJSON, err := json.Marshal(registry)
	require.NoError(t, err)
	before := map[string]string{"OmniAPIRouting": oldRouting, "OmniAPIPricing": oldPricing, "OmniAPIModelRegistry": string(oldRegistryJSON)}
	input := OmniCapabilityPublicationInput{Pricing: newPricing, Routing: newRouting, Registry: string(newRegistryJSON), PrimaryRoutes: map[string]int{name: 9}}
	oldScope := map[string][]int{name: {1, 2}}
	newScope := map[string][]int{name: {9, 1, 2}}
	require.NoError(t, omniValidatePrimaryPublication(input, before, oldRegistry, registry, oldScope, oldScope, 600000))
	require.ErrorContains(t, omniValidatePrimaryPublication(input, before, oldRegistry, registry, oldScope, newScope, 600000), "毛利门槛")
	require.NoError(t, omniValidatePrimaryPublication(input, before, oldRegistry, registry, oldScope, newScope, 750000))
	wrong := input
	wrong.Routing = strings.Replace(wrong.Routing, "[9,1,2]", "[9,2,1]", 1)
	require.ErrorContains(t, omniValidatePrimaryPublication(wrong, before, oldRegistry, registry, oldScope, oldScope, 600000), "备用顺序")
	wrong = input
	wrong.Pricing = strings.Replace(wrong.Pricing, `"output":"1"`, `"output":"2"`, 1)
	require.ErrorContains(t, omniValidatePrimaryPublication(wrong, before, oldRegistry, registry, oldScope, oldScope, 600000), "改动了普通价")
	wrong = input
	wrong.Pricing = strings.ReplaceAll(wrong.Pricing, `"cache_write_1h_usd_per_million":0.3`, `"cache_write_1h_usd_per_million":null`)
	wrong.Routing = strings.ReplaceAll(wrong.Routing, `"cache_write_1h_usd_per_million":0.3`, `"cache_write_1h_usd_per_million":null`)
	require.ErrorContains(t, omniValidatePrimaryPublication(wrong, before, oldRegistry, registry, oldScope, oldScope, 600000), "cache_write_1h")
}

func TestOmniScopedSubscriptionPathRequiresMatchingRouteAndCertificate(t *testing.T) {
	const name = "gpt-6-luna"
	const chat = "/v1/chat/completions"
	const responses = "/v1/responses"
	const id = 35
	evidence := strings.Repeat("a", 64)
	limit := OmniRequestLimits{Basis: "accepted_parameters", MaxOutputTokens: 4096, EvidenceHash: evidence, VerifiedAt: time.Now().Unix()}
	registry := OmniModelRegistry{Models: map[string]OmniModelIdentity{name: {Bindings: []OmniModelBinding{{
		ChannelID: id, UpstreamModel: name, Status: "verified", VerifiedPathsOnly: true,
		RequestLimits: map[string]OmniRequestLimits{chat: limit},
	}}}}}
	proof := OmniCapabilityBound{MaxQuota: 1000, MaxRequestBytes: 32768, MaxOutputTokens: 4096,
		AcceptanceHash: evidence, RequestBasedReservation: true, LocalControlEvidence: "isolated proof"}
	matrix := OmniCapabilityMatrix{
		SharedScope: "bounded_text", ScopeApproval: evidence,
		ScopedChannelPaths: map[string]map[int][]string{name: {id: {chat}}},
		Bounds:             map[string]map[int]map[string]OmniCapabilityBound{name: {id: {chat: proof}, 1: {responses: proof}}},
	}
	input := OmniCapabilityPublicationInput{PrimaryRoutes: map[string]int{name: id}, EvidenceHash: evidence}
	require.NoError(t, omniValidateScopedChannelPaths(input, registry, OmniCapabilityMatrix{}, matrix))
	require.Error(t, omniValidateScopedChannelPaths(OmniCapabilityPublicationInput{}, registry, OmniCapabilityMatrix{}, matrix))
	require.Error(t, omniValidateScopedChannelPaths(input, registry, matrix, OmniCapabilityMatrix{}))
	encoded, err := json.Marshal(matrix)
	require.NoError(t, err)
	profile := OmniCommerceProfile{CapabilityMatrix: string(encoded)}
	_, err = profile.capabilityFromSnapshot(name, id, chat)
	require.NoError(t, err)
	_, err = profile.capabilityFromSnapshot(name, id, responses)
	require.Error(t, err)
	require.True(t, omniCommerceHasPathFallback(profile, name, id, responses, []int{id, 1}))
	require.False(t, omniCommerceHasPathFallback(profile, name, id, responses, []int{id}))
}
