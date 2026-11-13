package service

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type kuaiWaiverBilling struct{ settled []int }

func (b *kuaiWaiverBilling) Settle(q int) error     { b.settled = append(b.settled, q); return nil }
func (*kuaiWaiverBilling) Refund(*gin.Context)      {}
func (*kuaiWaiverBilling) NeedsRefund() bool        { return false }
func (*kuaiWaiverBilling) GetPreConsumedQuota() int { return 100 }
func (*kuaiWaiverBilling) Reserve(int) error        { return nil }

func TestOmniKuaiInflatedUsageQuarantinesOnlyAffectedProtocolAndWaivesCharge(t *testing.T) {
	c, info := actualAllowanceFixture(t)
	require.NoError(t, model.DB.AutoMigrate(&model.OmniKuaiQuarantine{}))
	modelName := "gpt-6.1-terra"
	channelID := 95
	policy := &model.OmniKuaiMeteringPolicy{EvidenceHash: strings.Repeat("e", 64), MaxInputRatioPPM: 2_000_000, MaxInputOverhead: 128}
	registry := model.OmniModelRegistry{Enabled: true, Version: "kuai-metering-regression", Models: map[string]model.OmniModelIdentity{
		modelName: {Version: "fixture", Capability: "text", Bindings: []model.OmniModelBinding{{
			ChannelID: channelID, UpstreamModel: modelName, Status: "verified", Protocols: []string{"openai", "responses"}, VerifiedPathsOnly: true, KuaiMetering: policy,
		}}},
	}}
	raw, err := json.Marshal(registry)
	require.NoError(t, err)
	common.OptionMapRWMutex.Lock()
	previous := common.OptionMap["OmniAPIModelRegistry"]
	common.OptionMap["OmniAPIModelRegistry"] = string(raw)
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap["OmniAPIModelRegistry"] = previous
		common.OptionMapRWMutex.Unlock()
	})
	info.OriginModelName, info.OmniRegistryVersion = modelName, registry.Version
	info.ChannelMeta = &relaycommon.ChannelMeta{ChannelId: channelID, ApiKey: "synthetic"}
	info.RequestURLPath = "/v1/chat/completions"
	limit := uint(512)
	info.Request = &dto.GeneralOpenAIRequest{MaxCompletionTokens: &limit}
	info.IsStream = true
	info.SetEstimatePromptTokens(18)
	InitOmniRouteTrace(c)
	request, err := http.NewRequest("POST", "https://kuaiapi.net/v1/chat/completions", nil)
	require.NoError(t, err)
	request.Header.Set("User-Agent", "Go-http-client/1.1")
	request.Header.Set("OpenAI-Project", "synthetic-override")
	require.NoError(t, OmniApplyKuaiHeaders(request, info))
	require.Equal(t, OmniKuaiUserAgent, request.Header.Get("User-Agent"))
	require.Empty(t, request.Header.Get("OpenAI-Project"))
	require.Equal(t, "text/event-stream", request.Header.Get("Accept"))
	require.True(t, OmniCheckKuaiUsage(c, info, channelID, map[string]any{"prompt_tokens": 18.0, "completion_tokens": 4.0, "total_tokens": 22.0}, false))
	require.False(t, OmniCheckKuaiUsage(c, info, channelID, map[string]any{
		"prompt_tokens": 4400.0, "completion_tokens": 4.0, "total_tokens": 4404.0,
		"prompt_tokens_details": map[string]any{"cached_tokens": 4224.0},
	}, false))
	_, key := omniKuaiRoute(info, channelID)
	omniKuaiLocalQuarantine.Delete(key)
	require.False(t, OmniKuaiDispatchAllowed(info, channelID), "persisted quarantine survives a process restart")
	other := *info
	other.RequestURLPath = "/v1/responses"
	require.True(t, OmniKuaiDispatchAllowed(&other, channelID))
	billing := &kuaiWaiverBilling{}
	info.Billing = billing
	PostTextConsumeQuota(c, info, &dto.Usage{PromptTokens: 4400, CompletionTokens: 4, TotalTokens: 4404}, nil)
	require.Equal(t, []int{0}, billing.settled)
}
