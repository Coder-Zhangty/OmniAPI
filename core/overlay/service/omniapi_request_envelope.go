package service

import (
	"bytes"
	"encoding/json"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"math"
	"net/url"
	"slices"
	"strings"
)

func omniRequestCandidateOutput(info *relaycommon.RelayInfo) int64 {
	if info == nil {
		return 0
	}
	value := uint(0)
	switch r := info.Request.(type) {
	case *dto.GeneralOpenAIRequest:
		if r.MaxTokens != nil {
			value = *r.MaxTokens
		}
		if r.MaxCompletionTokens != nil {
			value = max(value, *r.MaxCompletionTokens)
		}
	case *dto.ClaudeRequest:
		if r.MaxTokens != nil {
			value = *r.MaxTokens
		}
	case *dto.OpenAIResponsesRequest:
		if r.MaxOutputTokens != nil {
			value = *r.MaxOutputTokens
		}
	case *dto.GeminiChatRequest:
		if r.GenerationConfig.MaxOutputTokens != nil {
			value = *r.GenerationConfig.MaxOutputTokens
		}
	}
	if uint64(value) > uint64(^uint64(0)>>1) {
		return -1
	}
	return int64(value)
}

func omniRequestBinding(info *relaycommon.RelayInfo, id int) (model.OmniModelBinding, string, bool) {
	if info == nil {
		return model.OmniModelBinding{}, "", false
	}
	path, err := url.Parse(info.RequestURLPath)
	if err != nil {
		return model.OmniModelBinding{}, "", false
	}
	registry := model.OmniRegistryAt(info.OmniRegistryVersion)
	for _, binding := range registry.Models[registry.Canonical(info.GetBillingModelName())].Bindings {
		if binding.ChannelID == id && binding.Status == "verified" {
			return binding, path.Path, true
		}
	}
	return model.OmniModelBinding{}, path.Path, false
}

// Text routes let providers decide their token limits. Shared requests also
// validate and reserve their request-specific financial envelope separately.
func omniProviderDecidesTextLimits(info *relaycommon.RelayInfo, path string) bool {
	switch path {
	case "/v1/messages", "/v1/chat/completions", "/v1/responses":
		return true
	}
	return false
}

// Use the registered native transport for native Messages. Existing registries
// already certify Claude bindings even when the newer rollout map is absent.
func OmniNativeClaudeFor(info *relaycommon.RelayInfo) bool {
	policy := OmniRoutingFor(info)
	if !policy.Enabled || !OmniRoutingUserAllowed(policy, info.UserId) {
		return false
	}
	if enabled, configured := policy.NativeClaudeModels[info.OriginModelName]; configured {
		return enabled
	}
	registry := model.OmniRegistryAt(info.OmniRegistryVersion)
	for _, id := range policy.Models[info.OriginModelName] {
		for _, binding := range registry.Models[registry.Canonical(info.OriginModelName)].Bindings {
			if binding.ChannelID == id && binding.Status == "verified" &&
				(slices.Contains(binding.Protocols, "claude") || slices.Contains(binding.Protocols, "anthropic")) {
				return true
			}
		}
	}
	return false
}

