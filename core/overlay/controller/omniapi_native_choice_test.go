package controller

import (
	"encoding/json"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOmniPrimarySharedBootstrapUsesMatchingRequestEnvelope(t *testing.T) {
	c, info := omniPrimaryFixture(t)
	base := "https://synthetic-large-envelope.invalid"
	require.NoError(t, model.DB.Create(&model.Channel{Id: 7, Type: constant.ChannelTypeAnthropic, Status: 1, Key: "synthetic", BaseURL: &base, Models: "test-model", Group: "default"}).Error)
	policy := service.GetOmniRouting()
	policy.Models["test-model"] = []int{6, 7}
	raw, _ := json.Marshal(policy)
	common.OptionMap["OmniAPIRouting"] = string(raw)
	model.InitChannelCache()
	info.OmniPoolID = "synthetic-pool"
	info.OmniSharedDispatchBounds = map[int]relaycommon.OmniSharedDispatchBound{7: {MaxRequestBytes: 1572864, MaxOutputTokens: 128000}}
	selected, managed, err := omniPrimary(c, info)
	require.True(t, managed)
	require.Nil(t, err)
	require.NotNil(t, selected)
	require.Equal(t, 7, selected.Id, "bootstrap must respect the request's frozen eligible shared routes")
}

func TestOmniNativeBootstrapLetsSupplierDecideContextAndOutput(t *testing.T) {
	c, info := omniPrimaryFixture(t)
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	info.RequestURLPath = "/v1/messages"
	base := "https://synthetic-larger-context.invalid"
	require.NoError(t, model.DB.Create(&model.Channel{Id: 7, Type: constant.ChannelTypeAnthropic, Status: 1, Key: "synthetic", BaseURL: &base, Models: "test-model", Group: "default"}).Error)
	policy := service.GetOmniRouting()
	policy.Models["test-model"] = []int{6, 7}
	raw, _ := json.Marshal(policy)
	common.OptionMap["OmniAPIRouting"] = string(raw)
	registry := model.OmniModelRegistry{Enabled: true, Version: "native-total-context", Models: map[string]model.OmniModelIdentity{"test-model": {Version: "test", Capability: "text"}}}
	identity := registry.Models["test-model"]
	for _, cid := range []int{6, 7} {
		size, output := int64(2000), int64(600)
		if cid == 7 {
			size, output = 5000, 1000
		}
		identity.Bindings = append(identity.Bindings, model.OmniModelBinding{ChannelID: cid, Status: "verified", UpstreamModel: "test-model", Protocols: []string{"claude"}, RequestLimits: map[string]model.OmniRequestLimits{"/v1/messages": {MaxContextTokens: size, MaxOutputTokens: output, Basis: "boundary_verified", EvidenceHash: fmt.Sprintf("%064d", cid), VerifiedAt: time.Now().Unix()}}})
	}
	registry.Models["test-model"] = identity
	raw, _ = json.Marshal(registry)
	common.OptionMap["OmniAPIModelRegistry"] = string(raw)
	model.InitChannelCache()
	output := uint(500)
	info.Request = &dto.ClaudeRequest{Model: "test-model", MaxTokens: &output}
	info.OmniRegistryVersion = registry.Version
	info.SetEstimatePromptTokens(1700)
	selected, managed, err := omniPrimary(c, info)
	require.True(t, managed)
	require.Nil(t, err)
	require.Equal(t, 6, selected.Id)
	output = 1_000_000
	selected, _, err = omniPrimary(c, info)
	require.Nil(t, err)
	require.Equal(t, 6, selected.Id)
}

func TestOmniNativeForcedChoiceSelectsCompatibleBootstrap(t *testing.T) {
	c, info := omniPrimaryFixture(t)
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	base := "https://synthetic-compatible.invalid"
	require.NoError(t, model.DB.Create(&model.Channel{Id: 7, Type: constant.ChannelTypeOpenAI, Status: 1, Key: "synthetic", BaseURL: &base, Models: "test-model", Group: "default"}).Error)
	policy := service.GetOmniRouting()
	policy.Models["test-model"] = []int{6, 7}
	raw, _ := json.Marshal(policy)
	common.OptionMap["OmniAPIRouting"] = string(raw)
	registry := model.OmniModelRegistry{Enabled: true, Version: fmt.Sprintf("native-choice-%d", time.Now().UnixNano()), Models: map[string]model.OmniModelIdentity{"test-model": {Version: "v1", Capability: "text", Bindings: []model.OmniModelBinding{
		{ChannelID: 6, UpstreamModel: "test-model", Status: "verified", Protocols: []string{"claude"}, Supports: []string{"tools", "native_tool_choice_auto_only"}},
		{ChannelID: 7, UpstreamModel: "test-model", Status: "verified", Protocols: []string{"claude", "openai"}, Supports: []string{"tools"}},
	}}}}
	raw, _ = json.Marshal(registry)
	common.OptionMap["OmniAPIModelRegistry"] = string(raw)
	model.InitChannelCache()
	c.Set("omni_registry_version", registry.Version)
	info.Request = &dto.ClaudeRequest{Model: "test-model", ToolChoice: map[string]any{"type": "tool", "name": "ping"}}
	selected, managed, err := omniPrimary(c, info)
	require.True(t, managed)
	require.Nil(t, err)
	require.Equal(t, 7, selected.Id, "native force must skip an explicitly auto-only provider")
	require.Equal(t, constant.ChannelTypeOpenAI, info.ChannelType, "verified native transport must not bypass the coordinator")
	info.Request = &dto.ClaudeRequest{Model: "test-model", ToolChoice: map[string]any{"type": "auto"}}
	selected, _, err = omniPrimary(c, info)
	require.Nil(t, err)
	require.Equal(t, 6, selected.Id, "auto retains its working primary")
	require.Equal(t, constant.ChannelTypeOpenAI, info.ChannelType, "native primary must still enter the coordinator")
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	info.Request = &dto.GeneralOpenAIRequest{Model: "test-model", ToolChoice: map[string]any{"type": "function", "function": map[string]any{"name": "ping"}}}
	selected, _, err = omniPrimary(c, info)
	require.Nil(t, err)
	require.Equal(t, 6, selected.Id, "Chat compatibility retains its primary")
}

func TestOmniNativeHostedToolSelectsVerifiedBootstrap(t *testing.T) {
	c, info := omniPrimaryFixture(t)
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	base := "https://synthetic-search.invalid"
	require.NoError(t, model.DB.Create(&model.Channel{Id: 7, Type: constant.ChannelTypeOpenAI, Status: 1, Key: "synthetic", BaseURL: &base, Models: "test-model", Group: "default"}).Error)
	policy := service.GetOmniRouting()
	policy.Models["test-model"] = []int{6, 7}
	raw, _ := json.Marshal(policy)
	common.OptionMap["OmniAPIRouting"] = string(raw)
	registry := model.OmniModelRegistry{Enabled: true, Version: fmt.Sprintf("native-search-%d", time.Now().UnixNano()), Models: map[string]model.OmniModelIdentity{"test-model": {Version: "v1", Capability: "text", Bindings: []model.OmniModelBinding{
		{ChannelID: 6, UpstreamModel: "test-model", Status: "verified", Protocols: []string{"claude"}, Supports: []string{"tools"}},
		{ChannelID: 7, UpstreamModel: "test-model", Status: "verified", Protocols: []string{"claude", "openai"}, Supports: []string{"tools", "native_web_search"}},
	}}}}
	raw, _ = json.Marshal(registry)
	common.OptionMap["OmniAPIModelRegistry"] = string(raw)
	model.InitChannelCache()
	info.Request = &dto.ClaudeRequest{Model: "test-model", Tools: []map[string]any{{"type": "web_search_20250305", "name": "web_search", "max_uses": 1}}}
	selected, managed, err := omniPrimary(c, info)
	require.True(t, managed)
	require.Nil(t, err)
	require.Equal(t, 7, selected.Id, "native search must use an actually verified search route")
	info.Request = &dto.ClaudeRequest{Model: "test-model"}
	selected, _, err = omniPrimary(c, info)
	require.Nil(t, err)
	require.Equal(t, 6, selected.Id, "ordinary native traffic retains its working primary")
}
