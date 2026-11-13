package service

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/require"
)

func TestOmniSharedTextScalesReservationWithoutChangingSoldProfile(t *testing.T) {
	_, info := actualAllowanceFixture(t)
	profile := model.OmniCommerceProfile{Models: `{"test-model":[7]}`, PricingHash: "pricing", RoutingHash: "routing"}
	bound := model.OmniCapabilityBound{
		MaxQuota: 9000, MaxRequestBytes: 4096, MaxOutputTokens: 1024,
		LocalControlEvidence: "synthetic frozen worst-price text envelope", AcceptanceHash: strings.Repeat("b", 64),
		VerifiedAt: time.Now().Unix(), RequestBasedReservation: true,
	}
	matrix := model.OmniCapabilityMatrix{
		SharedScope: "bounded_text", ScopeApproval: strings.Repeat("a", 64),
		PricingHash: profile.PricingHash, RoutingHash: profile.RoutingHash,
		Bounds: map[string]map[int]map[string]model.OmniCapabilityBound{
			"test-model": {7: {"/v1/responses": bound}},
		},
	}
	data, err := json.Marshal(matrix)
	require.NoError(t, err)
	profile.CapabilityMatrix = string(data)
	original := profile.CapabilityMatrix
	output := uint(1_000_000)
	request := &dto.OpenAIResponsesRequest{MaxOutputTokens: &output, Input: json.RawMessage(`"` + strings.Repeat("x", 10000) + `"`)}
	info.Request = request
	info.RequestURLPath, info.OmniPoolID = "/v1/responses", "synthetic-pool"
	info.OmniPriceProtected, info.OmniPriceExpression = true, "v1:tier(\"base\", p * 10 + c * 20)"
	require.NoError(t, omniValidateCertifiedShared(info, profile))
	require.True(t, info.OmniSharedDynamic)
	require.Greater(t, info.OmniSharedUpperQuota, bound.MaxQuota)
	require.EqualValues(t, 1_000_000, info.OmniSharedDispatchBounds[7].MaxOutputTokens)
	wire, err := json.Marshal(request)
	require.NoError(t, err)
	require.GreaterOrEqual(t, info.OmniSharedDispatchBounds[7].MaxRequestBytes, int64(len(wire)+4096))
	require.Equal(t, original, profile.CapabilityMatrix, "sold financial snapshots must remain intact")

	bound.MaxQuota = math.MaxInt64 / 2
	matrix.Bounds["test-model"][7]["/v1/responses"] = bound
	data, err = json.Marshal(matrix)
	require.NoError(t, err)
	profile.CapabilityMatrix = string(data)
	require.ErrorContains(t, omniValidateCertifiedShared(info, profile), "预留溢出")
}
