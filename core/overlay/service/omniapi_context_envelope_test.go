package service

import (
	"encoding/json"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

func TestOmniNewPrimaryRequiresAnAcceptedStatelessPath(t *testing.T) {
	_, info := actualAllowanceFixture(t)
	row := model.OmniRequestLimits{MaxInputTokens: 1024, MaxOutputTokens: 384, Basis: "boundary_verified", EvidenceHash: strings.Repeat("a", 64), VerifiedAt: time.Now().Unix()}
	registry := model.OmniModelRegistry{Enabled: true, Version: "scoped-primary-fixture", Models: map[string]model.OmniModelIdentity{"test-model": {Version: "test", Capability: "text", Bindings: []model.OmniModelBinding{{
		ChannelID: 9, Status: "verified", UpstreamModel: "test-model", Protocols: []string{"responses"}, VerifiedPathsOnly: true,
		RequestLimits: map[string]model.OmniRequestLimits{"/v1/responses": row},
	}}}}}
	raw, err := json.Marshal(registry)
	require.NoError(t, err)
	common.OptionMapRWMutex.Lock()
	old := common.OptionMap["OmniAPIModelRegistry"]
	common.OptionMap["OmniAPIModelRegistry"] = string(raw)
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap["OmniAPIModelRegistry"] = old
		common.OptionMapRWMutex.Unlock()
	})
	info.OmniRegistryVersion = registry.Version
	info.RequestURLPath = "/v1/responses"
	info.Request = &dto.OpenAIResponsesRequest{Store: json.RawMessage("false"), Input: json.RawMessage(`"test"`)}
	require.Equal(t, "route_output_budget_required", OmniDeclaredRequestReject(info, 9).Reason)
	limit := uint(128)
	info.Request = &dto.OpenAIResponsesRequest{Store: json.RawMessage("false"), MaxOutputTokens: &limit, Input: json.RawMessage(`"test"`)}
	require.Nil(t, OmniDeclaredRequestReject(info, 9))
	info.Request = &dto.OpenAIResponsesRequest{}
	require.Equal(t, "route_stateful_response_not_verified", OmniDeclaredRequestReject(info, 9).Reason)
	info.Request = &dto.OpenAIResponsesRequest{Store: json.RawMessage("false"), PreviousResponseID: "resp_existing"}
	require.Equal(t, "route_stateful_response_not_verified", OmniDeclaredRequestReject(info, 9).Reason)
	info.RequestURLPath = "/v1/chat/completions"
	require.Equal(t, "route_protocol_not_verified", OmniDeclaredRequestReject(info, 9).Reason)
}

func TestOmniKuaiResponsesWithoutClientInstructionsFallsThrough(t *testing.T) {
	_, info := actualAllowanceFixture(t)
	modelName := "gpt-6.1-sol"
	path := "/v1/responses"
	evidence := strings.Repeat("a", 64)
	registry := model.OmniModelRegistry{Enabled: true, Version: "kuai-explicit-instructions-fixture", Models: map[string]model.OmniModelIdentity{
		modelName: {Version: "test", Capability: "text", Bindings: []model.OmniModelBinding{
			{ChannelID: 9, Status: "verified", UpstreamModel: modelName, Protocols: []string{"responses"}, VerifiedPathsOnly: true,
				KuaiMetering: &model.OmniKuaiMeteringPolicy{EvidenceHash: evidence, MaxInputRatioPPM: 2_000_000, MaxInputOverhead: 128,
					RequireResponsesInstructions: true},
				RequestLimits: map[string]model.OmniRequestLimits{path: {MaxInputTokens: 128000, MaxOutputTokens: 128000,
					Basis: "accepted_parameters", EvidenceHash: evidence, VerifiedAt: time.Now().Unix()}}},
			{ChannelID: 10, Status: "verified", UpstreamModel: modelName, Protocols: []string{"responses"}},
		}},
	}}
	raw, err := json.Marshal(registry)
	require.NoError(t, err)
	common.OptionMapRWMutex.Lock()
	old := common.OptionMap["OmniAPIModelRegistry"]
	common.OptionMap["OmniAPIModelRegistry"] = string(raw)
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap["OmniAPIModelRegistry"] = old
		common.OptionMapRWMutex.Unlock()
	})
	info.OriginModelName, info.OmniRegistryVersion, info.RequestURLPath = modelName, registry.Version, path
	limit := uint(512)
	request := &dto.OpenAIResponsesRequest{Store: json.RawMessage("false"), MaxOutputTokens: &limit, Input: json.RawMessage(`"test"`)}
	info.Request = request
	for _, raw := range []string{"", `null`, `""`, `"\t "`, `[]`} {
		request.Instructions = json.RawMessage(raw)
		require.Equal(t, &model.OmniBindingRejection{Reason: "kuai_client_instructions_required", Field: "instructions"},
			OmniDeclaredRequestReject(info, 9))
		require.Nil(t, OmniDeclaredRequestReject(info, 10))
	}
	request.Instructions = json.RawMessage(`"exact client instructions"`)
	require.Nil(t, OmniDeclaredRequestReject(info, 9))
	// Independently scope the same binding to the stream-only billing evidence.
	streamRegistry := registry
	streamRegistry.Version += "-stream"
	streamRegistry.Models[modelName].Bindings[0].KuaiMetering.RequireResponsesStream = true
	encoded, err := json.Marshal(streamRegistry)
	require.NoError(t, err)
	common.OptionMapRWMutex.Lock()
	common.OptionMap["OmniAPIModelRegistry"] = string(encoded)
	common.OptionMapRWMutex.Unlock()
	info.OmniRegistryVersion = streamRegistry.Version
	require.Equal(t, &model.OmniBindingRejection{Reason: "kuai_stream_usage_required", Field: "stream"}, OmniDeclaredRequestReject(info, 9))
	require.Nil(t, OmniDeclaredRequestReject(info, 10))
	info.IsStream = true
	require.Nil(t, OmniDeclaredRequestReject(info, 9))
	request.PreviousResponseID = "resp_existing"
	require.Equal(t, "route_stateful_response_not_verified", OmniDeclaredRequestReject(info, 9).Reason)
}

