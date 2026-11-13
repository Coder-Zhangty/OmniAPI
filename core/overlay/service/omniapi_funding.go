package service

import (
	"encoding/json"
	"errors"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/url"
	"slices"
	"strconv"
)

func OmniEnsureOutputLimit(info *relaycommon.RelayInfo) {
	limit := uint(0)
	protocol := ""
	path := ""
	switch info.Request.(type) {
	case *dto.GeneralOpenAIRequest:
		protocol = "openai"
		path = "/v1/chat/completions"
	case *dto.ClaudeRequest:
		protocol = "claude"
		path = "/v1/messages"
	case *dto.OpenAIResponsesRequest:
		protocol = "responses"
		path = "/v1/responses"
	case *dto.GeminiChatRequest:
		protocol = "gemini"
	}
	registry := model.OmniRegistryAt(info.OmniRegistryVersion)
	for _, binding := range registry.Models[registry.Canonical(info.GetBillingModelName())].Bindings {
		specific, hasSpecific := binding.AcceptedOutputForPath(registry.Canonical(info.GetBillingModelName()), path)
		accepted := binding.AcceptedOutputTokens
		if hasSpecific && specific > 0 {
			accepted = int(specific)
		}
		if binding.Status == "verified" && accepted > 0 && (hasSpecific || protocol == "" || slices.Contains(binding.Protocols, protocol)) {
			limit = max(limit, uint(accepted))
		}
	}
	if limit == 0 {
		limit = 4096
	}
	if info.OmniPoolID != "" && model.OmniCommerceReady() {
		if _, profile, err := model.OmniSharedAuthorization(info.UserId, info.TokenId, info.OmniPoolID, info.GetBillingModelName(), info.OmniRoutingSnapshot); err == nil {
			var routes map[string][]int
			_ = json.Unmarshal([]byte(profile.Models), &routes)
			if path, err := url.Parse(info.RequestURLPath); err == nil {
				largest := int64(0)
				body, _ := json.Marshal(info.Request)
				for _, id := range routes[info.GetBillingModelName()] {
					if bound, err := profile.Capability(info.GetBillingModelName(), id, path.Path); err == nil {
						if int64(len(body)) <= bound.MaxRequestBytes {
							largest = max(largest, bound.MaxOutputTokens)
						}
					}
				}
				if largest > 0 {
					limit = uint(largest)
				}
			}
		}
	}
	switch r := info.Request.(type) {
	case *dto.GeneralOpenAIRequest:
		if r.MaxTokens != nil && *r.MaxTokens == 0 {
			r.MaxTokens = nil
		}
		if r.MaxCompletionTokens != nil && *r.MaxCompletionTokens == 0 {
			r.MaxCompletionTokens = nil
		}
		if (r.MaxTokens == nil || *r.MaxTokens == 0) && (r.MaxCompletionTokens == nil || *r.MaxCompletionTokens == 0) {
			r.MaxCompletionTokens = &limit
			info.OmniOutputDefaulted = true
		}
	case *dto.ClaudeRequest:
		if r.MaxTokens == nil || *r.MaxTokens == 0 {
			r.MaxTokens = &limit
			info.OmniOutputDefaulted = true
		}
	case *dto.GeminiChatRequest:
		if r.GenerationConfig.MaxOutputTokens == nil || *r.GenerationConfig.MaxOutputTokens == 0 {
			r.GenerationConfig.MaxOutputTokens = &limit
			info.OmniOutputDefaulted = true
		}
	case *dto.OpenAIResponsesRequest:
		if r.MaxOutputTokens == nil || *r.MaxOutputTokens == 0 {
			r.MaxOutputTokens = &limit
			info.OmniOutputDefaulted = true
		}
	}
}
func OmniReservedOutput(info *relaycommon.RelayInfo) int {
	if !info.OmniBillingActive && !info.OmniPriceProtected {
		return 0
	}
	switch r := info.Request.(type) {
	case *dto.GeneralOpenAIRequest:
		a, b := uint(0), uint(0)
		if r.MaxTokens != nil {
			a = *r.MaxTokens
		}
		if r.MaxCompletionTokens != nil {
			b = *r.MaxCompletionTokens
		}
		count := 1
		if r.N != nil {
			count = max(1, *r.N)
		}
		return int(max(a, b)) * count
	case *dto.ClaudeRequest:
		if r.MaxTokens != nil {
			return int(*r.MaxTokens)
		}
	case *dto.GeminiChatRequest:
		if r.GenerationConfig.MaxOutputTokens != nil {
			count := 1
			if r.GenerationConfig.CandidateCount != nil {
				count = max(1, *r.GenerationConfig.CandidateCount)
			}
			return int(*r.GenerationConfig.MaxOutputTokens) * count
		}
	case *dto.OpenAIResponsesRequest:
		if r.MaxOutputTokens != nil {
			return int(*r.MaxOutputTokens)
		}
	}
	return 0
}

