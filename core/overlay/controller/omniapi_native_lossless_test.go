package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type nativeLosslessTransport func(*http.Request) (*http.Response, error)

func (f nativeLosslessTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOmniNativeClaudePreservesSignedHistoryAndToolResults(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			c, info := omniPrimaryFixture(t)
			require.NoError(t, model.DB.AutoMigrate(&model.User{}, &model.OmniAttempt{}))
			require.NoError(t, model.DB.Create(&model.User{Id: 1, Username: "native-lossless", Group: "default"}).Error)
			oldBatch, oldLogs := common.BatchUpdateEnabled, common.LogConsumeEnabled
			common.BatchUpdateEnabled, common.LogConsumeEnabled = false, false
			t.Cleanup(func() { common.BatchUpdateEnabled, common.LogConsumeEnabled = oldBatch, oldLogs })
			oldTimeout := constant.StreamingTimeout
			constant.StreamingTimeout = 5
			t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })
			service.InitHttpClient()
			policy := service.GetOmniRouting()
			policy.NativeClaudeModels = nil
			policyJSON, err := json.Marshal(policy)
			require.NoError(t, err)
			common.OptionMap["OmniAPIRouting"] = string(policyJSON)
			registry := model.OmniModelRegistry{Enabled: true, Version: fmt.Sprintf("native-legacy-%t", streaming),
				Models: map[string]model.OmniModelIdentity{"test-model": {Version: "test", Capability: "text", Bindings: []model.OmniModelBinding{{
					ChannelID: 6, Status: "verified", UpstreamModel: "test-model", Protocols: []string{"claude"},
					Supports: []string{"tools", "cache_write_1h"}, ContextLength: 10, VerifiedPathsOnly: true,
					RequestLimits: map[string]model.OmniRequestLimits{"/v1/messages": {
						MaxOutputTokens: 128000, Basis: "accepted_parameters", EvidenceHash: strings.Repeat("a", 64), VerifiedAt: time.Now().Unix(),
					}},
				}}}}}
			registryJSON, err := json.Marshal(registry)
			require.NoError(t, err)
			common.OptionMap["OmniAPIModelRegistry"] = string(registryJSON)
			info.OmniRegistryVersion = registry.Version
			body := `{"model":"test-model","max_tokens":1000000,"messages":[{"role":"user","content":"Question"},{"role":"assistant","content":[{"type":"thinking","signature":"opaque-original-signature"},{"type":"text","text":"Before tool"},{"type":"tool_use","id":"tool-original","name":"lookup","input":{"key":"value"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-original","is_error":true,"content":"synthetic error","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`
			var request dto.ClaudeRequest
			require.NoError(t, json.Unmarshal([]byte(body), &request))
			request.Stream = &streaming
			encoded, err := json.Marshal(request)
			require.NoError(t, err)
			var expected map[string]any
			require.NoError(t, json.Unmarshal(encoded, &expected))
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/v1/messages", r.URL.Path)
				require.Contains(t, r.Header.Get("anthropic-beta"), "extended-cache-ttl")
				var got map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
				require.Equal(t, expected["messages"], got["messages"], "native history must not pass through the lossy Chat representation")
				require.Equal(t, float64(1000000), got["max_tokens"])
				if !streaming {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"id":"msg_signed","type":"message","role":"assistant","model":"test-model","content":[{"type":"thinking","thinking":"New thought","signature":"opaque-new-signature"},{"type":"text","text":"Native answer"}],"stop_reason":"end_turn","usage":{"input_tokens":9,"output_tokens":6}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				for _, frame := range []string{
					`{"type":"message_start","message":{"id":"msg_signed","model":"test-model","role":"assistant","content":[],"usage":{"input_tokens":9,"output_tokens":0}}}`,
					`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
					`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"New thought"}}`,
					`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"opaque-new-signature"}}`,
					`{"type":"content_block_stop","index":0}`,
					`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
					`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Native answer"}}`,
					`{"type":"content_block_stop","index":1}`,
					`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":6}}`,
					`{"type":"message_stop"}`,
				} {
					fmt.Fprint(w, "data: "+frame+"\n\n")
				}
			})
			client := service.GetHttpClient()
			originalTransport := client.Transport
			client.Transport = nativeLosslessTransport(func(r *http.Request) (*http.Response, error) {
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, r)
				return response.Result(), nil
			})
			t.Cleanup(func() { client.Transport = originalTransport })
			recorder := httptest.NewRecorder()
			c, _ = gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
			c.Request.Header.Set("anthropic-beta", "extended-cache-ttl-2025-04-11")
			info.Request, info.RelayFormat, info.RequestURLPath = &request, types.RelayFormatClaude, "/v1/messages"
			info.RequestId = fmt.Sprintf("native-lossless-%t-%d", streaming, time.Now().UnixNano())
			info.IsStream = streaming
			info.StartTime = time.Now()
			billing := &nativeRouteBilling{}
			info.Billing = billing
			_, apiErr := getChannel(c, info, &service.RetryParam{Ctx: c})
			require.Nil(t, apiErr)
			apiErr = relay.ClaudeHelper(c, info)
			require.Nil(t, apiErr)
			require.Contains(t, recorder.Body.String(), "opaque-new-signature")
			require.Contains(t, recorder.Body.String(), "Native answer")
			require.Equal(t, []string{"settle"}, billing.events)
			require.Len(t, model.OmniRequestAttempts(info.RequestId), 1)
		})
	}
}
