package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/claude"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// OmniNativeClaudeRequest reuses ordered selection and transport deadlines.
// The native adaptor consumes the winning response and settles it once.
func OmniNativeClaudeRequest(c *gin.Context, info *relaycommon.RelayInfo) (*http.Response, error) {
	policy := service.OmniRoutingFor(info)
	encoded, err := json.Marshal(info.Request)
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := json.Unmarshal(encoded, &raw); err != nil {
		return nil, err
	}
	service.InitOmniRouteTrace(c)
	if err := omniAdmissionError(c, info, raw, policy.Models[info.OriginModelName]); err != nil {
		return nil, err
	}
	adaptor := &Adaptor{}
	if info.IsStream {
		return adaptor.omniResponsesRequest(c, info, raw, policy)
	}
	return adaptor.omniJSONRequest(c, info, raw, policy)
}

func omniCandidateReject(info *relaycommon.RelayInfo, id, channelType int, raw map[string]any) *model.OmniBindingRejection {
	if info.OmniNativeClaude {
		if !model.OmniChannelNativeClaude(info.OmniRegistryVersion, info.OriginModelName, id, channelType) {
			return &model.OmniBindingRejection{Reason: "native_protocol_not_verified"}
		}
		if !model.OmniNativeToolChoiceAllows(info.OmniRegistryVersion, info.OriginModelName, id, raw["tool_choice"]) {
			return &model.OmniBindingRejection{Reason: "native_tool_choice_not_verified", Field: "tool_choice"}
		}
	}
	required := omniRequiredCapabilities(raw)
	if omniResponsesPath(info) && model.OmniKuaiMeteringFor(info.OmniRegistryVersion, info.OriginModelName, id) != nil {
		items, _ := raw["input"].([]any)
		for _, value := range items {
			item, _ := value.(map[string]any)
			var capability string
			switch item["type"] {
			case "custom_tool_call", "custom_tool_call_output":
				capability = "custom_tools"
			case "tool_search_call", "tool_search_output":
				capability = "client_tool_search"
			}
			if capability != "" && !slices.Contains(required, capability) {
				required = append(required, capability)
			}
		}
	}
	if rejection := service.OmniBindingRequestReject(info, id, required); rejection != nil {
		return rejection
	}
	if !service.OmniKuaiDispatchAllowed(info, id) {
		return &model.OmniBindingRejection{Reason: "kuai_usage_quarantined"}
	}
	return nil
}

func omniSendClaude(c *gin.Context, info *relaycommon.RelayInfo, kind int) (*http.Response, error) {
	if !model.OmniChannelNativeClaude(info.OmniRegistryVersion, info.OriginModelName, info.ChannelId, kind) {
		return nil, &omniUnsentRouteError{&model.OmniBindingRejection{Reason: "native_protocol_not_verified"}}
	}
	info.ChannelType = 14
	info.ApiType, _ = common.ChannelType2APIType(info.ChannelType)
	request, ok := info.Request.(*dto.ClaudeRequest)
	if !ok {
		return nil, &omniUnsentRouteError{errors.New("native Claude request required")}
	}
	clone := *request
	clone.Model = info.UpstreamModelName
	adaptor := &claude.Adaptor{}
	adaptor.Init(info)
	converted, err := adaptor.ConvertClaudeRequest(c, info, &clone)
	if err != nil {
		return nil, &omniUnsentRouteError{err}
	}
	encoded, err := json.Marshal(converted)
	if err != nil {
		return nil, &omniUnsentRouteError{err}
	}
	// Keep source message blocks in their original JSON representation, including
	// integer tool arguments that the typed request's any fields may round.
	if storage, err := common.GetBodyStorage(c); err == nil && storage.Size() > 0 {
		source, err := storage.Bytes()
		if err != nil {
			return nil, &omniUnsentRouteError{err}
		}
		encoded, err = omniRestoreClaudeBlocks(encoded, source)
		if err != nil {
			return nil, &omniUnsentRouteError{err}
		}
	}
	if err := service.OmniValidateDispatchBody(info, encoded); err != nil {
		return nil, &omniUnsentRouteError{err}
	}
	return channel.DoApiRequest(adaptor, c, info, bytes.NewReader(encoded))
}

func omniRestoreClaudeBlocks(encoded, source []byte) ([]byte, error) {
	var body, original map[string]json.RawMessage
	if json.Unmarshal(encoded, &body) != nil || json.Unmarshal(source, &original) != nil || body == nil || original == nil {
		return nil, errors.New("invalid native Claude request body")
	}
	for _, key := range []string{"messages", "system", "tools", "tool_choice"} {
		if value := original[key]; len(value) > 0 {
			body[key] = value
		}
	}
	return json.Marshal(body)
}

func omniClaudeJSONValid(body map[string]any, upstream string) bool {
	content, ok := body["content"].([]any)
	stop, hasStop := body["stop_reason"].(string)
	return body["type"] == "message" && body["model"] == upstream && ok && len(content) > 0 && hasStop && stop != ""
}

func omniValidateCoordinatedJSON(info *relaycommon.RelayInfo, decoded, raw map[string]any) error {
	if info.OmniNativeClaude {
		return nil
	}
	return omniValidateJSONResult(decoded, raw)
}

func omniClaudeJSONUsage(audit *model.OmniAttempt, data []byte, cost service.OmniRouteCost, info *relaycommon.RelayInfo) {
	var message dto.ClaudeResponse
	if json.Unmarshal(data, &message) == nil && message.Usage != nil {
		omniAttemptUsage(audit, relayconvert.UsageFromClaudeAPIUsage(message.Usage), cost, info)
	}
}

func omniClaudeProgress(line, upstream string, started *bool) (effective, terminal bool, err error) {
	if !strings.HasPrefix(line, "data:") {
		return false, false, nil
	}
	var event dto.ClaudeResponse
	if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &event) != nil {
		return false, false, errors.New("invalid native Claude frame")
	}
	if event.Type == "error" || event.GetClaudeError() != nil {
		return false, false, errors.New("native Claude upstream error")
	}
	switch event.Type {
	case "message_start":
		if *started || event.Message == nil || event.Message.Model != upstream {
			return false, false, errors.New("native Claude response model mismatch")
		}
		*started = true
	case "content_block_start", "content_block_delta", "message_stop":
		if !*started {
			return false, false, errors.New("native Claude stream missing message start")
		}
		return true, event.Type == "message_stop", nil
	}
	return false, false, nil
}