// For verified plain text, serialized UTF-8 bytes conservatively bound token
// input across supported tokenizers. Billing still uses actual provider usage.
func OmniReservedInputTokens(info *relaycommon.RelayInfo, estimated int) int {
	if (info.OmniBillingActive && OmniSubscriptionRequest(info)) || info.OmniPriceProtected {
		if data, err := json.Marshal(info.Request); err == nil {
			overhead := 0
			if info.OmniSharedDynamic {
				overhead = int(info.OmniSharedInputOverhead)
			}
			// Reserve transport framing before funding. Native/compatibility
			// adapters add fields after this point; the final-wire check uses
			// its actual size and releases any unused padding on settlement.
			framing := 64
			if info.OmniPoolID != "" && info.OmniSharedDynamic {
				framing = 4096
			}
			return max(estimated, len(data)+framing+overhead)
		}
	}
	return estimated
}
func OmniReserveMultiplier(info *relaycommon.RelayInfo, value float64) float64 {
	if info.OmniBillingActive {
		return max(1, value)
	}
	return value
}
func OmniInputReserveFactor(info *relaycommon.RelayInfo) float64 {
	if info.OmniPriceProtected {
		var row struct {
			Retail map[string]string `json:"retail"`
			Tiers  []struct {
				Retail map[string]string `json:"retail"`
			} `json:"tiers"`
		}
		data := info.OmniPriceQuote
		if len(data) == 0 {
			data = model.GetOmniPrices().Models[info.OriginModelName]
		}
		if json.Unmarshal(data, &row) != nil {
			return 1
		}
		factor := 1.0
		prices := []map[string]string{row.Retail}
		for _, tier := range row.Tiers {
			prices = append(prices, tier.Retail)
		}
		for _, price := range prices {
			input, _ := strconv.ParseFloat(price["input"], 64)
			if input <= 0 {
				continue
			}
			for _, key := range []string{"cache_read", "cache_write", "cache_write_1h"} {
				v, _ := strconv.ParseFloat(price[key], 64)
				factor = max(factor, v/input)
			}
		}
		return factor
	}
	if !info.OmniBillingActive {
		return 1
	}
	return model.OmniSubscriptionReserveFactor(info.UserId, info.GetBillingModelName())
}

type OmniFunding struct {
	uid                  int
	requestID, modelName string
	asyncTask            bool
	reserved             int64
	record               *model.OmniReservation
	flexibleAllowance    bool
	poolID               string
	tokenID              int
	shared               bool
	requestOptions       map[string]string
}

