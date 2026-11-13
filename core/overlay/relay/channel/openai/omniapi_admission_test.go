package openai

import (
	"encoding/json"
	"errors"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOmniTextAdmissionLeavesContextLimitsToUpstream(t *testing.T) {
	for _, mode := range []string{"sse", "json", "responses"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			c, info := omniHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch mode {
				case "json":
					w.Header().Set("Content-Type", "application/json")
					io.WriteString(w, `{"id":"mock","choices":[{"message":{"role":"assistant","content":"accepted"},"finish_reason":"stop"}]}`)
				case "responses":
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"accepted\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{}}\n\n")
				default:
					omniSend(w, "ok", "accepted", true)
				}
			})
			ids := service.GetOmniRouting().Models[info.OriginModelName]
			registry := model.OmniModelRegistry{Enabled: true, Version: info.RequestId, Models: map[string]model.OmniModelIdentity{"test-model": {Version: "test", Capability: "text", Bindings: []model.OmniModelBinding{{ChannelID: ids[0], Status: "verified", UpstreamModel: "test-model", Protocols: []string{"openai"}, ContextLength: 8192}}}}}
			encoded, _ := json.Marshal(registry)
			old := common.OptionMap["OmniAPIModelRegistry"]
			common.OptionMap["OmniAPIModelRegistry"] = string(encoded)
			t.Cleanup(func() { common.OptionMap["OmniAPIModelRegistry"] = old })
			info.OmniRegistryVersion = registry.Version
			info.SetEstimatePromptTokens(9000)
			raw := `{"stream":true,"messages":[{"role":"user","content":"synthetic"}]}`
			if mode == "json" {
				info.IsStream = false
			}
			if mode == "responses" {
				info.RequestURLPath = "/v1/responses"
				c.Request.URL.Path = "/v1/responses"
				raw = `{"stream":true,"input":"synthetic"}`
			}
			response, err := (&Adaptor{}).omniRequest(c, info, strings.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			output, err := io.ReadAll(response.Body)
			if err != nil || !strings.Contains(string(output), "accepted") || calls.Load() != 1 {
				t.Fatalf("request did not reach upstream exactly once: %d %v", calls.Load(), err)
			}
		})
	}
}