func TestOmniChatForcedToolModeDoesNotUseAutoOnlyCandidate(t *testing.T) {
	_, info := actualAllowanceFixture(t)
	registry := model.OmniModelRegistry{Enabled: true, Version: "scoped-chat-tool-choice", Models: map[string]model.OmniModelIdentity{
		"test-model": {Version: "test", Capability: "text", Bindings: []model.OmniModelBinding{
			{ChannelID: 9, Status: "verified", UpstreamModel: "test-model", Protocols: []string{"openai"},
				Supports: []string{"tools", "chat_tool_choice_auto_only"}},
			{ChannelID: 10, Status: "verified", UpstreamModel: "test-model", Protocols: []string{"openai"}, Supports: []string{"tools"}},
		}},
	}}
	encoded, err := json.Marshal(registry)
	require.NoError(t, err)
	common.OptionMapRWMutex.Lock()
	previous := common.OptionMap["OmniAPIModelRegistry"]
	common.OptionMap["OmniAPIModelRegistry"] = string(encoded)
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap["OmniAPIModelRegistry"] = previous
		common.OptionMapRWMutex.Unlock()
	})
	info.OmniRegistryVersion, info.RequestURLPath = registry.Version, "/v1/chat/completions"
	for _, value := range []any{"required", map[string]any{"type": "function", "function": map[string]any{"name": "synthetic"}}} {
		info.Request = &dto.GeneralOpenAIRequest{ToolChoice: value}
		require.Equal(t, &model.OmniBindingRejection{Reason: "chat_tool_choice_not_verified", Field: "tool_choice"}, OmniDeclaredRequestReject(info, 9))
		require.Nil(t, OmniDeclaredRequestReject(info, 10))
	}
	info.Request = &dto.GeneralOpenAIRequest{ToolChoice: "auto"}
	require.Nil(t, OmniDeclaredRequestReject(info, 9))
}