func (f *OmniFunding) Source() string {
	if f.record != nil {
		return f.record.Source
	}
	return "omni_subscription"
}
func (f *OmniFunding) PreConsume(amount int) error {
	var r *model.OmniReservation
	var err error
	if f.poolID != "" {
		_, _, e := model.OmniSharedAuthorization(f.uid, f.tokenID, f.poolID, f.modelName, f.requestOptions)
		if e != nil {
			return e
		}
		// Durable personal reservations win on retries; never switch a request's source.
		if prior, e := model.OmniRequestRecord(f.requestID); e == nil {
			if prior.UserID != f.uid || prior.SharedPoolID != f.poolID || prior.SharedTokenID != f.tokenID || prior.State != "pending" {
				return errors.New("request owner conflict")
			}
			f.shared = prior.Source == "omni_shared"
			f.flexibleAllowance = prior.FlexibleAllowance
			f.record = &prior
			f.reserved = prior.Reserved
			return nil
		}
		hold, e := model.OmniSharedReserve(f.uid, f.tokenID, f.poolID, f.requestID, f.modelName, int64(amount), f.requestOptions)
		if e == nil {
			f.shared = true
			f.record = &model.OmniReservation{RequestID: f.requestID, UserID: f.uid, Source: "omni_shared", Reserved: hold.Reserved, State: "pending", BillingPolicy: hold.BillingPolicy, PayerID: hold.PayerID}
			f.reserved = hold.Reserved
			return nil
		}
		if !errors.Is(e, model.ErrOmniSharedUnavailable) {
			return e
		}
		r, err = model.OmniSharedPersonalReserve(f.uid, f.requestID, f.modelName, int64(amount), f.poolID, f.tokenID, f.requestOptions)
		if err != nil {
			return err
		}
		f.flexibleAllowance = true
		f.record = r
		f.reserved = r.Reserved
		return nil
	}
	if f.flexibleAllowance {
		r, err = model.OmniReserveAllowance(f.uid, f.requestID, f.modelName, int64(amount), f.requestOptions)
	} else {
		r, err = model.OmniReserve(f.uid, f.requestID, f.modelName, int64(amount), f.requestOptions)
	}
	if err != nil {
		return err
	}
	f.record = r
	f.reserved = r.Reserved
	return nil
}
func (f *OmniFunding) Settle(delta int) error {
	if f.record != nil && f.record.BillingPolicy == model.OmniCustomerBillingV1 && f.asyncTask {
		return nil
	}
	if f.shared {
		if f.asyncTask {
			return nil
		}
		return model.OmniSharedSettle(f.requestID, f.reserved+int64(delta))
	}
	err := model.OmniAdjust(f.requestID, f.reserved+int64(delta), !f.asyncTask)
	if err == nil {
		f.reserved += int64(delta)
	}
	return err
}
func (f *OmniFunding) Refund() error {
	if f.shared {
		return refundWithRetry(func() error { return model.OmniSharedSettle(f.requestID, 0) })
	}
	return refundWithRetry(func() error { return model.OmniAdjust(f.requestID, 0, true) })
}
func (f *OmniFunding) Adjust(delta int) error {
	if f.record != nil && f.record.BillingPolicy == model.OmniCustomerBillingV1 {
		return nil
	}
	if f.shared {
		return errors.New("共享池请求不允许提交后扩大预留，请重试")
	}
	err := model.OmniAdjust(f.requestID, f.reserved+int64(delta), false)
	if err == nil {
		f.reserved += int64(delta)
	}
	return err
}
func NewOmniBillingSession(c *gin.Context, info *relaycommon.RelayInfo, amount int) (*BillingSession, *types.NewAPIError) {
	if info.OmniPoolID != "" {
		if info.OmniSharedDynamic {
			if info.TieredBillingSnapshot == nil || amount <= 0 || int64(amount) > info.OmniSharedUpperQuota {
				return nil, types.NewErrorWithStatusCode(errors.New("请求所需预留超过该能力的已验收上界"), types.ErrorCodeInvalidRequest, http.StatusForbidden, types.ErrOptionWithSkipRetry())
			}
			info.OmniSharedUpperQuota = int64(amount)
		} else {
			amount = max(amount, int(info.OmniSharedUpperQuota))
		}
	}
	if amount <= 0 {
		return nil, types.NewErrorWithStatusCode(errors.New("positive full reservation required"), types.ErrorCodeInsufficientUserQuota, http.StatusForbidden, types.ErrOptionWithSkipRetry())
	}
	info.ForcePreConsume = true
	info.OmniBillingActive = true
	f := &OmniFunding{uid: info.UserId, requestID: info.RequestId, modelName: info.GetBillingModelName(), poolID: info.OmniPoolID, tokenID: info.TokenId, requestOptions: info.OmniRoutingSnapshot}
	switch info.Request.(type) {
	case *dto.GeneralOpenAIRequest, *dto.ClaudeRequest, *dto.GeminiChatRequest, *dto.OpenAIResponsesRequest, *dto.ImageRequest:
		f.flexibleAllowance = true
	}
	if f.poolID != "" {
		f.flexibleAllowance = false
	}
	if model.OmniCustomerBillingFor(info.UserId) {
		f.flexibleAllowance = true
	}
	if f.poolID == "" && !OmniSubscriptionRequest(info) {
		f.modelName = ""
	}
	s := &BillingSession{relayInfo: info, funding: f}
	if f.flexibleAllowance || f.poolID != "" {
		if apiErr := s.preConsumeOmniAllowance(c, amount); apiErr != nil {
			return nil, apiErr
		}
		return s, nil
	}
	if err := s.preConsume(c, amount); err != nil {
		return nil, err
	}
	return s, nil
}
func OmniSubscriptionRequest(info *relaycommon.RelayInfo) bool {
	if model.GetOmniPolicy().ModelScope == "all_available" {
		status, _, _ := model.OmniPriceAvailability(info.GetBillingModelName())
		return status == "available"
	}
	return OmniSafeTextRequest(info)
}

