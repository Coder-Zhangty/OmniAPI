package service

import (
	"encoding/json"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"strings"
)

// Classify only the already-validated request. Nothing is removed, replaced or
// fetched; remote/file references and hosted tools need separate acceptance.
func omniSharedInlineImageRequest(info *relaycommon.RelayInfo) bool {
	if info == nil || !model.OmniCustomerBillingFor(info.UserId) || !omniInputImageQuote(info) || !omniSafeTokenRequest(info, true) {
		return false
	}
	encoded, err := json.Marshal(info.Request)
	if err != nil {
		return false
	}
	var raw map[string]any
	if json.Unmarshal(encoded, &raw) != nil {
		return false
	}
	return omniSharedInlineImageBody(raw)
}

func omniSharedInlineImageBody(raw map[string]any) bool {
	if omniRequestMedia(raw, true) || omniValidateInputImages(raw) != nil {
		return false
	}
	for _, key := range []string{"previous_response_id", "conversation", "audio", "modalities", "container", "context_management", "mcp_servers", "service_tier", "speed", "cachedContent", "requests"} {
		if raw[key] != nil {
			return false
		}
	}
	count := 0
	var walk func(any) bool
	walk = func(value any) bool {
		switch v := value.(type) {
		case []any:
			for _, item := range v {
				if !walk(item) {
					return false
				}
			}
		case map[string]any:
			kind, _ := v["type"].(string)
			if kind == "tool_use" {
				return true
			} // Function arguments are JSON data, not image blocks.
			switch kind {
			case "image":
				source, ok := v["source"].(map[string]any)
				if !ok || source["type"] != "base64" {
					return false
				}
				mime, _ := source["media_type"].(string)
				data, _ := source["data"].(string)
				if !strings.HasPrefix(mime, "image/") || data == "" {
					return false
				}
				count++
				return true
			case "image_url", "input_image":
				address, _ := v["image_url"].(string)
				if part, ok := v["image_url"].(map[string]any); ok {
					address, _ = part["url"].(string)
				}
				if !strings.HasPrefix(address, "data:image/") {
					return false
				}
				count++
				return true
			case "document", "input_file":
				return false
			}
			for _, key := range []string{"inlineData", "inline_data"} {
				if part, ok := v[key].(map[string]any); ok {
					mime, _ := part["mimeType"].(string)
					if mime == "" {
						mime, _ = part["mime_type"].(string)
					}
					data, _ := part["data"].(string)
					if !strings.HasPrefix(mime, "image/") || data == "" {
						return false
					}
					count++
				}
			}
			for _, key := range []string{"messages", "input", "contents", "content", "parts", "system", "systemInstruction"} {
				if !walk(v[key]) {
					return false
				}
			}
		}
		return true
	}
	return walk(raw) && count > 0
}

// Recheck actual wire data after conversion/overrides. Image acceptance is not
// authorization for hosted work, remote documents, audio or hidden context.
func omniSharedFinalImageRequest(info *relaycommon.RelayInfo, body []byte, raw map[string]any) bool {
	if !omniInputImageQuote(info) || !omniSharedInlineImageBody(raw) {
		return false
	}
	clone := *info
	switch {
	case info.ChannelType == 14 || info.RelayFormat == types.RelayFormatClaude:
		var request dto.ClaudeRequest
		if json.Unmarshal(body, &request) != nil {
			return false
		}
		clone.Request = &request
	case raw["input"] != nil:
		var request dto.OpenAIResponsesRequest
		if json.Unmarshal(body, &request) != nil {
			return false
		}
		clone.Request = &request
	case raw["contents"] != nil:
		var request dto.GeminiChatRequest
		if json.Unmarshal(body, &request) != nil {
			return false
		}
		clone.Request = &request
	default:
		var request dto.GeneralOpenAIRequest
		if json.Unmarshal(body, &request) != nil {
			return false
		}
		clone.Request = &request
	}
	return omniSafeTokenRequest(&clone, true)
}
