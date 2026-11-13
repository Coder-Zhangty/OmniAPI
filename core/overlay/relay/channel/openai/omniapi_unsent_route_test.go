package openai

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
)

func TestOmniForwardsDeclaredLimitsAndRetriesSupplierLimit(t *testing.T) {
	const output = 1_000_000
	type attempt struct {
		supplier string
		tokens   int
	}
	sent := make(chan attempt, 2)
	record := func(supplier string, r *http.Request) {
		var body struct {
			MaxTokens int `json:"max_tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sent <- attempt{supplier, body.MaxTokens}
	}
	c, info := omniHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
		record("primary", r)
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"error":{"code":"context_length_exceeded"}}`)
	}, func(w http.ResponseWriter, r *http.Request) {
		record("backup", r)
		omniSend(w, "backup", "backup answer", true)
	})
	ids := service.GetOmniRouting().Models[info.OriginModelName]
	path := "/v1/chat/completions"
	limits := model.OmniRequestLimits{MaxInputTokens: 10, MaxOutputTokens: 96000, Basis: "accepted_parameters",
		EvidenceHash: strings.Repeat("a", 64), VerifiedAt: time.Now().Unix()}
	bindings := []model.OmniModelBinding{
		{ChannelID: ids[0], Status: "verified", UpstreamModel: "test-model", Protocols: []string{"openai"}, ContextLength: 10,
			RequestLimits: map[string]model.OmniRequestLimits{path: limits}},
		{ChannelID: ids[1], Status: "verified", UpstreamModel: "test-model", Protocols: []string{"openai"}},
	}
	registry := model.OmniModelRegistry{Enabled: true, Version: info.RequestId,
		Models: map[string]model.OmniModelIdentity{info.OriginModelName: {Version: "test", Capability: "text", Bindings: bindings}}}
	encoded, _ := json.Marshal(registry)
	previous := common.OptionMap["OmniAPIModelRegistry"]
	common.OptionMap["OmniAPIModelRegistry"] = string(encoded)
	t.Cleanup(func() { common.OptionMap["OmniAPIModelRegistry"] = previous })
	info.OmniRegistryVersion, info.RequestURLPath = registry.Version, path
	declared := uint(output)
	info.Request = &dto.GeneralOpenAIRequest{MaxTokens: &declared}
	info.SetEstimatePromptTokens(120_000)
	resp, err := (&Adaptor{}).omniRequest(c, info, strings.NewReader(`{"max_tokens":1000000,"stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || !strings.Contains(string(body), "backup answer") {
		t.Fatalf("fallback failed: %s %v", body, err)
	}
	if len(sent) != 2 {
		t.Fatalf("got %d upstream sends, want 2", len(sent))
	}
	for _, supplier := range []string{"primary", "backup"} {
		if got := <-sent; got != (attempt{supplier, output}) {
			t.Fatalf("unexpected upstream send: %+v", got)
		}
	}
}

func TestOmniSupplierLimitClassificationDoesNotDependOnVendorErrorType(t *testing.T) {
	for _, test := range []struct {
		body   string
		status int
		limit  bool
	}{
		{`{"error":{"type":"<nil>","message":"max_tokens must be <= 128000, got 1000000"}}`, 400, true},
		{`{"error":{"type":"invalid_request_error","message":"max_tokens: maximum is 128000"}}`, 422, true},
		{`{"error":{"type":"invalid_request_error","message":"prompt is too long"}}`, 400, true},
		{`{"error":{"type":"<nil>","message":"prompt is too long: 1005853 tokens > 1000000 maximum"}}`, 400, true},
		{`{"error":{"code":"INVALID_ARGUMENT","message":"max_tokens must be an integer"}}`, 400, false},
		{`{"error":{"message":"invalid credential"}}`, 401, false},
	} {
		err := omniReadHTTPError(&http.Response{StatusCode: test.status, Body: io.NopCloser(strings.NewReader(test.body))})
		if omniSupplierRequestLimited(err) != test.limit {
			t.Fatalf("incorrect supplier limit classification: %v", err)
		}
		if test.limit && !omniRetryable(err) {
			t.Fatal("supplier request limit must try the next route")
		}
	}
}

func TestOmniSupplierSignedHistoryErrorAllowsOriginalRouteFallback(t *testing.T) {
	for _, tc := range []struct {
		message string
		retry   bool
	}{
		{"messages.2.content.0: Invalid `signature` in `thinking` block (private upstream data)", true},
		{"thinking block prefix mismatch", true},
		{"invalid tool arguments", false},
	} {
		body, _ := json.Marshal(map[string]any{"error": map[string]any{"code": "INVALID_REQUEST", "message": tc.message}})
		upstream := omniReadHTTPError(&http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(string(body)))})
		if omniSupplierHistoryRejected(upstream) != tc.retry || omniRetryable(upstream) != tc.retry {
			t.Fatalf("unexpected fallback decision for %s: %v", tc.message, upstream)
		}
		if strings.Contains(upstream.Error(), "private upstream data") {
			t.Fatal("upstream error details leaked into route logs")
		}
	}
}

func TestOmniNativeMessagesForwardsDeclaredOutputWithoutClamping(t *testing.T) {
	const output = 1_000_000
	received := make(chan uint, 1)
	c, info := omniHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var request dto.ClaudeRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		if r.URL.Path != "/v1/messages" || request.MaxTokens == nil {
			t.Error("expected native Messages and an explicit output parameter")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		received <- *request.MaxTokens
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"type":"message","role":"assistant","id":"msg_ok","model":"test-model","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn","usage":{"input_tokens":20,"output_tokens":4}}`)
	})
	id := service.GetOmniRouting().Models[info.OriginModelName][0]
	if err := model.DB.Model(&model.Channel{}).Where("id = ?", id).Update("type", 14).Error; err != nil {
		t.Fatal(err)
	}
	path := "/v1/messages"
	registry := model.OmniModelRegistry{Enabled: true, Version: info.RequestId,
		Models: map[string]model.OmniModelIdentity{info.OriginModelName: {Version: "test", Capability: "text", Bindings: []model.OmniModelBinding{{
			ChannelID: id, Status: "verified", UpstreamModel: "test-model", Protocols: []string{"claude"},
			RequestLimits: map[string]model.OmniRequestLimits{path: {
				MaxInputTokens: 128000, MaxOutputTokens: 96000, MaxContextTokens: 224000, MaxRequestBytes: 64,
				Basis: "accepted_parameters", EvidenceHash: strings.Repeat("a", 64), VerifiedAt: time.Now().Unix(),
			}},
		}}}}}
	encoded, _ := json.Marshal(registry)
	previous := common.OptionMap["OmniAPIModelRegistry"]
	common.OptionMap["OmniAPIModelRegistry"] = string(encoded)
	t.Cleanup(func() { common.OptionMap["OmniAPIModelRegistry"] = previous })
	info.OmniRegistryVersion, info.RequestURLPath = registry.Version, path
	c.Request.URL.Path = path
	info.OmniNativeClaude, info.IsStream = service.OmniNativeClaudeFor(info), false
	if !info.OmniNativeClaude {
		t.Fatal("verified native binding was ignored because the rollout map was absent")
	}
	info.SetEstimatePromptTokens(130_000)
	var request dto.ClaudeRequest
	if err := json.Unmarshal([]byte(`{"model":"test-model","max_tokens":1000000,"messages":[{"role":"user","content":"reply with OK"}]}`), &request); err != nil {
		t.Fatal(err)
	}
	info.Request = &request
	resp, err := OmniNativeClaudeRequest(c, info)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	select {
	case n := <-received:
		if n != output {
			t.Fatalf("upstream received max_tokens=%d, want %d", n, output)
		}
	default:
		t.Fatal("the native upstream received no request")
	}
}