func OmniDeclaredRequestReject(info *relaycommon.RelayInfo, id int) *model.OmniBindingRejection {
	binding, path, ok := omniRequestBinding(info, id)
	if !ok {
		return nil
	}
	if request, ok := info.Request.(*dto.GeneralOpenAIRequest); ok && path == "/v1/chat/completions" &&
		slices.Contains(binding.Supports, "chat_tool_choice_auto_only") {
		choice := omniRouteToolChoice(request.ToolChoice)
		if choice == "required" || choice == "named" || choice == "other" {
			return &model.OmniBindingRejection{Reason: "chat_tool_choice_not_verified", Field: "tool_choice"}
		}
	}
	if binding.VerifiedPathsOnly && path == "/v1/responses" {
		request, ok := info.Request.(*dto.OpenAIResponsesRequest)
		if !ok || !bytes.Equal(bytes.TrimSpace(request.Store), []byte("false")) || request.PreviousResponseID != "" ||
			len(bytes.TrimSpace(request.Conversation)) != 0 && !bytes.Equal(bytes.TrimSpace(request.Conversation), []byte("null")) {
			return &model.OmniBindingRejection{Reason: "route_stateful_response_not_verified"}
		}
		if metering := binding.KuaiMetering; metering.Valid() {
			if metering.RequireResponsesStream && !info.IsStream {
				return &model.OmniBindingRejection{Reason: "kuai_stream_usage_required", Field: "stream"}
			}
			if metering.RequireResponsesInstructions {
				var instructions string
				if json.Unmarshal(request.Instructions, &instructions) != nil || strings.TrimSpace(instructions) == "" {
					return &model.OmniBindingRejection{Reason: "kuai_client_instructions_required", Field: "instructions"}
				}
			}
		}
	}
	limits, hard := binding.AdmissionLimitsFor(path)
	if !hard {
		if binding.VerifiedPathsOnly {
			return &model.OmniBindingRejection{Reason: "route_protocol_not_verified"}
		}
		return nil
	}
	if binding.VerifiedPathsOnly && !OmniSafeTextRequest(info) {
		field, blockType := omniRequestShapeHint(info)
		return &model.OmniBindingRejection{Reason: "route_request_shape_not_verified", Field: field, BlockType: blockType}
	}
	output := omniRequestCandidateOutput(info)
	if binding.VerifiedPathsOnly && limits.MaxOutputTokens > 0 && output == 0 {
		return &model.OmniBindingRejection{Reason: "route_output_budget_required"}
	}
	if output < 0 {
		return &model.OmniBindingRejection{Reason: "request_envelope_invalid"}
	}
	if omniProviderDecidesTextLimits(info, path) {
		return nil
	}
	data, err := json.Marshal(info.Request)
	if err != nil {
		return &model.OmniBindingRejection{Reason: "request_envelope_invalid"}
	}
	return binding.RejectEnvelope(path, int64(info.GetEstimatePromptTokens()), output, int64(len(data)))
}

func OmniHasDeclaredRequestLimits(info *relaycommon.RelayInfo, id int) bool {
	binding, _, ok := omniRequestBinding(info, id)
	if !ok {
		return false
	}
	for path := range binding.RequestLimits {
		if _, hard := binding.AdmissionLimitsFor(path); hard {
			return true
		}
	}
	return false
}

func omniDeclaredWireReject(info *relaycommon.RelayInfo, body []byte, raw map[string]any) *model.OmniBindingRejection {
	binding, path, ok := omniRequestBinding(info, info.GetChannelID())
	if !ok {
		return nil
	}
	if info.ChannelType == 14 && raw["messages"] != nil {
		path = "/v1/messages"
	}
	limits, hard := binding.AdmissionLimitsFor(path)
	if !hard {
		return nil
	}
	output := int64(0)
	scan := func(part map[string]any) bool {
		for _, key := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens", "maxOutputTokens"} {
			if value, exists := part[key]; exists && value != nil {
				n, ok := value.(float64)
				if !ok || n < 0 || n > 1<<30 || math.Trunc(n) != n {
					return false
				}
				output = max(output, int64(n))
			}
		}
		return true
	}
	valid := scan(raw)
	if config, ok := raw["generationConfig"].(map[string]any); ok {
		valid = valid && scan(config)
	}
	if !valid {
		return &model.OmniBindingRejection{Reason: "request_envelope_invalid"}
	}
	if binding.VerifiedPathsOnly && limits.MaxOutputTokens > 0 && output == 0 {
		return &model.OmniBindingRejection{Reason: "route_output_budget_required"}
	}
	if omniProviderDecidesTextLimits(info, path) {
		return nil
	}
	return binding.RejectEnvelope(path, int64(info.GetEstimatePromptTokens()), output, int64(len(body)))
}

