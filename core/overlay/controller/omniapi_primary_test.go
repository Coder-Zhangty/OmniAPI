package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	hostdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func omniPrimaryFixture(t *testing.T) (*gin.Context, *relaycommon.RelayInfo) {
	t.Helper()
	previousDB, previousCache, previousOptions := model.DB, common.MemoryCacheEnabled, common.OptionMap
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	connection, err := db.DB()
	require.NoError(t, err)
	connection.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}))
	model.DB, common.MemoryCacheEnabled = db, true
	base := "https://synthetic-provider.invalid"
	require.NoError(t, db.Create(&model.Channel{Id: 6, Type: constant.ChannelTypeAnthropic, Status: common.ChannelStatusEnabled, Key: "synthetic-test-key", BaseURL: &base, Name: "synthetic-native", Models: "test-model", Group: "default"}).Error)
	require.NoError(t, db.Create(&model.Ability{Group: "default", Model: "test-model", ChannelId: 6, Enabled: true}).Error)
	policy, err := json.Marshal(service.OmniRoutePolicy{Enabled: true, Models: map[string][]int{"test-model": {6}}})
	require.NoError(t, err)
	common.OptionMap = map[string]string{"OmniAPIRouting": string(policy)}
	model.InitChannelCache()
	t.Cleanup(func() {
		model.DB, common.MemoryCacheEnabled, common.OptionMap = previousDB, previousCache, previousOptions
		if previousDB != nil {
			model.InitChannelCache()
		}
		connection, _ := db.DB()
		_ = connection.Close()
	})
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	return c, &relaycommon.RelayInfo{OriginModelName: "test-model", UsingGroup: "default", UserId: 1}
}

func TestOmniPrimaryImageSelectsOnlyAcceptedSpecificationBeforeSubmission(t *testing.T) {
	c, info := omniPrimaryFixture(t)
	c.Request = httptest.NewRequest("POST", "/v1/images/generations", nil)
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", 6).Update("type", constant.ChannelTypeOpenAI).Error)
	url := "https://synthetic-image.invalid"
	require.NoError(t, model.DB.Create(&model.Channel{Id: 7, Type: constant.ChannelTypeOpenAI, Status: 1, Key: "test", BaseURL: &url, Models: "test-model", Group: "default"}).Error)
	registry := model.OmniModelRegistry{Enabled: true, Version: fmt.Sprintf("image-spec-%d", time.Now().UnixNano()), Models: map[string]model.OmniModelIdentity{"test-model": {Version: "test", Capability: "image", Bindings: []model.OmniModelBinding{{ChannelID: 6, UpstreamModel: "test-model", Status: "verified", Protocols: []string{"openai"}, Supports: []string{"image_spec:4096x4096:low"}}, {ChannelID: 7, UpstreamModel: "test-model", Status: "verified", Protocols: []string{"openai"}, Supports: []string{"image_spec:1024x1024:low"}}}}}}
	encoded, err := json.Marshal(registry)
	require.NoError(t, err)
	common.OptionMap["OmniAPIModelRegistry"] = string(encoded)
	policy := service.GetOmniRouting()
	policy.Models["test-model"] = []int{6, 7}
	encoded, err = json.Marshal(policy)
	require.NoError(t, err)
	common.OptionMap["OmniAPIRouting"] = string(encoded)
	model.InitChannelCache()
	for _, tc := range []struct {
		size    string
		channel int
	}{{"1024x1024", 7}, {"4096x4096", 6}, {"2048x2048", 0}} {
		info.Request = &dto.ImageRequest{Size: tc.size, Quality: "low"}
		channel, managed, apiErr := omniPrimary(c, info)
		require.True(t, managed)
		if tc.channel == 0 {
			require.NotNil(t, apiErr)
		} else {
			require.Nil(t, apiErr)
			require.Equal(t, tc.channel, channel.Id)
		}
	}
}