func OmniSafeTextRequest(info *relaycommon.RelayInfo) bool {
	return omniSafeTokenRequest(info, false)
}

func omniSafeTokenRequest(info *relaycommon.RelayInfo, allowImages bool) bool {
	switch info.Request.(type) {
	case *dto.GeneralOpenAIRequest, *dto.ClaudeRequest, *dto.GeminiChatRequest, *dto.OpenAIResponsesRequest:
	default:
		return false
	}
	data, err := json.Marshal(info.Request)
	if err != nil {
		return false
	}
	var raw map[string]any
	if json.Unmarshal(data, &raw) != nil {
		return false
	}
	for _, key := range []string{"previous_response_id", "conversation", "audio", "modalities", "container", "context_management", "mcp_servers", "service_tier", "speed", "cachedContent", "requests"} {
		if v := raw[key]; v != nil {
			return false
		}
	}
	if tools, ok := raw["tools"].([]any); ok {
		for _, entry := range tools {
			tool, ok := entry.(map[string]any)
			if !ok {
				return false
			}
			if _, gemini := info.Request.(*dto.GeminiChatRequest); gemini {
				if len(tool) != 1 || tool["functionDeclarations"] == nil {
					return false
				}
			} else if _, claude := info.Request.(*dto.ClaudeRequest); claude {
				if handled, err := omniNativeClientTool(info, tool); handled {
					if err != nil {
						return false
					}
					continue
				}
				if tool["type"] != nil || tool["input_schema"] == nil {
					return false
				}
			} else if tool["type"] != "function" {
				if _, responses := info.Request.(*dto.OpenAIResponsesRequest); !responses {
					return false
				}
				if handled, err := omniResponsesClientTool(info, tool); !handled || err != nil {
					return false
				}
			}
		}
	}
	if _, responses := info.Request.(*dto.OpenAIResponsesRequest); responses {
		if _, ok := raw["input"].(string); ok {
			return true
		}
		items, ok := raw["input"].([]any)
		if !ok || len(items) == 0 {
			return false
		}
		for _, entry := range items {
			item, ok := entry.(map[string]any)
			if !ok {
				return false
			}
			kind, _ := item["type"].(string)
			if kind == "function_call" || kind == "function_call_output" {
				continue
			}
			if kind == "custom_tool_call" || kind == "custom_tool_call_output" {
				if !omniQuoteRestriction(info, "client_custom_tools_token_priced") {
					return false
				}
				continue
			}
			if kind == "tool_search_call" || kind == "tool_search_output" {
				if item["execution"] != "client" || !omniQuoteRestriction(info, "client_tool_search_token_priced") {
					return false
				}
				continue
			}
			if kind != "" && kind != "message" {
				return false
			}
			if !omniTokenContent(item["content"], allowImages) {
				return false
			}
		}
		return true
	}
	if _, gemini := info.Request.(*dto.GeminiChatRequest); gemini {
		if config, ok := raw["generationConfig"].(map[string]any); ok {
			if modes, ok := config["responseModalities"].([]any); ok {
				for _, mode := range modes {
					if mode != "TEXT" {
						return false
					}
				}
			}
			for _, key := range []string{"speechConfig", "imageConfig"} {
				if config[key] != nil {
					return false
				}
			}
		}
		contents, ok := raw["contents"].([]any)
		if !ok || len(contents) == 0 {
			return false
		}
		if sys := raw["systemInstruction"]; sys != nil {
			contents = append(contents, sys)
		}
		for _, entry := range contents {
			row, ok := entry.(map[string]any)
			if !ok {
				return false
			}
			parts, ok := row["parts"].([]any)
			if !ok {
				return false
			}
			for _, part := range parts {
				p, ok := part.(map[string]any)
				if !ok || len(p) != 1 {
					return false
				}
				if _, ok = p["text"].(string); !ok && p["functionCall"] == nil && p["functionResponse"] == nil && !(allowImages && (p["inlineData"] != nil || p["inline_data"] != nil)) {
					return false
				}
			}
		}
		return true
	}
	if sys := raw["system"]; sys != nil && !omniTokenContent(sys, allowImages) {
		return false
	}
	messages, ok := raw["messages"].([]any)
	if !ok || len(messages) == 0 {
		return false
	}
	for _, entry := range messages {
		m, ok := entry.(map[string]any)
		if !ok {
			return false
		}
		content := m["content"]
		if info.OmniNativeClaude && m["role"] == "assistant" {
			content = omniClaudeTextShape(content)
		}
		if !omniTokenContent(content, allowImages) && !(m["content"] == nil && (m["tool_calls"] != nil || m["function_call"] != nil)) {
			return false
		}
	}
	return true
}

