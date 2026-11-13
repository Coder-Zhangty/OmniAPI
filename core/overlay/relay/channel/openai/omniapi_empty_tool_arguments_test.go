package openai

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func omniEmptyToolRequest(schema string) map[string]any {
	var raw map[string]any
	_ = json.Unmarshal([]byte(`{"model":"test-model","stream":true,"messages":[{"role":"user","content":"Call ping."}],"tools":[{"type":"function","function":{"name":"ping","parameters":`+schema+`}}],"tool_choice":{"type":"function","function":{"name":"ping"}}}`), &raw)
	return raw
}

func omniEmptyToolFrames(arguments, finish string, done bool) []string {
	function, _ := json.Marshal(map[string]any{"name": "ping", "arguments": arguments})
	lines := []string{`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_ping","type":"function","function":` + string(function) + `}]}}]}`}
	if finish != "" {
		lines = append(lines, `data: {"choices":[{"index":0,"delta":{},"finish_reason":"`+finish+`"}]}`)
	}
	if done {
		lines = append(lines, "data: [DONE]")
	}
	return lines
}

func TestOmniEmptyToolArgumentsHTTP(t *testing.T) {
	c, info := omniHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, line := range omniEmptyToolFrames("", "tool_calls", true) {
			fmt.Fprint(w, line+"\n\n")
		}
	})
	raw := omniEmptyToolRequest(`{"type":"object","properties":{}}`)
	body, _ := json.Marshal(raw)
	response, err := (&Adaptor{}).omniRequest(c, info, strings.NewReader(string(body)))
	if err != nil {
		t.Fatal("completed no-argument tool rejected:", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	var arguments string
	for _, line := range strings.Split(string(data), "\n") {
		frame, _, _, _ := omniParse(line)
		choices, _ := frame["choices"].([]any)
		for _, value := range choices {
			choice := value.(map[string]any)
			delta, _ := choice["delta"].(map[string]any)
			calls, _ := delta["tool_calls"].([]any)
			for _, value := range calls {
				call := value.(map[string]any)
				function := call["function"].(map[string]any)
				part, _ := function["arguments"].(string)
				arguments += part
			}
		}
	}
	if arguments != "{}" || !strings.Contains(string(data), "[DONE]") {
		t.Fatalf("client did not receive complete empty object: %q", arguments)
	}
}

func TestOmniEmptyToolArgumentsValidation(t *testing.T) {
	for _, tc := range []struct {
		name, schema, arguments, finish string
		done, accepted                  bool
	}{
		{"empty object", `{"type":"object","properties":{}}`, "", "tool_calls", true, true},
		{"optional property", `{"type":"object","properties":{"message":{"type":"string"}}}`, "", "tool_calls", true, true},
		{"required property", `{"type":"object","required":["message"],"properties":{"message":{"type":"string"}}}`, "", "tool_calls", true, false},
		{"composed required", `{"type":"object","allOf":[{"required":["message"]}]}`, "", "tool_calls", true, false},
		{"partial JSON", `{"type":"object"}`, "{", "tool_calls", true, false},
		{"token limit", `{"type":"object"}`, "", "length", true, false},
		{"missing finish", `{"type":"object"}`, "", "", true, false},
		{"missing done", `{"type":"object"}`, "", "tool_calls", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines := omniEmptyToolFrames(tc.arguments, tc.finish, tc.done)
			err := omniValidateBuffered(lines, omniEmptyToolRequest(tc.schema))
			if (err == nil) != tc.accepted {
				t.Fatalf("accepted=%v, error=%v", tc.accepted, err)
			}
		})
	}
}
