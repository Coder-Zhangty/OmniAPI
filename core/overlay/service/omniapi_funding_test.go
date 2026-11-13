package service

import (
	"encoding/json"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"testing"
)
func TestOmniVerifiedWalletReservesMaximumOutput(t *testing.T) {
 limit:=uint(750)
 info:=&relaycommon.RelayInfo{OmniPriceProtected:true,Request:&dto.GeneralOpenAIRequest{MaxCompletionTokens:&limit}}
 if got:=OmniReservedOutput(info);got!=750{t.Fatalf("reserved %d instead of maximum output",got)}
}

func TestOmniTextProtocolReservation(t *testing.T) {
	claude := &dto.ClaudeRequest{}
	json.Unmarshal([]byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`), claude)
	gemini := &dto.GeminiChatRequest{}
	json.Unmarshal([]byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`), gemini)
	for _, request := range []dto.Request{claude, gemini} {
		info := &relaycommon.RelayInfo{Request: request, ForcePreConsume: true, OmniBillingActive: true}
		if !OmniSubscriptionRequest(info) {
			t.Fatal("plain text excluded")
		}
		if OmniReservedInputTokens(info, 1) <= 1 {
			t.Fatal("input was not conservatively reserved")
		}
		OmniEnsureOutputLimit(info)
		if OmniReservedOutput(info) != 4096 {
			t.Fatal("full output not reserved")
		}
	}
	json.Unmarshal([]byte(`{"contents":[{"parts":[{"inlineData":{"mimeType":"image/png","data":"x"}}]}]}`), gemini)
	if OmniSubscriptionRequest(&relaycommon.RelayInfo{Request: gemini}) {
		t.Fatal("image admitted")
	}
}

func TestOmniTextFunctionAndResponsesEligibility(t *testing.T) {
	chat := &dto.GeneralOpenAIRequest{}
	json.Unmarshal([]byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`), chat)
	if !OmniSubscriptionRequest(&relaycommon.RelayInfo{Request: chat}) {
		t.Fatal("local function should use text allowance")
	}
	response := &dto.OpenAIResponsesRequest{}
	json.Unmarshal([]byte(`{"input":"hi"}`), response)
	if !OmniSubscriptionRequest(&relaycommon.RelayInfo{Request: response}) {
		t.Fatal("stateless text Responses excluded")
	}
	json.Unmarshal([]byte(`{"input":"hi","previous_response_id":"previous"}`), response)
	if OmniSubscriptionRequest(&relaycommon.RelayInfo{Request: response}) {
		t.Fatal("unbounded stored state admitted")
	}
	response = &dto.OpenAIResponsesRequest{}
	json.Unmarshal([]byte(`{"input":"hi","tools":[{"type":"web_search_preview"}]}`), response)
	if OmniSubscriptionRequest(&relaycommon.RelayInfo{Request: response}) {
		t.Fatal("unknown paid tool admitted")
	}
}

func TestOmniMultipleCandidatesFullyReserved(t *testing.T) {
	limit := uint(100)
	count := 3
	request := &dto.GeneralOpenAIRequest{MaxCompletionTokens: &limit, N: &count}
	info := &relaycommon.RelayInfo{Request: request, OmniBillingActive: true}
	if err := OmniValidateOutputLimits(info); err != nil {
		t.Fatal(err)
	}
	if got := OmniReservedOutput(info); got != 300 {
		t.Fatalf("partial reservation: %d", got)
	}
	gemini := &dto.GeminiChatRequest{}
	gemini.GenerationConfig.MaxOutputTokens = &limit
	gemini.GenerationConfig.CandidateCount = &count
	info.Request = gemini
	if got := OmniReservedOutput(info); got != 300 {
		t.Fatal(got)
	}
	limit = uint(1 << 31)
	if OmniValidateOutputLimits(info) == nil {
		t.Fatal("unsafe limit accepted")
	}
	limit = 100
	count = -1
	if OmniValidateOutputLimits(info) == nil {
		t.Fatal("negative candidate count accepted")
	}
}
