package openai

import (
	"encoding/json"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestOmniSonnetForcedFunctionUsesValidatedAutoCompatibility(t *testing.T) {
	var raw map[string]any
	json.Unmarshal([]byte(`{"tool_choice":{"type":"function","function":{"name":"ping"}},"tools":[{"type":"function","function":{"name":"ping"}},{"type":"function","function":{"name":"other"}}]}`), &raw)
	converted := omniOpusToolRequest("claude-sonnet-5-5", raw)
	if converted["tool_choice"] != "auto" || len(converted["tools"].([]any)) != 1 {
		t.Fatal("verified Sonnet auto mode was not used with the selected function")
	}
	if _, ok := raw["tool_choice"].(map[string]any); !ok || len(raw["tools"].([]any)) != 2 {
		t.Fatal("original client contract was mutated")
	}
	if omniValidateToolResult(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "OK"}}}}, raw) == nil {
		t.Fatal("plain text must not satisfy required function choice")
	}
	if err := omniValidateToolResult(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"tool_calls": []any{map[string]any{"function": map[string]any{"name": "ping"}}}}}}}, raw); err != nil {
		t.Fatal(err)
	}
}

func TestOmniSonnetForcedFunctionNativeTransportValidatesBeforeCommit(t *testing.T) {
	for _, valid := range []bool{true, false} {
		t.Run(fmt.Sprint(valid), func(t *testing.T) {
			c, info := omniHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
				var wire map[string]any
				if json.NewDecoder(r.Body).Decode(&wire) != nil {
					t.Error("invalid converted request")
				}
				choice, _ := wire["tool_choice"].(map[string]any)
				tools, _ := wire["tools"].([]any)
				if r.URL.Path != "/v1/messages" || choice["type"] != "auto" || len(tools) != 1 {
					t.Error("forced mode was not converted to the verified native auto request")
					http.Error(w, "unsupported forced choice", 400)
					return
				}
				content := `{"type":"text","text":"OK"}`
				reason := "end_turn"
				if valid {
					content = `{"type":"tool_use","id":"call_test","name":"ping","input":{"message":"OK"}}`
					reason = "tool_use"
				}
				fmt.Fprintf(w, `{"id":"msg_test","type":"message","role":"assistant","model":"claude-sonnet-5-5","content":[%s],"stop_reason":%q,"usage":{"input_tokens":10,"output_tokens":10}}`, content, reason)
			})
			policy := service.GetOmniRouting()
			ids := policy.Models["test-model"]
			delete(policy.Models, "test-model")
			policy.Models["claude-sonnet-5-5"] = ids
			data, _ := json.Marshal(policy)
			common.OptionMapRWMutex.Lock()
			oldRegistry := common.OptionMap["OmniAPIModelRegistry"]
			common.OptionMap["OmniAPIModelRegistry"] = `{"enabled":false,"version":"sonnet-transport-fixture","models":{}}`
			common.OptionMap["OmniAPIRouting"] = string(data)
			common.OptionMapRWMutex.Unlock()
			t.Cleanup(func() {
				common.OptionMapRWMutex.Lock()
				common.OptionMap["OmniAPIModelRegistry"] = oldRegistry
				common.OptionMapRWMutex.Unlock()
			})
			model.DB.Model(&model.Channel{}).Where("id IN ?", ids).Updates(map[string]any{"models": "claude-sonnet-5-5", "type": 14})
			info.OriginModelName = "claude-sonnet-5-5"
			info.IsStream = false
			resp, err := (&Adaptor{}).omniRequest(c, info, strings.NewReader(`{"model":"claude-sonnet-5-5","max_tokens":1024,"messages":[{"role":"user","content":"Call ping."}],"tool_choice":{"type":"function","function":{"name":"ping"}},"tools":[{"type":"function","function":{"name":"ping","parameters":{"type":"object","properties":{"message":{"type":"string"}}}}},{"type":"function","function":{"name":"other","parameters":{"type":"object"}}}]}`))
			if valid {
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				body, _ := io.ReadAll(resp.Body)
				if !strings.Contains(string(body), `"ping"`) {
					t.Fatal("required function result missing")
				}
			} else if err == nil {
				resp.Body.Close()
				t.Fatal("plain native response committed as a required tool success")
			}
		})
	}
}

func TestOmniSonnetCompatibilityDoesNotRewriteUnverifiedToolKinds(t *testing.T) {
	for _, payload := range []string{
		`{"tool_choice":"required","tools":[{"type":"custom","name":"shell"}]}`,
		`{"tool_choice":{"type":"web_search"},"tools":[{"type":"web_search"}]}`,
		`{"tool_choice":"required","tools":[{"type":"function","function":{"name":"ping"}},{"type":"custom","name":"shell"}]}`,
	} {
		var raw map[string]any
		json.Unmarshal([]byte(payload), &raw)
		before, _ := json.Marshal(raw)
		after, _ := json.Marshal(omniOpusToolRequest("claude-sonnet-5-5", raw))
		if string(before) != string(after) {
			t.Fatal("unverified tool mode was silently changed")
		}
	}
}

func TestOmniRequiredToolContractRejectsPlainSuccess(t *testing.T) {
	var request, result map[string]any
	json.Unmarshal([]byte(`{"tool_choice":"required","tools":[{"type":"function","function":{"name":"ping"}}]}`), &request)
	json.Unmarshal([]byte(`{"choices":[{"message":{"content":"OK"},"finish_reason":"stop"}]}`), &result)
	if omniValidateJSONResult(result, request) == nil {
		t.Fatal("forced tool request silently accepted a plain text result")
	}
	json.Unmarshal([]byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"ping","arguments":"{\"message\":\"OK\"}"}}]},"finish_reason":"tool_calls"}]}`), &result)
	if err := omniValidateJSONResult(result, request); err != nil {
		t.Fatal(err)
	}
}
