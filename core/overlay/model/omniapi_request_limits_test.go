package model

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

func TestOmniRequestLimitEvidenceDoesNotClaimUntestedPhysicalMaximum(t *testing.T) {
	row := OmniRequestLimits{MaxOutputTokens: 128000, Basis: "accepted_parameters", EvidenceHash: strings.Repeat("a", 64), VerifiedAt: time.Now().Unix()}
	require.NoError(t, omniValidateRequestLimits(map[string]OmniRequestLimits{"/v1/messages": row}))
	row.FullOutputVerified = true
	require.Error(t, omniValidateRequestLimits(map[string]OmniRequestLimits{"/v1/messages": row}))
	row.FullOutputVerified = false
	row.Basis = "supplier_documented"
	require.Error(t, omniValidateRequestLimits(map[string]OmniRequestLimits{"/v1/messages": row}))
	row.Sources = []string{"https://provider.example/docs/model"}
	require.NoError(t, omniValidateRequestLimits(map[string]OmniRequestLimits{"/v1/messages": row}))
	row.Sources = []string{"https://provider.example/docs?api_key=secret"}
	require.Error(t, omniValidateRequestLimits(map[string]OmniRequestLimits{"/v1/messages": row}))
}

func TestOmniRequestLimitsLegacyDefaultsUseApprovedExactProtocol(t *testing.T) {
	p := commerceFixture(t)
	var matrix OmniCapabilityMatrix
	require.NoError(t, json.Unmarshal([]byte(p.CapabilityMatrix), &matrix))
	bound := matrix.Bounds["gpt-6.1-sol"][7]["/v1/chat/completions"]
	bound.MaxOutputTokens = 65536
	matrix.Bounds["gpt-6.1-sol"][7]["/v1/responses"] = bound
	bound.MaxOutputTokens = 262144
	matrix.Bounds["gpt-6.1-sol"][7]["/v1/chat/completions"] = bound
	encoded, _ := json.Marshal(matrix)
	p.CapabilityMatrix = string(encoded)
	commerceOption(t, "OmniAPICommerceCapabilities", OmniCommerceCapabilityRelease{Version: "path-output-fixture", ApprovedBy: 1, ApprovedAt: time.Now().Unix(), EvidenceHash: strings.Repeat("a", 64), Profile: p})
	b := OmniModelBinding{ChannelID: 7, AcceptedOutputTokens: 262144, Protocols: []string{"openai"}}
	output, ok := b.AcceptedOutputForPath("gpt-6.1-sol", "/v1/responses")
	require.True(t, ok)
	require.EqualValues(t, 65536, output)
	_, ok = b.AcceptedOutputForPath("gpt-6.1-sol", "/v1/unknown")
	require.False(t, ok)
}

func TestOmniRequestLimitsKeepInputTotalAndOutputSeparate(t *testing.T) {
	row := OmniRequestLimits{MaxInputTokens: 600000, MaxContextTokens: 872000, MaxOutputTokens: 272000, Basis: "boundary_verified", EvidenceHash: strings.Repeat("b", 64), VerifiedAt: time.Now().Unix()}
	binding := OmniModelBinding{RequestLimits: map[string]OmniRequestLimits{"/v1/responses": row}}
	got, ok := binding.LimitsFor("/v1/responses")
	require.True(t, ok)
	require.EqualValues(t, 600000, got.MaxInputTokens)
	_, ok = binding.LimitsFor("/v1/chat/completions")
	require.False(t, ok)
	row.MaxOutputTokens = 900000
	binding.RequestLimits["/v1/responses"] = row
	_, ok = binding.LimitsFor("/v1/responses")
	require.False(t, ok)
}