func OmniBindingRequestReject(info *relaycommon.RelayInfo, id int, required []string) *model.OmniBindingRejection {
	input := info.GetEstimatePromptTokens()
	if b, path, ok := omniRequestBinding(info, id); ok {
		if b.VerifiedPathsOnly && model.OmniCommerceCheckChannelPath(info.UserId, info.OmniPoolID, info.RequestId, info.GetBillingModelName(), id, path, info.OmniRoutingSnapshot) != nil {
			return &model.OmniBindingRejection{Reason: "route_funding_not_verified"}
		}
		if limits, hard := b.AdmissionLimitsFor(path); omniProviderDecidesTextLimits(info, path) ||
			hard && (limits.MaxInputTokens > 0 || limits.MaxContextTokens > 0) {
			input = 0
		}
	}
	if rejection := model.OmniBindingReject(info.OmniRegistryVersion, info.OriginModelName, id, input, required); rejection != nil {
		return rejection
	}
	return OmniDeclaredRequestReject(info, id)
}

func OmniVerifiedPathsOnly(info *relaycommon.RelayInfo, id int) bool {
	binding, _, ok := omniRequestBinding(info, id)
	return ok && binding.VerifiedPathsOnly
}

// Only generated defaults on routes with enforced envelopes may shrink.
func OmniAdjustDefaultOutputLimit(info *relaycommon.RelayInfo) (bool, error) {
	if info == nil || !info.OmniOutputDefaulted {
		return false, nil
	}
	registry := model.OmniRegistryAt(info.OmniRegistryVersion)
	name := registry.Canonical(info.GetBillingModelName())
	path, err := url.Parse(info.RequestURLPath)
	if err != nil {
		return false, err
	}
	if omniProviderDecidesTextLimits(info, path.Path) {
		return false, nil
	}
	hasHard := false
	for _, b := range registry.Models[name].Bindings {
		if _, ok := b.AdmissionLimitsFor(path.Path); ok {
			hasHard = true
			break
		}
	}
	if !hasHard {
		return false, nil
	}
	var allowed []int
	routing := OmniRoutingFor(info)
	if routing.Enabled && OmniRoutingUserAllowed(routing, info.UserId) {
		allowed = routing.Models[name]
	}
	var shared *model.OmniCommerceProfile
	if info.OmniPoolID != "" && model.OmniCommerceReady() {
		_, profile, e := model.OmniSharedAuthorization(info.UserId, info.TokenId, info.OmniPoolID, name, info.OmniRoutingSnapshot)
		if e != nil {
			return false, nil
		} // Preserve the shared gate's original permission status.
		var routes map[string][]int
		if json.Unmarshal([]byte(profile.Models), &routes) != nil {
			return false, nil
		}
		allowed = routes[name]
		shared = &profile
	}
	body, err := json.Marshal(info.Request)
	if err != nil {
		return false, err
	}
	protocol := ""
	switch info.Request.(type) {
	case *dto.GeneralOpenAIRequest:
		protocol = "openai"
	case *dto.ClaudeRequest:
		protocol = "claude"
	case *dto.OpenAIResponsesRequest:
		protocol = "responses"
	case *dto.GeminiChatRequest:
		protocol = "gemini"
	}
	largest := int64(0)
	var best *model.OmniBindingRejection
	for _, b := range registry.Models[name].Bindings {
		if b.Status != "verified" || (allowed != nil && !slices.Contains(allowed, b.ChannelID)) {
			continue
		}
		candidate, accepted := b.AcceptedOutputForPath(name, path.Path)
		if !accepted && protocol != "" && !slices.Contains(b.Protocols, protocol) {
			continue
		}
		if !accepted {
			candidate = int64(b.AcceptedOutputTokens)
			if candidate <= 0 {
				candidate = 4096
			}
		}
		if limits, hard := b.AdmissionLimitsFor(path.Path); hard {
			if failure := b.RejectEnvelope(path.Path, int64(info.GetEstimatePromptTokens()), 1, int64(len(body))); failure != nil {
				best = failure
				continue
			}
			if limits.MaxOutputTokens > 0 {
				candidate = limits.MaxOutputTokens
			}
			if limits.MaxContextTokens > 0 {
				candidate = min(candidate, limits.MaxContextTokens-int64(info.GetEstimatePromptTokens())-limits.InputOverheadTokens)
			}
		}
		if shared != nil {
			bound, e := shared.Capability(name, b.ChannelID, path.Path)
			if e != nil || int64(len(body)) > bound.MaxRequestBytes {
				continue
			}
			candidate = min(candidate, bound.MaxOutputTokens)
		}
		largest = max(largest, candidate)
	}
	if largest <= 0 {
		if best != nil {
			return false, best
		}
		return false, nil
	}
	if largest == omniRequestCandidateOutput(info) {
		return false, nil
	}
	value := uint(largest)
	switch r := info.Request.(type) {
	case *dto.GeneralOpenAIRequest:
		r.MaxTokens = nil
		r.MaxCompletionTokens = &value
	case *dto.ClaudeRequest:
		r.MaxTokens = &value
	case *dto.OpenAIResponsesRequest:
		r.MaxOutputTokens = &value
	case *dto.GeminiChatRequest:
		r.GenerationConfig.MaxOutputTokens = &value
	}
	return true, nil
}