func TestOmniSharedDispatchUsesHeldEnvelopeInsteadOfProviderTokenMetadata(t *testing.T) {
	_, info := actualAllowanceFixture(t)
	row := model.OmniRequestLimits{MaxContextTokens: 2000, MaxInputTokens: 1900, MaxOutputTokens: 600, Basis: "boundary_verified", EvidenceHash: strings.Repeat("a", 64), VerifiedAt: time.Now().Unix()}
	registry := model.OmniModelRegistry{Enabled: true, Version: "joint-context-fixture", Models: map[string]model.OmniModelIdentity{"test-model": {Version: "test", Capability: "text", Bindings: []model.OmniModelBinding{{ChannelID: 7, Status: "verified", UpstreamModel: "test-model", Protocols: []string{"openai"}, RequestLimits: map[string]model.OmniRequestLimits{"/v1/chat/completions": row}}}}}}
	common.OptionMapRWMutex.Lock()
	old := common.OptionMap["OmniAPIModelRegistry"]
	raw, _ := json.Marshal(registry)
	common.OptionMap["OmniAPIModelRegistry"] = string(raw)
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap["OmniAPIModelRegistry"] = old
		common.OptionMapRWMutex.Unlock()
	})
	info.OmniRegistryVersion = registry.Version
	info.OmniPoolID = "synthetic-pool"
	info.RequestURLPath = "/v1/chat/completions"
	info.OmniCustomerActual = true
	info.ChannelMeta = &relaycommon.ChannelMeta{ChannelId: 7}
	info.OmniSharedDispatchBounds = map[int]relaycommon.OmniSharedDispatchBound{7: {MaxRequestBytes: 65536, MaxOutputTokens: 4096}}
	for _, tc := range []struct {
		label    string
		input    int
		output   uint
		n        int
		path     string
		rejected bool
	}{
		{"joint overflow", 1700, 500, 1, "/v1/chat/completions", false},
		{"exact total", 1500, 500, 1, "/v1/chat/completions", false},
		{"one above total", 1500, 501, 1, "/v1/chat/completions", false},
		{"per candidate context", 1000, 500, 4, "/v1/chat/completions", false},
		{"input maximum", 1901, 1, 1, "/v1/chat/completions", false},
		{"output maximum", 100, 601, 1, "/v1/chat/completions", false},
		{"different protocol", 1700, 500, 1, "/v1/responses", false},
	} {
		t.Run(tc.label, func(t *testing.T) {
			output, n := tc.output, tc.n
			info.SetEstimatePromptTokens(tc.input)
			info.RequestURLPath = tc.path
			info.Request = &dto.GeneralOpenAIRequest{MaxTokens: &output, N: &n}
			body, _ := json.Marshal(info.Request)
			err := OmniValidateDispatchBody(info, body)
			if tc.rejected {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
	info.RequestURLPath = "/v1/chat/completions"
	info.SetEstimatePromptTokens(1500)
	value := uint(500)
	info.Request = &dto.GeneralOpenAIRequest{MaxTokens: &value}
	require.NoError(t, OmniValidateDispatchBody(info, []byte(`{"max_tokens":1000}`)))
	require.Error(t, OmniValidateDispatchBody(info, []byte(`{"max_tokens":1000000}`)), "wire output must fit the funds held for this request")
}

func TestOmniWalletTextLetsProviderDecideContextAndOutputLimits(t *testing.T) {
	_, info := actualAllowanceFixture(t)
	registry := model.OmniModelRegistry{Enabled: true, Version: "joint-default-fixture", Models: map[string]model.OmniModelIdentity{"test-model": {Version: "test", Capability: "text", Bindings: []model.OmniModelBinding{{ChannelID: 7, Status: "verified", UpstreamModel: "test-model", Protocols: []string{"openai"}, RequestLimits: map[string]model.OmniRequestLimits{"/v1/chat/completions": {MaxContextTokens: 2000, MaxInputTokens: 1900, MaxOutputTokens: 600, Basis: "boundary_verified", EvidenceHash: strings.Repeat("b", 64), VerifiedAt: time.Now().Unix()}}}}}}}
	common.OptionMapRWMutex.Lock()
	old := common.OptionMap["OmniAPIModelRegistry"]
	raw, _ := json.Marshal(registry)
	common.OptionMap["OmniAPIModelRegistry"] = string(raw)
	oldRoute := common.OptionMap["OmniAPIRouting"]
	common.OptionMap["OmniAPIRouting"] = `{"enabled":false}`
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap["OmniAPIModelRegistry"] = old
		common.OptionMap["OmniAPIRouting"] = oldRoute
		common.OptionMapRWMutex.Unlock()
	})
	info.OmniRegistryVersion = registry.Version
	info.RequestURLPath = "/v1/chat/completions"
	zero := uint(0)
	request := &dto.GeneralOpenAIRequest{MaxTokens: &zero}
	info.Request = request
	OmniEnsureOutputLimit(info)
	require.Nil(t, request.MaxTokens, "zero is unspecified and must not be forwarded alongside a positive default")
	require.True(t, info.OmniOutputDefaulted)
	info.SetEstimatePromptTokens(1800)
	changed, err := OmniAdjustDefaultOutputLimit(info)
	require.NoError(t, err)
	require.False(t, changed)
	require.EqualValues(t, 600, *request.MaxCompletionTokens)
	explicit := uint(1_000_000)
	request = &dto.GeneralOpenAIRequest{MaxTokens: &explicit}
	info.Request = request
	info.OmniOutputDefaulted = false
	info.SetEstimatePromptTokens(2500)
	info.ChannelMeta = &relaycommon.ChannelMeta{ChannelId: 7}
	OmniEnsureOutputLimit(info)
	changed, err = OmniAdjustDefaultOutputLimit(info)
	require.NoError(t, err)
	require.False(t, changed)
	require.EqualValues(t, 1_000_000, *request.MaxTokens)
	require.Nil(t, OmniBindingRequestReject(info, 7, nil))
	body, err := json.Marshal(request)
	require.NoError(t, err)
	require.NoError(t, OmniValidateDispatchBody(info, body))
}
