package openai

import (
	"encoding/json"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"testing"
)

func TestOmniContinuationEligibility(t *testing.T) {
	var raw map[string]any
	json.Unmarshal([]byte(`{"messages":[{"role":"user","content":"hello"}]}`), &raw)
	if !omniPlainRequest(raw) {
		t.Fatal("plain text excluded")
	}
	raw["tools"] = []any{}
	if omniPlainRequest(raw) {
		t.Fatal("tools admitted")
	}
	delete(raw, "tools")
	raw["messages"] = []any{map[string]any{"role": "user", "content": []any{}}}
	if omniPlainRequest(raw) {
		t.Fatal("multimodal admitted")
	}
}
func TestOmniOverlapAndFrames(t *testing.T) {
	_,_,progress,_:=omniParse(`data: {"choices":[{"delta":{"reasoning_content":"","tool_calls":[]}}]}`)
	if progress{t.Fatal("empty reasoning heartbeat treated as progress")}
	if s := omniOverlap("hello, this is already written", "already written and new"); s != " and new" {
		t.Fatal(s)
	}
	_, text, effective, safe := omniParse(`data: {"choices":[{"delta":{"content":"hello"}}]}`)
	if text != "hello" || !effective || !safe {
		t.Fatal(text, effective, safe)
	}
	_, _, _, safe = omniParse(`data: {"choices":[{"delta":{"tool_calls":[]}}]}`)
	if safe {
		t.Fatal("tool response admitted")
	}
}

func TestOmniCostKeepsCacheDimensions(t *testing.T) {
	a := model.OmniAttempt{}
	omniAttemptUsage(&a, map[string]any{"prompt_tokens": 1000, "completion_tokens": 100, "prompt_tokens_details": map[string]any{"cached_tokens": 400, "cache_write_tokens": 100}}, service.OmniRouteCost{Input: 1, Output: 2, CacheRead: .1, CacheWrite: 2})
	if a.InputTokens != 1000 || a.OutputTokens != 100 || a.CostUSD < .0009399 || a.CostUSD > .0009401 {
		t.Fatal(a)
	}
}
