package service

import (
	"encoding/json"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

func TestOmniPricedResponsesClientTool(t *testing.T) {
	for _, tc := range []struct {
		name     string
		tool     string
		accepted bool
	}{
		{"custom_text", `{"type":"custom","name":"apply_patch","format":{"type":"text"}}`, true},
		{"custom_grammar", `{"type":"custom","name":"apply_patch","format":{"type":"grammar","syntax":"lark","definition":"start: /.+/"}}`, true},
		{"custom_missing_name", `{"type":"custom"}`, false},
		{"custom_blank_name", `{"type":"custom","name":" "}`, false},
		{"web_search", `{"type":"web_search"}`, false},
		{"code_interpreter", `{"type":"code_interpreter","container":{"type":"auto"}}`, false},
		{"file_search", `{"type":"file_search","vector_store_ids":["synthetic"]}`, false},
		{"image_generation", `{"type":"image_generation"}`, false},
		{"hosted_shell", `{"type":"shell","environment":{"type":"container_auto"}}`, false},
		{"mcp", `{"type":"mcp","server_label":"synthetic","server_url":"https://example.invalid"}`, false},
		{"unknown", `{"type":"unverified_future_tool"}`, false},
		{"function", `{"type":"function","name":"ping","parameters":{"type":"object","properties":{}}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var request dto.OpenAIResponsesRequest
			body := `{"model":"deepseek-v4-flash","input":"synthetic","tools":[` + tc.tool + `]}`
			if err := json.Unmarshal([]byte(body), &request); err != nil {
				t.Fatal(err)
			}
			err := OmniValidatePricedRequest(&relaycommon.RelayInfo{Request: &request, OriginModelName: request.Model, OmniPriceQuote: []byte(`{"restrictions":["client_custom_tools_token_priced"]}`)})
			if (err == nil) != tc.accepted {
				t.Fatalf("accepted=%t error=%v", tc.accepted, err)
			}
		})
	}
}

func TestOmniPricedCustomToolNeedsVerifiedQuote(t *testing.T) {
	var request dto.OpenAIResponsesRequest
	if err := json.Unmarshal([]byte(`{"model":"deepseek-v4-flash","input":"synthetic","tools":[{"type":"custom","name":"apply_patch"}]}`), &request); err != nil {
		t.Fatal(err)
	}
	if OmniValidatePricedRequest(&relaycommon.RelayInfo{Request: &request, OriginModelName: request.Model, OmniPriceQuote: []byte(`{"restrictions":[]}`)}) == nil {
		t.Fatal("custom tool accepted without verified token quote")
	}
}

func TestOmniPricedChatDoesNotAcceptResponsesCustomTool(t *testing.T) {
	var request dto.GeneralOpenAIRequest
	if err := json.Unmarshal([]byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"synthetic"}],"tools":[{"type":"custom","function":{"name":"apply_patch"}}]}`), &request); err != nil {
		t.Fatal(err)
	}
	if OmniValidatePricedRequest(&relaycommon.RelayInfo{Request: &request, OriginModelName: request.Model}) == nil {
		t.Fatal("Responses-only custom tool accepted on Chat")
	}
}