// The normalized copy is used only for the bounded-text shape check. The
// complete signed history still counts toward the wire-size bound and is sent unchanged.
func omniClaudeTextShape(content any) any {
	parts, ok := content.([]any)
	if !ok {
		return content
	}
	checked := make([]any, 0, len(parts))
	for _, part := range parts {
		block, _ := part.(map[string]any)
		switch block["type"] {
		case "thinking":
			thinking, textOK := block["thinking"].(string)
			signature, signed := block["signature"].(string)
			_, hasText := block["thinking"]
			// Some clients omit the empty summary when replaying a signed block.
			if signed && signature != "" && (textOK || !hasText) {
				checked = append(checked, map[string]any{"type": "text", "text": thinking})
				continue
			}
		case "redacted_thinking":
			if data, valid := block["data"].(string); valid && data != "" {
				checked = append(checked, map[string]any{"type": "text", "text": data})
				continue
			}
		}
		checked = append(checked, part)
	}
	return checked
}

func omniTextContent(content any) bool {
	return omniTokenContent(content, false)
}

func omniTokenContent(content any, allowImages bool) bool {
	if _, ok := content.(string); ok {
		return true
	}
	parts, ok := content.([]any)
	if !ok || len(parts) == 0 {
		return false
	}
	for _, entry := range parts {
		part, ok := entry.(map[string]any)
		if !ok {
			return false
		}
		switch part["type"] {
		case "image", "image_url", "input_image":
			if !allowImages {
				return false
			}
			continue
		case "tool_use":
			if part["input"] == nil {
				return false
			}
			continue
		case "tool_result":
			if !omniTokenContent(part["content"], allowImages) {
				return false
			}
			continue
		case "text", "input_text", "output_text":
		default:
			return false
		}
		if _, ok := part["text"].(string); !ok {
			return false
		}
	}
	return true
}