// Exercise the real Claude HTTP helper, including its second metadata init,
// response conversion and settlement call, rather than calling omniRequest directly.
func TestOmniPrimaryNativeClaudeHelperBridgeAndFallbackSettleOnce(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, primaryMode := range []string{"healthy", "failed", "disabled", "missing"} {
			failPrimary := primaryMode == "failed"
			unavailable := primaryMode == "disabled" || primaryMode == "missing"
			useBackup := primaryMode != "healthy"
			name := fmt.Sprintf("stream=%t/primary=%s", streaming, primaryMode)
			t.Run(name, func(t *testing.T) {
				c, info := omniPrimaryFixture(t)
				require.NoError(t, model.DB.AutoMigrate(&model.User{}, &model.OmniAttempt{}))
				require.NoError(t, model.DB.Create(&model.User{Id: 1, Username: "synthetic-coordinator-user", Group: "default"}).Error)
				previousBatch, previousLogs := common.BatchUpdateEnabled, common.LogConsumeEnabled
				previousStreamingTimeout := constant.StreamingTimeout
				constant.StreamingTimeout = 5
				common.BatchUpdateEnabled, common.LogConsumeEnabled = false, false
				t.Cleanup(func() {
					common.BatchUpdateEnabled, common.LogConsumeEnabled = previousBatch, previousLogs
					constant.StreamingTimeout = previousStreamingTimeout
				})
				service.InitHttpClient()
				service.OmniCircuitResult("6:test-model", true, time.Now(), service.GetOmniRouting())
				var nativeCalls, fallbackCalls atomic.Int32
				native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					nativeCalls.Add(1)
					if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "synthetic-test-key" {
						t.Error("coordinator failed to restore native Claude transport")
					}
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["model"] != "test-model" {
						t.Error("invalid normalized native request")
					}
					if failPrimary {
						http.Error(w, "synthetic failure", http.StatusServiceUnavailable)
						return
					}
					if !streaming {
						w.Header().Set("Content-Type", "application/json")
						fmt.Fprint(w, `{"id":"msg_mock","type":"message","role":"assistant","model":"test-model","content":[{"type":"text","text":"native answer"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":2}}`)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					for _, frame := range []string{
						`{"type":"message_start","message":{"id":"msg_mock","model":"test-model","role":"assistant","content":[],"usage":{"input_tokens":2,"output_tokens":0}}}`,
						`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
						`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"native answer"}}`,
						`{"type":"content_block_stop","index":0}`,
						`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
						`{"type":"message_stop"}`,
					} {
						fmt.Fprint(w, "data: "+frame+"\n\n")
					}
				}))
				t.Cleanup(native.Close)
				backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					fallbackCalls.Add(1)
					if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer synthetic-backup-key" {
						t.Error("fallback must restore its own OpenAI transport and credentials")
					}
					if !streaming {
						w.Header().Set("Content-Type", "application/json")
						fmt.Fprint(w, `{"id":"backup_mock","model":"test-model","choices":[{"message":{"role":"assistant","content":"backup answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":2,"total_tokens":4}}`)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: "+`{"id":"backup_mock","model":"test-model","choices":[{"index":0,"delta":{"content":"backup answer"}}]}`+"\n\n")
					fmt.Fprint(w, "data: "+`{"id":"backup_mock","model":"test-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n")
					fmt.Fprint(w, "data: "+`{"id":"backup_mock","model":"test-model","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":2,"total_tokens":4}}`+"\n\ndata: [DONE]\n\n")
				}))
				t.Cleanup(backup.Close)
				require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", 6).Update("base_url", native.URL).Error)
				if primaryMode == "disabled" {
					require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", 6).Update("status", common.ChannelStatusManuallyDisabled).Error)
				}
				if primaryMode == "missing" {
					require.NoError(t, model.DB.Delete(&model.Channel{}, 6).Error)
				}
				require.NoError(t, model.DB.Create(&model.Channel{Id: 7, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Key: "synthetic-backup-key", BaseURL: &backup.URL, Models: "test-model", Group: "default"}).Error)
				require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "test-model", ChannelId: 7, Enabled: true}).Error)
				policy := service.GetOmniRouting()
				policy.Models["test-model"] = []int{6, 7}
				policy.Costs = map[string]map[int]service.OmniRouteCost{"test-model": {6: {Input: 1, Output: 2}, 7: {Input: 3, Output: 4}}}
				encoded, marshalErr := json.Marshal(policy)
				require.NoError(t, marshalErr)
				common.OptionMap["OmniAPIRouting"] = string(encoded)
				model.InitChannelCache()
				recorder := httptest.NewRecorder()
				c, _ = gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
				maxTokens := uint(64)
				request := &dto.ClaudeRequest{Model: "test-model", MaxTokens: &maxTokens, Stream: &streaming}
				require.NoError(t, json.Unmarshal([]byte(`[{"role":"user","content":"hello"}]`), &request.Messages))
				info.Request, info.RelayFormat, info.RequestURLPath = request, types.RelayFormatClaude, "/v1/messages"
				info.RequestId, info.IsStream, info.StartTime = name, streaming, time.Now()
				billing := &nativeRouteBilling{}
				info.Billing = billing
				selected, channelErr := getChannel(c, info, &service.RetryParam{Ctx: c})
				require.Nil(t, channelErr)
				if unavailable {
					require.Equal(t, 7, selected.Id, "only bootstrap selection may skip unavailable primary")
				} else {
					require.Equal(t, constant.ChannelTypeAnthropic, selected.Type)
				}
				require.Equal(t, []int{6, 7}, service.GetOmniRouting().Models["test-model"], "bootstrap must not rewrite supplier order")
				helperErr := relay.ClaudeHelper(c, info)
				require.Nil(t, helperErr, recorder.Body.String())
				answer, finalChannel := "native answer", 6
				if useBackup {
					answer, finalChannel = "backup answer", 7
				}
				require.Contains(t, recorder.Body.String(), answer)
				if streaming {
					require.Contains(t, recorder.Body.String(), "message_stop")
				}
				if unavailable {
					require.Zero(t, nativeCalls.Load(), "disabled or missing primary must never reach its HTTP provider")
				} else {
					require.Equal(t, int32(1), nativeCalls.Load())
				}
				if useBackup {
					require.Equal(t, int32(1), fallbackCalls.Load())
				} else {
					require.Zero(t, fallbackCalls.Load())
				}
				require.Equal(t, finalChannel, info.ChannelId)
				require.Equal(t, []string{"settle"}, billing.events, "internal native bridge and fallback must not settle independently")
				attempts := model.OmniRequestAttempts(name)
				if failPrimary || (unavailable && streaming) {
					require.Len(t, attempts, 2)
				} else {
					require.Len(t, attempts, 1)
				}
				require.Equal(t, finalChannel, attempts[len(attempts)-1].ChannelID)
				require.Greater(t, attempts[len(attempts)-1].CostUSD, float64(0))
				var user model.User
				require.NoError(t, model.DB.First(&user, 1).Error)
				require.Equal(t, 1, user.RequestCount)
				if primaryMode != "missing" {
					cached, cacheErr := model.CacheGetChannel(6)
					require.NoError(t, cacheErr)
					require.Equal(t, constant.ChannelTypeAnthropic, cached.Type)
				}
			})
		}
	}
}

func TestOmniPrimaryNativeChannelUsesCoordinatorWithoutChangingCachedProvider(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/messages", "/v1/responses?preview=1"} {
		t.Run(path, func(t *testing.T) {
			c, info := omniPrimaryFixture(t)
			c.Request = httptest.NewRequest("POST", path, nil)
			if c.Request.URL.Path == "/v1/responses" {
				info.Request = &dto.OpenAIResponsesRequest{Model: "test-model", Input: json.RawMessage(`"hello"`)}
			}
			selected, err := getChannel(c, info, &service.RetryParam{Ctx: c})
			require.Nil(t, err)
			require.Equal(t, 6, selected.Id)
			require.Equal(t, constant.ChannelTypeAnthropic, selected.Type)
			require.Equal(t, constant.ChannelTypeOpenAI, common.GetContextKeyInt(c, constant.ContextKeyChannelType))
			require.Equal(t, 6, info.ChannelId)
			require.Equal(t, constant.ChannelTypeOpenAI, info.ChannelType)
			require.Equal(t, constant.APITypeOpenAI, info.ApiType)
			require.Equal(t, "synthetic-test-key", info.ApiKey)
			require.Equal(t, "https://synthetic-provider.invalid", info.ChannelBaseUrl)
			// All three HTTP helpers initialize metadata again before selecting an adaptor.
			info.InitChannelMeta(c)
			require.Equal(t, constant.APITypeOpenAI, info.ApiType)
			cached, cacheErr := model.CacheGetChannel(6)
			require.NoError(t, cacheErr)
			require.Equal(t, constant.ChannelTypeAnthropic, cached.Type)
			var stored model.Channel
			require.NoError(t, model.DB.First(&stored, 6).Error)
			require.Equal(t, constant.ChannelTypeAnthropic, stored.Type)
		})
	}
}

func TestOmniPrimaryNativeChannelKeepsStatefulAndUnsupportedTransportsUnmanaged(t *testing.T) {
	for _, tc := range []struct{ name, path, body string }{
		{"previous response", "/v1/responses", `{"model":"test-model","previous_response_id":"resp_existing"}`},
		{"conversation id", "/v1/responses", `{"model":"test-model","conversation":"conv_existing"}`},
		{"conversation object", "/v1/responses", `{"model":"test-model","conversation":{"id":"conv_existing"}}`},
		{"compaction", "/v1/responses/compact", `{}`},
		{"images", "/v1/images/generations", `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, info := omniPrimaryFixture(t)
			c.Request = httptest.NewRequest("POST", tc.path, nil)
			var request dto.OpenAIResponsesRequest
			require.NoError(t, json.Unmarshal([]byte(tc.body), &request))
			info.Request = &request
			common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeAnthropic)
			channel, managed, err := omniPrimary(c, info)
			require.Nil(t, err)
			require.Nil(t, channel)
			require.False(t, managed)
			require.Equal(t, constant.ChannelTypeAnthropic, common.GetContextKeyInt(c, constant.ContextKeyChannelType))
		})
	}
}

