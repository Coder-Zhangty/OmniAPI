package model

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

func TestOmniCommercePublicLimitsKeepRealPairsAndHideProcurement(t *testing.T) {
	p := commerceFixture(t)
	var matrix OmniCapabilityMatrix
	require.NoError(t, json.Unmarshal([]byte(p.CapabilityMatrix), &matrix))
	for id, paths := range matrix.Bounds["gpt-6.1-sol"] {
		for path, bound := range paths {
			if id == 7 {
				bound.MaxRequestBytes = 4096
				bound.MaxOutputTokens = 1024
			} else {
				bound.MaxRequestBytes = 1024
				bound.MaxOutputTokens = 4096
			}
			paths[path] = bound
		}
	}
	raw, _ := json.Marshal(matrix)
	p.CapabilityMatrix = string(raw)
	require.NoError(t, DB.Model(&OmniCommerceProfile{}).Where("id = ?", p.ID).Update("capability_matrix", p.CapabilityMatrix).Error)
	commerceOption(t, "OmniAPIModelRegistry", OmniModelRegistry{Enabled: true, Version: "synthetic-public-limits", Models: map[string]OmniModelIdentity{"gpt-6.1-sol": {Version: "test", Capability: "text", Bindings: []OmniModelBinding{{ChannelID: 7, Status: "verified", UpstreamModel: "gpt-6.1-sol", Protocols: []string{"openai"}, ContextLength: 8192}, {ChannelID: 8, Status: "verified", UpstreamModel: "gpt-6.1-sol", Protocols: []string{"openai"}, ContextLength: 8192}}}}})
	result := OmniPublicCapabilities()
	rows := result["models"].(map[string]any)
	row := rows["gpt-6.1-sol"].(map[string]any)
	require.Equal(t, false, row["provider_physical_maximum_verified"])
	limits := row["shared_request_limits"].([]map[string]any)
	require.NotEmpty(t, limits)
	for _, limit := range limits {
		require.False(t, limit["max_request_bytes"] == int64(4096) && limit["max_output_tokens"] == int64(4096), "never combine different routes' maxima into an unsupported pair")
		require.NotContains(t, limit, "channel_id")
		require.NotContains(t, limit, "max_quota")
	}
	encoded, _ := json.Marshal(result)
	require.NotContains(t, string(encoded), "procurement")
	require.NotContains(t, string(encoded), "worst_cost_ppm")
	require.NotContains(t, string(encoded), "fx_micros")
}

func TestOmniCommercePublicProtocolLimitsKeepProvenanceAndDoNotBorrowMaxima(t *testing.T) {
	commerceFixture(t)
	limits := map[string]OmniRequestLimits{
		"/v1/chat/completions": {MaxOutputTokens: 65536, Basis: "accepted_parameters", EvidenceHash: strings.Repeat("a", 64), VerifiedAt: time.Now().Unix()},
		"/v1/responses":        {MaxInputTokens: 600000, MaxContextTokens: 872000, MaxOutputTokens: 262144, Basis: "boundary_verified", EvidenceHash: strings.Repeat("b", 64), VerifiedAt: time.Now().Unix()},
	}
	commerceOption(t, "OmniAPIModelRegistry", OmniModelRegistry{Enabled: true, Version: "public-path-limits", Models: map[string]OmniModelIdentity{"gpt-6.1-sol": {Version: "test", Capability: "text", Bindings: []OmniModelBinding{{ChannelID: 7, Status: "verified", UpstreamModel: "gpt-6.1-sol", Protocols: []string{"openai", "responses"}, RequestLimits: limits, Procurement: map[string]float64{"input": 0.12345}}}}}})
	result := OmniPublicCapabilities()
	row := result["models"].(map[string]any)["gpt-6.1-sol"].(map[string]any)
	paths, ok := row["protocol_request_limits"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, paths, 2)
	for _, path := range paths {
		if path["path"] == "/v1/chat/completions" {
			require.EqualValues(t, 65536, path["max_output_tokens"])
			require.Equal(t, "accepted_parameters", path["basis"])
			require.Equal(t, false, path["full_output_verified"])
		}
		if path["path"] == "/v1/responses" {
			require.EqualValues(t, 872000, path["max_context_tokens"])
			require.EqualValues(t, 600000, path["max_input_tokens"])
		}
		require.NotContains(t, path, "channel_id")
		require.NotContains(t, path, "procurement")
	}
}

func TestOmniCommercePublicImageApprovalStaysWithItsExactEnvelope(t *testing.T) {
	p := commerceFixture(t)
	var matrix OmniCapabilityMatrix
	require.NoError(t, json.Unmarshal([]byte(p.CapabilityMatrix), &matrix))
	bound := matrix.Bounds["gpt-6.1-sol"][7]["/v1/chat/completions"]
	bound.InputImages = false
	matrix.Bounds["gpt-6.1-sol"][8] = map[string]OmniCapabilityBound{"/v1/chat/completions": bound}
	bound.InputImages = true
	matrix.Bounds["gpt-6.1-sol"][7] = map[string]OmniCapabilityBound{"/v1/chat/completions": bound}
	raw, _ := json.Marshal(matrix)
	p.CapabilityMatrix = string(raw)
	require.NoError(t, DB.Model(&OmniCommerceProfile{}).Where("id = ?", p.ID).Update("capability_matrix", p.CapabilityMatrix).Error)
	commerceOption(t, "OmniAPIModelRegistry", OmniModelRegistry{Enabled: true, Version: "public-image-envelope", Models: map[string]OmniModelIdentity{"gpt-6.1-sol": {Version: "test", Capability: "text", Bindings: []OmniModelBinding{
		{ChannelID: 7, Status: "verified", UpstreamModel: "gpt-6.1-sol", Protocols: []string{"openai"}, Supports: []string{"vision"}},
		{ChannelID: 8, Status: "verified", UpstreamModel: "gpt-6.1-sol", Protocols: []string{"openai"}, Supports: []string{"vision"}},
	}}}})
	result := OmniPublicCapabilities()
	row := result["models"].(map[string]any)["gpt-6.1-sol"].(map[string]any)
	require.Equal(t, true, row["shared_input_images"])
	require.Equal(t, "text_and_verified_inline_images", row["shared_scope"])
	limits := row["shared_request_limits"].([]map[string]any)
	require.Len(t, limits, 2, "otherwise identical envelopes with different media approval cannot collapse")
	count := 0
	for _, limit := range limits {
		if limit["input_images"] == true {
			count++
		}
		require.NotContains(t, limit, "channel_id")
	}
	require.Equal(t, 1, count)
}
