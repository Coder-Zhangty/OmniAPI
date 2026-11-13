package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
)

func omniTestInstallWaitBudget(infoName string, first, last, attempt, total int) service.OmniRoutePolicy {
	p := service.GetOmniRouting()
	channels := map[int]int{}
	for _, id := range p.Models[infoName] {
		channels[id] = first
	}
	p.FirstSeconds = 3
	p.WaitBudgets = map[string]service.OmniRouteWaitBudget{infoName: {FirstProgressSecondsByChannel: channels, LastRouteFirstProgressSeconds: last, AttemptSeconds: attempt, PrecommitSeconds: total}}
	data, _ := json.Marshal(p)
	common.OptionMapRWMutex.Lock()
	common.OptionMap["OmniAPIRouting"] = string(data)
	common.OptionMapRWMutex.Unlock()
	return p
}

func omniTestNoiseStream(w http.ResponseWriter, r *http.Request, frame string) {
	w.Header().Set("Content-Type", "text/event-stream")
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		fmt.Fprint(w, frame+"\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func TestOmniWaitBudgetJSONToolUsesCompletionBudgetInsteadOfFirstStreamProgress(t *testing.T) {
	c, info := omniHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(1500 * time.Millisecond):
		}
		fmt.Fprint(w, `{"model":"test-model","choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"ping","arguments":"{\"message\":\"OK\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":10}}`)
	}, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "supplier payment required", 402) })
	info.IsStream = false
	omniTestInstallWaitBudget(info.OriginModelName, 1, 3, 3, 5)
	resp, err := (&Adaptor{}).omniRequest(c, info, strings.NewReader(`{"model":"test-model","messages":[{"role":"user","content":"Call ping."}],"tools":[{"type":"function","function":{"name":"ping","parameters":{"type":"object"}}}],"tool_choice":"required"}`))
	if err != nil {
		t.Fatal("JSON tool completion was cut off by a stream-first-progress deadline", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil || !strings.Contains(string(data), `"ping"`) {
		t.Fatal(string(data), err)
	}
	if len(model.OmniRequestAttempts(info.RequestId)) != 1 {
		t.Fatal("healthy tool request was needlessly retried")
	}
}

func TestOmniWaitBudgetHTTPNoiseDoesNotHoldPrimary(t *testing.T) {
	c, info := omniHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
		omniTestNoiseStream(w, r, `data: {"choices":[{"delta":{"role":"assistant"}}]}`)
	}, func(w http.ResponseWriter, r *http.Request) { omniSend(w, "backup", "backup answer", true) })
	omniTestInstallWaitBudget(info.OriginModelName, 1, 3, 3, 5)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 4*time.Second)
	defer cancel()
	c.Request = c.Request.WithContext(ctx)
	start := time.Now()
	resp, err := (&Adaptor{}).omniRequest(c, info, strings.NewReader(`{"stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal("role-only noise must fall back before client deadline", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil || !strings.Contains(string(data), "backup answer") || time.Since(start) > 2*time.Second {
		t.Fatal(string(data), err, time.Since(start))
	}
}

func TestOmniWaitBudgetHTTPReasoningCannotExtendAbsoluteAttempt(t *testing.T) {
	c, info := omniHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
		omniTestNoiseStream(w, r, `data: {"choices":[{"delta":{"reasoning_content":"thinking"}}]}`)
	}, func(w http.ResponseWriter, r *http.Request) { omniSend(w, "backup", "backup answer", true) })
	omniTestInstallWaitBudget(info.OriginModelName, 1, 3, 2, 5)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 4*time.Second)
	defer cancel()
	c.Request = c.Request.WithContext(ctx)
	start := time.Now()
	resp, err := (&Adaptor{}).omniRequest(c, info, strings.NewReader(`{"stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal("continuous reasoning must respect absolute attempt deadline", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil || !strings.Contains(string(data), "backup answer") || time.Since(start) > 3*time.Second {
		t.Fatal(string(data), err, time.Since(start))
	}
}

func TestOmniWaitBudgetHTTPSharedPrecommitDeadline(t *testing.T) {
	hang := func(w http.ResponseWriter, r *http.Request) { omniTestNoiseStream(w, r, ": heartbeat") }
	c, info := omniHTTPFixture(t, hang, hang)
	omniTestInstallWaitBudget(info.OriginModelName, 1, 3, 3, 2)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 4*time.Second)
	defer cancel()
	c.Request = c.Request.WithContext(ctx)
	start := time.Now()
	resp, err := (&Adaptor{}).omniRequest(c, info, strings.NewReader(`{"stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatal("attempts must share request deadline", err, time.Since(start))
	}
}

func TestOmniWaitBudgetJSONHeadersAreNotProgress(t *testing.T) {
	c, info := omniHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			return
		case <-time.After(1500 * time.Millisecond):
			fmt.Fprint(w, `{"choices":[{"message":{"content":"wrong primary"}}]}`)
		}
	}, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"backup JSON"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	})
	info.IsStream = false
	omniTestInstallWaitBudget(info.OriginModelName, 1, 3, 3, 5)
	resp, err := (&Adaptor{}).omniRequest(c, info, strings.NewReader(`{"messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil || !strings.Contains(string(data), "backup JSON") {
		t.Fatal(string(data), err)
	}
}

func TestOmniWaitBudgetCommittedContinuationOutlivesPrecommit(t *testing.T) {
	c, info := omniHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
		omniSend(w, "primary", "The beginning is already visible. ", false)
		<-r.Context().Done()
	}, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(1200 * time.Millisecond):
			omniSend(w, "backup", "This is the continued answer.", true)
		}
	})
	omniTestInstallWaitBudget(info.OriginModelName, 1, 3, 3, 1)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	c.Request = c.Request.WithContext(ctx)
	resp, err := (&Adaptor{}).omniRequest(c, info, strings.NewReader(`{"stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil || !strings.Contains(string(data), "beginning is already visible") || !strings.Contains(string(data), "continued answer") {
		t.Fatal("committed plain text continuation was killed by the initial precommit budget", string(data), err)
	}
}

func TestOmniWaitBudgetToolProgressPreservesCompleteTurn(t *testing.T) {
	c, info := omniHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"tool-primary\",\"type\":\"function\",\"function\":{\"name\":\"f\",\"arguments\":\"{\"}}]}}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			return
		case <-time.After(1400 * time.Millisecond):
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"}\"}}]}}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
	}, func(w http.ResponseWriter, r *http.Request) { omniSend(w, "backup", "wrong fallback", true) })
	omniTestInstallWaitBudget(info.OriginModelName, 1, 3, 3, 5)
	resp, err := (&Adaptor{}).omniRequest(c, info, strings.NewReader(`{"stream":true,"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}],"messages":[{"role":"user","content":"call f"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil || !strings.Contains(string(data), "tool-primary") || strings.Contains(string(data), "wrong fallback") {
		t.Fatal("valid partial tool progress lost its complete-turn allowance", string(data), err)
	}
}

func TestOmniWaitBudgetProgressIsAbsoluteAndRequestScoped(t *testing.T) {
	c, info := omniHTTPFixture(t, func(http.ResponseWriter, *http.Request) {}, func(http.ResponseWriter, *http.Request) {})
	p := service.GetOmniRouting()
	ids := p.Models[info.OriginModelName]
	p.WaitBudgets = map[string]service.OmniRouteWaitBudget{info.OriginModelName: {FirstProgressSecondsByChannel: map[int]int{ids[0]: 15, ids[1]: 15}, LastRouteFirstProgressSeconds: 180, AttemptSeconds: 180, PrecommitSeconds: 240}}
	omniInitWaitBudget(c, info, p)
	start := time.Now()
	b := omniAttemptBudgetFor(c, info, map[string]any{}, ids[0], p, start)
	if !b.Enabled() || b.Remaining(start) != 15*time.Second {
		t.Fatal("nonfinal route must receive 15 seconds", b.Remaining(start))
	}
	if !b.ObserveProgress() || b.ObserveProgress() {
		t.Fatal("only first effective progress can change the deadline")
	}
	if got := b.Remaining(start.Add(170 * time.Second)); got != 10*time.Second {
		t.Fatal("progress must preserve absolute 180-second attempt budget", got)
	}
	late := start.Add(220 * time.Second)
	b2 := omniAttemptBudgetFor(c, info, map[string]any{}, ids[1], p, late)
	if got := b2.Remaining(late); got > 20*time.Second || got < 19*time.Second {
		t.Fatal("last route must share remaining precommit request budget", got)
	}
	omniCommitWaitBudget(c)
	if got := b2.Remaining(late); got != 180*time.Second {
		t.Fatal("committed safe continuation must not inherit exhausted initial request deadline", got)
	}
}

func TestOmniWaitBudgetLastEligibleDoesNotAcquireBreakerProbe(t *testing.T) {
	c, info := omniHTTPFixture(t, func(http.ResponseWriter, *http.Request) {}, func(http.ResponseWriter, *http.Request) {})
	p := service.GetOmniRouting()
	ids := p.Models[info.OriginModelName]
	p.WaitBudgets = map[string]service.OmniRouteWaitBudget{info.OriginModelName: {FirstProgressSecondsByChannel: map[int]int{ids[0]: 15, ids[1]: 15}, LastRouteFirstProgressSeconds: 180, AttemptSeconds: 180, PrecommitSeconds: 240}}
	p.FailureThreshold = 1
	p.CooldownSeconds = 1
	key := fmt.Sprintf("%d:%s", ids[1], info.OriginModelName)
	service.OmniCircuitResult(key, false, time.Now().Add(-2*time.Second), p)
	t.Cleanup(func() { service.OmniCircuitResult(key, true, time.Now(), p) })
	omniInitWaitBudget(c, info, p)
	now := time.Now()
	if got := omniAttemptBudgetFor(c, info, map[string]any{}, ids[0], p, now).Remaining(now); got != 15*time.Second {
		t.Fatal("available half-open candidate should count as a later route", got)
	}
	if !service.OmniCircuitAllow(key, time.Now()) {
		t.Fatal("lookahead consumed half-open probe")
	}
	if got := omniAttemptBudgetFor(c, info, map[string]any{}, ids[0], p, now).Remaining(now); got != 180*time.Second {
		t.Fatal("occupied half-open candidate must be skipped when determining final route", got)
	}
	service.OmniCircuitNeutral(key)
	badMapping := "invalid-json"
	model.DB.Model(&model.Channel{}).Where("id = ?", ids[1]).Update("model_mapping", badMapping)
	if got := omniAttemptBudgetFor(c, info, map[string]any{}, ids[0], p, now).Remaining(now); got != 180*time.Second {
		t.Fatal("invalid mapped later route must not shorten last eligible route", got)
	}
	model.DB.Model(&model.Channel{}).Where("id = ?", ids[1]).Update("status", 2)
	if got := omniAttemptBudgetFor(c, info, map[string]any{}, ids[0], p, now).Remaining(now); got != 180*time.Second {
		t.Fatal("disabled later route must not shorten last eligible route", got)
	}
}

func TestOmniWaitBudgetProgressIgnoresTransportNoise(t *testing.T) {
	_, info := omniHTTPFixture(t, func(http.ResponseWriter, *http.Request) {})
	for _, line := range []string{": ping", "event: message", `data: {"choices":[{"delta":{"role":"assistant"}}]}`, `data: {"choices":[{"delta":{"content":"","reasoning_content":""}}]}`, `data: {"choices":[{"delta":{"tool_calls":[{"index":0}]}}]}`} {
		if omniWaitFrameProgress(line, info) {
			t.Fatalf("noise counted as progress: %s", line)
		}
	}
	for _, line := range []string{`data: {"choices":[{"delta":{"content":"a"}}]}`, `data: {"choices":[{"delta":{"reasoning_content":"thinking"}}]}`, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{"}}]}}]}`} {
		if !omniWaitFrameProgress(line, info) {
			t.Fatalf("substantive progress ignored: %s", line)
		}
	}
	info.RequestURLPath = "/v1/responses"
	for _, line := range []string{`data: {"type":"response.created","response":{"id":"r"}}`, `data: {"type":"response.output_item.added","item":{"type":"function_call"}}}`} {
		if omniWaitFrameProgress(line, info) {
			t.Fatalf("Responses metadata counted as progress: %s", line)
		}
	}
	if !omniWaitFrameProgress(`data: {"type":"response.reasoning_summary_text.delta","delta":"thinking"}`, info) {
		t.Fatal("Responses reasoning should count as progress")
	}
}

func TestOmniWaitBudgetUnconfiguredPreservesLegacy(t *testing.T) {
	c, info := omniHTTPFixture(t, func(http.ResponseWriter, *http.Request) {})
	p := service.GetOmniRouting()
	omniInitWaitBudget(c, info, p)
	if omniAttemptBudgetFor(c, info, map[string]any{}, p.Models[info.OriginModelName][0], p, time.Now()).Enabled() {
		t.Fatal("legacy model unexpectedly opted into new budget")
	}
}