func TestOmniPrimaryNullConversationIsStateless(t *testing.T) {
	c, info := omniPrimaryFixture(t)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	var request dto.OpenAIResponsesRequest
	require.NoError(t, json.Unmarshal([]byte(`{"model":"test-model","conversation":null}`), &request))
	info.Request = &request
	channel, managed, err := omniPrimary(c, info)
	require.Nil(t, err)
	require.True(t, managed)
	require.Equal(t, 6, channel.Id)
	require.Equal(t, constant.APITypeOpenAI, info.ApiType)
}

func TestOmniPrimaryStatefulResponsesRetainSelectedSupplierAndSuppressRetry(t *testing.T) {
	for _, body := range []string{`{"previous_response_id":"resp_existing"}`, `{"conversation":"conv_existing"}`, `{"conversation":{"id":"conv_existing"}}`} {
		for _, initialized := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/meta=%t", body, initialized), func(t *testing.T) {
				c, info := omniPrimaryFixture(t)
				policy := service.GetOmniRouting()
				policy.WaitBudgets = map[string]service.OmniRouteWaitBudget{"test-model": {FirstProgressSecondsByChannel: map[int]int{6: 15}, LastRouteFirstProgressSeconds: 180, AttemptSeconds: 180, PrecommitSeconds: 240}}
				encoded, marshalErr := json.Marshal(policy)
				require.NoError(t, marshalErr)
				common.OptionMap["OmniAPIRouting"] = string(encoded)
				c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
				var request dto.OpenAIResponsesRequest
				require.NoError(t, json.Unmarshal([]byte(body), &request))
				info.Request = &request
				// The new GPT primary is OpenAI, while middleware has already
				// selected another supplier for this stateful request.
				require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", 6).Update("type", constant.ChannelTypeOpenAI).Error)
				base := "https://original-stateful-supplier.invalid"
				original := &model.Channel{Id: 7, Type: constant.ChannelTypeOpenAI, Status: 1, Key: "stateful-fixture-key", BaseURL: &base, Models: "test-model", Group: "default"}
				require.NoError(t, model.DB.Create(original).Error)
				require.NoError(t, model.DB.Create(&model.Ability{Group: "default", Model: "test-model", ChannelId: 7, Enabled: true}).Error)
				model.InitChannelCache()
				require.Nil(t, middleware.SetupContextForSelectedChannel(c, original, info.OriginModelName))
				if initialized {
					info.InitChannelMeta(c)
				}
				originalMeta := info.ChannelMeta
				selected, getErr := getChannel(c, info, &service.RetryParam{Ctx: c, TokenGroup: "default", ModelName: "test-model", Retry: common.GetPointer(1)})
				require.Nil(t, getErr)
				require.Equal(t, 7, selected.Id)
				require.Equal(t, 7, c.GetInt("channel_id"))
				require.Same(t, originalMeta, info.ChannelMeta)
				pin, found, _ := service.GetChannelConstraints(c).ResolvedPin()
				require.True(t, found)
				require.Equal(t, 7, pin.ChannelId)
				require.Equal(t, hostdto.PinRetrySingleAttempt, pin.RetryMode)
				selected, getErr = getChannel(c, info, &service.RetryParam{Ctx: c, TokenGroup: "default", ModelName: "test-model", Retry: common.GetPointer(1)})
				require.Nil(t, getErr)
				require.Equal(t, 7, selected.Id)
				failure := types.NewErrorWithStatusCode(fmt.Errorf("synthetic upstream unavailable"), types.ErrorCodeDoRequestFailed, http.StatusServiceUnavailable)
				require.Equal(t, "stop", service.DecideRelayRetry(c, failure, 3).Action, "outer retry must not replay stateful request to another supplier")
			})
		}
	}
}