func TestOmniUnsentStreamRoutesHaveZeroProcurementAndRealHTTPFailureKeepsEstimate(t *testing.T) {
	for _, mode := range []string{"disabled", "missing", "group", "model", "unverified", "mapping", "http_failure"} {
		t.Run(mode, func(t *testing.T) {
			var primaryCalls, backupCalls atomic.Int32
			c, info := omniHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
				primaryCalls.Add(1)
				http.Error(w, "synthetic upstream failure", http.StatusServiceUnavailable)
			}, func(w http.ResponseWriter, r *http.Request) {
				backupCalls.Add(1)
				omniSend(w, "backup", "backup answer", true)
			})
			policy := service.GetOmniRouting()
			ids := policy.Models[info.OriginModelName]
			policy.Costs = map[string]map[int]service.OmniRouteCost{info.OriginModelName: {ids[0]: {Input: 10, Output: 20}, ids[1]: {Input: 1, Output: 2}}}
			data, _ := json.Marshal(policy)
			common.OptionMap["OmniAPIRouting"] = string(data)
			switch mode {
			case "disabled":
				if err := model.DB.Model(&model.Channel{}).Where("id = ?", ids[0]).Update("status", 2).Error; err != nil {
					t.Fatal(err)
				}
			case "missing":
				if err := model.DB.Delete(&model.Channel{}, ids[0]).Error; err != nil {
					t.Fatal(err)
				}
			case "group", "model":
				column := "models"
				if mode == "group" {
					column = "group"
				}
				if err := model.DB.Model(&model.Channel{}).Where("id = ?", ids[0]).Update(column, "incompatible").Error; err != nil {
					t.Fatal(err)
				}
			case "mapping":
				if err := model.DB.Model(&model.Channel{}).Where("id = ?", ids[0]).Update("model_mapping", "invalid JSON").Error; err != nil {
					t.Fatal(err)
				}
			case "unverified":
				registry := model.OmniModelRegistry{Enabled: true, Version: info.RequestId, Models: map[string]model.OmniModelIdentity{info.OriginModelName: {Version: "test", Capability: "text", Bindings: []model.OmniModelBinding{{ChannelID: ids[0], UpstreamModel: "test-model", Status: "pending"}, {ChannelID: ids[1], UpstreamModel: "test-model", Status: "verified", Protocols: []string{"openai"}}}}}}
				encoded, _ := json.Marshal(registry)
				previous := common.OptionMap["OmniAPIModelRegistry"]
				common.OptionMap["OmniAPIModelRegistry"] = string(encoded)
				info.OmniRegistryVersion = registry.Version
				t.Cleanup(func() { common.OptionMap["OmniAPIModelRegistry"] = previous })
			}
			resp, err := (&Adaptor{}).omniRequest(c, info, strings.NewReader(`{"stream":true,"messages":[{"role":"user","content":"hello"}]}`))
			if err != nil {
				t.Fatal(err)
			}
			output, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil || !strings.Contains(string(output), "backup answer") {
				t.Fatalf("fallback failed: %s %v", output, err)
			}
			if backupCalls.Load() != 1 {
				t.Fatal("backup called more than once")
			}
			attempts := model.OmniRequestAttempts(info.RequestId)
			if len(attempts) != 2 {
				t.Fatalf("missing candidate evidence: %+v", attempts)
			}
			first := attempts[0]
			if mode == "http_failure" {
				if primaryCalls.Load() != 1 || first.CostUSD <= 0 || !first.CostEstimated || first.Outcome != "before_output_failed" {
					t.Fatalf("real HTTP failure lost its estimate: %+v", first)
				}
			} else {
				if primaryCalls.Load() != 0 || first.CostUSD != 0 || first.InputTokens != 0 || first.OutputTokens != 0 || first.CostEstimated || first.Outcome != "skipped_before_send" {
					t.Fatalf("unsent candidate incorrectly charged procurement: %+v", first)
				}
			}
		})
	}
}