// Reject numerically unsafe limits before converting them to quota integers.
func OmniValidateOutputLimits(info *relaycommon.RelayInfo) error {
	var limits []*uint
	var count *int
	switch r := info.Request.(type) {
	case *dto.GeneralOpenAIRequest:
		limits = []*uint{r.MaxTokens, r.MaxCompletionTokens}
		count = r.N
	case *dto.ClaudeRequest:
		limits = []*uint{r.MaxTokens}
	case *dto.GeminiChatRequest:
		limits = []*uint{r.GenerationConfig.MaxOutputTokens}
		count = r.GenerationConfig.CandidateCount
	case *dto.OpenAIResponsesRequest:
		limits = []*uint{r.MaxOutputTokens}
	}
	if count != nil && (*count < 1 || *count > 128) {
		return errors.New("invalid candidate count")
	}
	for _, limit := range limits {
		if limit != nil && *limit > 1<<30 {
			return errors.New("output limit exceeds supported reservation range")
		}
	}
	return nil
}

// A pool cannot fund a request whose hidden input or hosted work has no fixed
// upper bound. Reject it before choosing any shared or personal fallback source.
func OmniValidateSharedReservation(info *relaycommon.RelayInfo) error {
	if info.OmniPoolID == "" {
		return nil
	}
	if model.OmniCommerceReady() {
		_, profile, err := model.OmniSharedAuthorization(info.UserId, info.TokenId, info.OmniPoolID, info.GetBillingModelName(), info.OmniRoutingSnapshot)
		if err != nil {
			return err
		}
		if profile.CapabilityMatrix != "" {
			return omniValidateCertifiedShared(info, profile)
		}
	}
	switch info.Request.(type) {
	case *dto.GeneralOpenAIRequest, *dto.ClaudeRequest, *dto.GeminiChatRequest, *dto.OpenAIResponsesRequest:
	case *dto.ImageRequest:
		if !info.OmniPriceProtected {
			return errors.New("共享池图片请求需已核验的固定报价")
		}
		handled, err := omniValidateImageQuote(info)
		if !handled {
			return errors.New("共享池图片请求缺少已核验上界")
		}
		return err
	default:
		return errors.New("该请求类型尚未核验共享池预留上界，不会转扣个人资金")
	}
	data, err := json.Marshal(info.Request)
	if err != nil {
		return err
	}
	var raw map[string]any
	if json.Unmarshal(data, &raw) != nil {
		return errors.New("无法核验共享请求")
	}
	for _, key := range []string{"previous_response_id", "conversation", "cachedContent", "context_management", "audio", "modalities", "container", "mcp_servers"} {
		if raw[key] != nil {
			return errors.New("该请求含尚未核验的历史上下文或附加输入，不会转扣个人资金")
		}
	}
	if omniRequestMedia(raw, false) {
		return errors.New("该媒体输入的最大共享预留尚未核验，不会转扣个人资金")
	}
	if tools, ok := raw["tools"].([]any); ok {
		for _, entry := range tools {
			tool, ok := entry.(map[string]any)
			if !ok {
				return errors.New("invalid shared tool definition")
			}
			kind, _ := tool["type"].(string)
			switch kind {
			case "", "function", "custom", "namespace":
			case "tool_search":
				if tool["execution"] != "client" {
					return errors.New("共享池托管工具预留上界尚未核验")
				}
			default:
				return errors.New("共享池托管工具预留上界尚未核验")
			}
		}
	}
	return nil
}