func TestOmniPrimaryStatefulResponsesHonorExistingPin(t *testing.T) {
	c, info := omniPrimaryFixture(t)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	info.Request = &dto.OpenAIResponsesRequest{PreviousResponseID: "resp_existing"}
	policy := service.GetOmniRouting()
	policy.WaitBudgets = map[string]service.OmniRouteWaitBudget{"test-model": {FirstProgressSecondsByChannel: map[int]int{6: 15}, LastRouteFirstProgressSeconds: 180, AttemptSeconds: 180, PrecommitSeconds: 240}}
	encoded, marshalErr := json.Marshal(policy)
	require.NoError(t, marshalErr)
	common.OptionMap["OmniAPIRouting"] = string(encoded)
	selected, cacheErr := model.CacheGetChannel(6)
	require.NoError(t, cacheErr)
	require.Nil(t, middleware.SetupContextForSelectedChannel(c, selected, info.OriginModelName))
	info.InitChannelMeta(c)
	originalMeta := info.ChannelMeta
	service.GetChannelConstraints(c).AddPin(hostdto.ChannelPin{ChannelId: 6, Source: hostdto.PinSourceToken, Rank: hostdto.PinRankToken, RetryMode: hostdto.PinRetrySingleAttempt})
	channel, apiErr := getChannel(c, info, &service.RetryParam{Ctx: c, TokenGroup: "default", ModelName: "test-model", Retry: common.GetPointer(0)})
	require.Nil(t, apiErr)
	require.Equal(t, 6, channel.Id)
	require.Same(t, originalMeta, info.ChannelMeta)
	pin, found, _ := service.GetChannelConstraints(c).ResolvedPin()
	require.True(t, found)
	require.Equal(t, 6, pin.ChannelId)
	require.Equal(t, hostdto.PinSourceToken, pin.Source)
	require.Len(t, service.GetChannelConstraints(c).Pins, 1)
}