func TestOmniAdmissionModelDeadlineOverride(t *testing.T) {
	c, info := omniHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		time.Sleep(1400 * time.Millisecond)
		omniSend(w, "late", "accepted", true)
	})
	p := service.GetOmniRouting()
	data, _ := json.Marshal(p)
	var raw map[string]any
	json.Unmarshal(data, &raw)
	raw["first_seconds_by_model"] = map[string]int{"test-model": 3}
	data, _ = json.Marshal(raw)
	common.OptionMap["OmniAPIRouting"] = string(data)
	resp, err := (&Adaptor{}).omniRequest(c, info, strings.NewReader(`{"stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal("model-specific initial deadline ignored:", err)
	}
	defer resp.Body.Close()
	output, err := io.ReadAll(resp.Body)
	if err != nil || !strings.Contains(string(output), "accepted") {
		t.Fatal(string(output), err)
	}
}

func TestOmniAdmissionDeadlineBoundsAndDefault(t *testing.T) {
	p := service.OmniRoutePolicy{FirstSeconds: 30, FirstSecondsByModel: map[string]int{"pro": 180, "invalid": 181, "negative": -1}}
	for name, want := range map[string]int{"pro": 180, "other": 30, "invalid": 30, "negative": 30} {
		if got := p.FirstSecondsFor(name); got != want {
			t.Fatalf("%s deadline %d, want %d", name, got, want)
		}
	}
}

func TestOmniAdmissionJSONDeadlineOverride(t *testing.T) {
	c, info := omniHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1400 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"mock","choices":[{"message":{"role":"assistant","content":"accepted"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	})
	info.IsStream = false
	p := service.GetOmniRouting()
	p.FirstSecondsByModel = map[string]int{"test-model": 3}
	data, _ := json.Marshal(p)
	common.OptionMap["OmniAPIRouting"] = string(data)
	resp, err := (&Adaptor{}).omniRequest(c, info, strings.NewReader(`{"messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	output, err := io.ReadAll(resp.Body)
	if err != nil || !strings.Contains(string(output), "accepted") {
		t.Fatal(string(output), err)
	}
}

func TestOmniAdmissionCapabilityFallbackAndRealFailureStatus(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "fallback", true: "upstream_failure"}[fail], func(t *testing.T) {
			var first, second atomic.Int32
			c, info := omniHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) { first.Add(1); http.Error(w, "unavailable", 503) }, func(w http.ResponseWriter, r *http.Request) { second.Add(1); omniSend(w, "ok", "accepted", true) })
			ids := service.GetOmniRouting().Models[info.OriginModelName]
			bindings := []model.OmniModelBinding{{ChannelID: ids[0], Status: "verified", UpstreamModel: "test-model", Protocols: []string{"openai"}}, {ChannelID: ids[1], Status: "verified", UpstreamModel: "test-model", Protocols: []string{"openai"}, Supports: []string{"vision"}}}
			if fail {
				bindings[0].Supports, bindings[1].Supports = bindings[1].Supports, nil
			}
			registry := model.OmniModelRegistry{Enabled: true, Version: info.RequestId, Models: map[string]model.OmniModelIdentity{"test-model": {Version: "test", Capability: "text", Bindings: bindings}}}
			encoded, _ := json.Marshal(registry)
			old := common.OptionMap["OmniAPIModelRegistry"]
			common.OptionMap["OmniAPIModelRegistry"] = string(encoded)
			t.Cleanup(func() { common.OptionMap["OmniAPIModelRegistry"] = old })
			info.OmniRegistryVersion = registry.Version
			resp, err := (&Adaptor{}).omniRequest(c, info, strings.NewReader(`{"stream":true,"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,c3ludGhldGlj"}}]}]}`))
			if fail {
				var api *types.NewAPIError
				if !errors.As(err, &api) || api.StatusCode == 400 || first.Load() != 1 || second.Load() != 0 {
					t.Fatalf("upstream failure mislabeled as admission rejection: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			output, err := io.ReadAll(resp.Body)
			if err != nil || !strings.Contains(string(output), "accepted") || first.Load() != 0 || second.Load() != 1 {
				t.Fatal(string(output), err)
			}
		})
	}
}

func TestOmniAdmissionMissingVisionIsSpecific(t *testing.T) {
	c, info := omniHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) { t.Error("missing capability reached transport") })
	ids := service.GetOmniRouting().Models[info.OriginModelName]
	registry := model.OmniModelRegistry{Enabled: true, Version: info.RequestId, Models: map[string]model.OmniModelIdentity{"test-model": {Version: "test", Capability: "text", Bindings: []model.OmniModelBinding{{ChannelID: ids[0], Status: "verified", UpstreamModel: "test-model", Protocols: []string{"openai"}}}}}}
	encoded, _ := json.Marshal(registry)
	old := common.OptionMap["OmniAPIModelRegistry"]
	common.OptionMap["OmniAPIModelRegistry"] = string(encoded)
	t.Cleanup(func() { common.OptionMap["OmniAPIModelRegistry"] = old })
	info.OmniRegistryVersion = registry.Version
	_, err := (&Adaptor{}).omniRequest(c, info, strings.NewReader(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,c3ludGhldGlj"}}]}]}`))
	var api *types.NewAPIError
	if !errors.As(err, &api) || api.StatusCode != 400 || api.GetErrorCode() != types.ErrorCode("capability_not_verified") || !strings.Contains(err.Error(), "vision") {
		t.Fatalf("missing precise capability error: %v", err)
	}
}

func TestOmniAdmissionRiskReportRequiresVerifiedStructure(t *testing.T) {
	raw := map[string]any{"tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "submit_risk_report", "strict": true}}}}
	caps := omniRequiredCapabilities(raw)
	found := false
	for _, cap := range caps {
		if cap == "risk_report" {
			found = true
		}
	}
	if !found {
		t.Fatal("fixed risk report must require verified structured and tool capabilities")
	}
}