// The stock Claude tokenizer omits opaque thinking history. Bound its extra
// input conservatively by bytes without using it as customer billable usage.
func OmniNativeHistoryInputBound(info *relaycommon.RelayInfo) int {
	request, ok := info.Request.(*dto.ClaudeRequest)
	if !ok {
		return 0
	}
	bytesBound := 0
	for _, message := range request.Messages {
		if message.Role != "assistant" {
			continue
		}
		blocks, _ := message.ParseContent()
		for _, block := range blocks {
			switch block.Type {
			case "thinking":
				if block.Thinking != nil {
					bytesBound += len(*block.Thinking)
				}
				bytesBound += len(block.Signature)
			case "redacted_thinking":
				bytesBound += len(block.Data)
			}
		}
	}
	return bytesBound
}

// Only report known field names and a closed list of block types. Unknown
// client strings are replaced with a constant instead of entering logs.
func omniRequestShapeHint(info *relaycommon.RelayInfo) (string, string) {
	data, err := json.Marshal(info.Request)
	if err != nil {
		return "request", ""
	}
	var raw map[string]any
	if json.Unmarshal(data, &raw) != nil {
		return "request", ""
	}
	for _, key := range []string{"previous_response_id", "conversation", "audio", "modalities", "container", "context_management", "mcp_servers", "service_tier", "speed", "cachedContent", "requests"} {
		if raw[key] != nil {
			return key, ""
		}
	}
	if request, ok := info.Request.(*dto.ClaudeRequest); ok {
		for _, message := range request.Messages {
			blocks, _ := message.ParseContent()
			for _, block := range blocks {
				switch block.Type {
				case "image", "document", "server_tool_use", "web_search_tool_result", "web_fetch_tool_result":
					return "messages.content", block.Type
				case "thinking":
					if !info.OmniNativeClaude || message.Role != "assistant" || block.Signature == "" {
						return "messages.content", block.Type
					}
				case "redacted_thinking":
					if !info.OmniNativeClaude || message.Role != "assistant" || block.Data == "" {
						return "messages.content", block.Type
					}
				case "text", "tool_use", "tool_result":
				default:
					return "messages.content", "unsupported"
				}
			}
		}
	}
	if raw["tools"] != nil {
		return "tools", ""
	}
	if raw["input"] != nil {
		return "input", ""
	}
	return "messages.content", ""
}
