package service

import (
	"encoding/json"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestOmniProtocolDefaultOutputDoesNotBorrowOtherProtocolMaximum(t *testing.T) {
	_, info := actualAllowanceFixture(t)
	registry := model.OmniModelRegistry{Enabled: true, Version: "protocol-output-default-fixture", Models: map[string]model.OmniModelIdentity{"test-model": {Version: "test", Capability: "text", Bindings: []model.OmniModelBinding{
		{ChannelID: 7, UpstreamModel: "test-model", Status: "verified", Protocols: []string{"openai"}, AcceptedOutputTokens: 262144, OutputLimitEvidence: strings.Repeat("a", 64)},
		{ChannelID: 8, UpstreamModel: "test-model", Status: "verified", Protocols: []string{"responses"}, AcceptedOutputTokens: 65536, OutputLimitEvidence: strings.Repeat("a", 64)},
	}}}}
	raw, _ := json.Marshal(registry)
	common.OptionMapRWMutex.Lock()
	old := common.OptionMap["OmniAPIModelRegistry"]
	common.OptionMap["OmniAPIModelRegistry"] = string(raw)
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap["OmniAPIModelRegistry"] = old
		common.OptionMapRWMutex.Unlock()
	})
	info.Request = &dto.OpenAIResponsesRequest{}
	info.RequestURLPath = "/v1/responses"
	info.OmniRegistryVersion = registry.Version
	OmniEnsureOutputLimit(info)
	require.Equal(t, 65536, OmniReservedOutput(info))
}

func TestOmniProtocolDefaultHonorsVerifiedSmallMaximumAndExplicitValue(t *testing.T) {
	_, info := actualAllowanceFixture(t)
	registry := model.OmniModelRegistry{Enabled: true, Version: "small-output-default-fixture", Models: map[string]model.OmniModelIdentity{"test-model": {Version: "test", Capability: "text", Bindings: []model.OmniModelBinding{{ChannelID: 7, UpstreamModel: "test-model", Status: "verified", Protocols: []string{"openai"}, AcceptedOutputTokens: 1024, OutputLimitEvidence: strings.Repeat("a", 64)}}}}}
	raw, _ := json.Marshal(registry)
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
	info.RequestURLPath = "/v1/chat/completions"
	info.Request = &dto.GeneralOpenAIRequest{}
	OmniEnsureOutputLimit(info)
	require.Equal(t, 1024, OmniReservedOutput(info))
	explicit := uint(100)
	info.Request = &dto.GeneralOpenAIRequest{MaxTokens: &explicit}
	OmniEnsureOutputLimit(info)
	require.Equal(t, 100, OmniReservedOutput(info))
}
