package service

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/require"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"
)

func TestOmniSharedVerifiedInlineImageUsesOnlyApprovedRouteAndKeepsOriginal(t *testing.T) {
	_, info := actualAllowanceFixture(t)
	common.OptionMapRWMutex.Lock()
	oldRegistry := common.OptionMap["OmniAPIModelRegistry"]
	common.OptionMap["OmniAPICommercial"] = `{"billing_enabled":true,"model_scope":"all_available","customer_overage_policy":"customer_actual_v1"}`
	common.OptionMap["OmniAPIModelRegistry"] = `{"enabled":true,"version":"shared-image-fixture","models":{"test-model":{"version":"test","capability":"text","bindings":[{"channel_id":7,"upstream_model":"test-model","status":"verified","protocols":["claude"],"supports":["vision"]},{"channel_id":8,"upstream_model":"test-model","status":"verified","protocols":["claude"],"supports":["vision"]}]}}}`
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap["OmniAPIModelRegistry"] = oldRegistry
		common.OptionMapRWMutex.Unlock()
	})
	var buffer bytes.Buffer
	require.NoError(t, png.Encode(&buffer, image.NewRGBA(image.Rect(0, 0, 8, 8))))
	payload, _ := json.Marshal(map[string]any{"model": "test-model", "max_tokens": 128, "messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": base64.StdEncoding.EncodeToString(buffer.Bytes())}}, map[string]any{"type": "text", "text": "Read the image."}}}}})
	var request dto.ClaudeRequest
	require.NoError(t, json.Unmarshal(payload, &request))
	info.Request = &request
	info.RequestURLPath = "/v1/messages"
	info.RelayFormat = types.RelayFormatClaude
	info.OmniPoolID = "pool"
	info.OmniPriceProtected = true
	info.OmniRegistryVersion = "shared-image-fixture"
	info.OmniPriceQuote = json.RawMessage(`{"restrictions":["input_images_token_priced"]}`)
	p := model.OmniCommerceProfile{Models: `{"test-model":[7,8]}`, PricingHash: "pricing", RoutingHash: "routing"}
	matrix := map[string]any{"shared_scope": "bounded_text", "scope_approval": strings.Repeat("a", 64), "pricing_hash": "pricing", "routing_hash": "routing", "bounds": map[string]any{"test-model": map[string]any{}}}
	paths := matrix["bounds"].(map[string]any)["test-model"].(map[string]any)
	for _, id := range []string{"7", "8"} {
		paths[id] = map[string]any{"/v1/messages": map[string]any{"max_quota": 9000, "max_request_bytes": 16384, "max_output_tokens": 1024, "local_control_evidence": "synthetic actual-usage image proof", "acceptance_hash": strings.Repeat("b", 64), "verified_at": time.Now().Unix(), "input_images": id == "7"}}
	}
	raw, _ := json.Marshal(matrix)
	p.CapabilityMatrix = string(raw)
	before, _ := json.Marshal(info.Request)
	require.NoError(t, omniValidateCertifiedShared(info, p), "verified token-priced inline media must not hit the blanket text-only block")
	require.Contains(t, info.OmniSharedDispatchBounds, 7)
	require.NotContains(t, info.OmniSharedDispatchBounds, 8)
	after, _ := json.Marshal(info.Request)
	require.Equal(t, string(before), string(after), "classification cannot strip or replace the real image")
	info.OmniCustomerActual = true
	info.ChannelMeta = &relaycommon.ChannelMeta{ChannelId: 7}
	require.NoError(t, OmniValidateDispatchBody(info, before))
	var outgoing map[string]any
	require.NoError(t, json.Unmarshal(before, &outgoing))
	outgoing["tools"] = []any{map[string]any{"type": "web_search_20250305", "name": "web_search"}}
	injected, _ := json.Marshal(outgoing)
	require.Error(t, OmniValidateDispatchBody(info, injected), "post-funding overrides must not smuggle hosted work through the image certificate")
	delete(outgoing, "tools")
	outgoing["previous_response_id"] = "opaque-history"
	injected, _ = json.Marshal(outgoing)
	require.Error(t, OmniValidateDispatchBody(info, injected), "image acceptance does not authorize hidden history")
	savedQuote := info.OmniPriceQuote
	info.OmniPriceQuote = json.RawMessage(`{"restrictions":[]}`)
	require.Error(t, omniValidateCertifiedShared(info, p), "a vision declaration alone does not establish image billing")
	info.OmniPriceQuote = savedQuote
	common.OptionMapRWMutex.Lock()
	common.OptionMap["OmniAPICommercial"] = `{"billing_enabled":true,"model_scope":"all_available"}`
	common.OptionMapRWMutex.Unlock()
	require.Error(t, omniValidateCertifiedShared(info, p), "legacy shared requests must retain the original funding contract")
	common.OptionMapRWMutex.Lock()
	common.OptionMap["OmniAPICommercial"] = `{"billing_enabled":true,"model_scope":"all_available","customer_overage_policy":"customer_actual_v1"}`
	common.OptionMapRWMutex.Unlock()
	var remote map[string]any
	require.NoError(t, json.Unmarshal(before, &remote))
	remote["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["source"] = map[string]any{"type": "url", "url": "https://synthetic.invalid/private-image.png"}
	remoteRaw, _ := json.Marshal(remote)
	var remoteRequest dto.ClaudeRequest
	require.NoError(t, json.Unmarshal(remoteRaw, &remoteRequest))
	info.Request = &remoteRequest
	require.Error(t, omniValidateCertifiedShared(info, p), "inline image evidence cannot authorize remote/file references")
	info.Request = &request
	paths["7"].(map[string]any)["/v1/messages"].(map[string]any)["input_images"] = false
	raw, _ = json.Marshal(matrix)
	p.CapabilityMatrix = string(raw)
	require.Error(t, omniValidateCertifiedShared(info, p), "another channel's vision declaration is not shared acceptance")
}