func TestOmniPrimaryStatefulResponsesUnavailableBindingFailsClosed(t *testing.T) {
	for _, scenario := range []string{"missing", "disabled", "pin_conflict"} {
		t.Run(scenario, func(t *testing.T) {
			c, info := omniPrimaryFixture(t)
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
			info.Request = &dto.OpenAIResponsesRequest{PreviousResponseID: "resp_existing"}
			policy := service.GetOmniRouting()
			policy.WaitBudgets = map[string]service.OmniRouteWaitBudget{"test-model": {FirstProgressSecondsByChannel: map[int]int{6: 15}, LastRouteFirstProgressSeconds: 180, AttemptSeconds: 180, PrecommitSeconds: 240}}
			encoded, marshalErr := json.Marshal(policy)
			require.NoError(t, marshalErr)
			common.OptionMap["OmniAPIRouting"] = string(encoded)
			common.SetContextKey(c, constant.ContextKeyChannelId, 7)
			if scenario == "disabled" {
				base := "https://disabled-stateful.invalid"
				require.NoError(t, model.DB.Create(&model.Channel{Id: 7, Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusManuallyDisabled, Key: "fixture", BaseURL: &base, Models: "test-model", Group: "default"}).Error)
			}
			if scenario == "pin_conflict" {
				service.GetChannelConstraints(c).AddPin(hostdto.ChannelPin{ChannelId: 6, Source: hostdto.PinSourceToken, Rank: hostdto.PinRankToken})
			}
			model.InitChannelCache()
			channel, apiErr := getChannel(c, info, &service.RetryParam{Ctx: c, TokenGroup: "default", ModelName: "test-model", Retry: common.GetPointer(0)})
			require.Nil(t, channel)
			require.NotNil(t, apiErr)
			require.True(t, types.IsSkipRetryError(apiErr))
			require.Equal(t, 7, c.GetInt("channel_id"))
		})
	}
}

func TestOmniPrimaryStatefulResponsesUnconfiguredModelRetainsLegacy(t *testing.T) {
	c, info := omniPrimaryFixture(t)
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	info.Request = &dto.OpenAIResponsesRequest{PreviousResponseID: "resp_existing"}
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", 6).Update("type", constant.ChannelTypeOpenAI).Error)
	model.InitChannelCache()
	common.SetContextKey(c, constant.ContextKeyChannelId, 7)
	channel, managed, apiErr := omniPrimary(c, info)
	require.Nil(t, apiErr)
	require.True(t, managed)
	require.Equal(t, 6, channel.Id)
	_, pinned, _ := service.GetChannelConstraints(c).ResolvedPin()
	require.False(t, pinned)
}

func TestOmniPrimaryHonorsDisabledChannelsGroupsAndPins(t *testing.T) {
	for _, scenario := range []string{"disabled", "wrong group", "pinned"} {
		t.Run(scenario, func(t *testing.T) {
			c, info := omniPrimaryFixture(t)
			switch scenario {
			case "disabled":
				require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", 6).Update("status", common.ChannelStatusManuallyDisabled).Error)
				model.InitChannelCache()
			case "wrong group":
				info.UsingGroup = "premium"
			case "pinned":
				service.GetChannelConstraints(c).AddPin(hostdto.ChannelPin{ChannelId: 7, Source: hostdto.PinSourceToken})
			}
			channel, managed, err := omniPrimary(c, info)
			require.Nil(t, channel)
			if scenario == "pinned" {
				require.False(t, managed)
				require.Nil(t, err)
			} else {
				require.True(t, managed)
				require.NotNil(t, err)
			}
		})
	}
}
