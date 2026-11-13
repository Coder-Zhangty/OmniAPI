package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
)

func TestOmniResponsesFailoverReservesThirdRoute(t *testing.T) {
	thinking := func(w http.ResponseWriter, r *http.Request) {
		omniTestNoiseStream(w, r, `data: {"type":"response.reasoning_summary_text.delta","delta":"thinking"}`)
	}
	c, info := omniHTTPFixture(t, thinking, thinking, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"third route\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"third\",\"status\":\"completed\"}}\n\n")
	})
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	info.RequestURLPath = c.Request.URL.Path
	info.RelayFormat = types.RelayFormatOpenAIResponses
	p := omniTestInstallWaitBudget(info.OriginModelName, 1, 1, 3, 5)
	ids := p.Models[info.OriginModelName]
	installResponsesFailoverTestPolicy(t, p, info.OriginModelName, 1, 2)

	resp, err := (&Adaptor{}).omniRequest(c, info, strings.NewReader(`{"model":"test-model","stream":true,"input":"hello"}`))
	if err != nil {
		t.Fatalf("third eligible route was not sent: %v; attempts: %+v", err, model.OmniRequestAttempts(info.RequestId))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || !strings.Contains(string(body), "third route") || !strings.Contains(string(body), "response.completed") {
		t.Fatalf("third route did not finish: %s (%v)", body, err)
	}
	attempts := model.OmniRequestAttempts(info.RequestId)
	if len(attempts) != 3 || attempts[2].ChannelID != ids[2] || attempts[2].Outcome != "completed" {
		t.Fatalf("unexpected route attempts: %+v", attempts)
	}
	decisions := service.OmniRouteDecisions(c)
	if len(decisions) != 3 || decisions[0].Reason != "precommit_backup_limit" || decisions[1].Reason != "precommit_backup_limit" {
		t.Fatalf("incorrect timeout reasons: %+v", decisions)
	}
	for _, id := range ids[:2] {
		if !service.OmniCircuitAvailable(fmt.Sprintf("%d:%s", id, info.OriginModelName), time.Now()) {
			t.Fatalf("policy cutover opened the circuit for channel %d", id)
		}
	}
}

func installResponsesFailoverTestPolicy(t *testing.T, p service.OmniRoutePolicy, modelName string, reserve, cap int) service.OmniRoutePolicy {
	t.Helper()
	budget := p.WaitBudgets[modelName]
	budget.ResponsesFailover = &service.OmniResponsesFailoverBudget{
		ReserveSeconds: reserve,
		Channels:       map[int]service.OmniResponsesChannelBudget{},
	}
	ids := p.Models[modelName]
	for _, id := range ids[:len(ids)-1] {
		budget.ResponsesFailover.Channels[id] = service.OmniResponsesChannelBudget{FirstProgressSeconds: 1, AttemptSeconds: cap}
	}
	p.WaitBudgets[modelName] = budget
	p.FailureThreshold = 1
	p.CooldownSeconds = 60
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	override, err := json.Marshal(map[string]*service.OmniResponsesFailoverBudget{modelName: budget.ResponsesFailover})
	if err != nil {
		t.Fatal(err)
	}
	common.OptionMapRWMutex.Lock()
	oldOverride := common.OptionMap[service.OmniResponsesFailoverOption]
	common.OptionMap["OmniAPIRouting"] = string(data)
	common.OptionMap[service.OmniResponsesFailoverOption] = string(override)
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap[service.OmniResponsesFailoverOption] = oldOverride
		common.OptionMapRWMutex.Unlock()
	})
	return p
}

func TestOmniResponsesFailoverSkipsCircuitAndCountsNoProgress(t *testing.T) {
	var middleCalls atomic.Int32
	c, info := omniHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
		omniTestNoiseStream(w, r, ": heartbeat")
	}, func(w http.ResponseWriter, r *http.Request) {
		middleCalls.Add(1)
		http.Error(w, "unexpected", 503)
	}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
	})
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	info.RequestURLPath = c.Request.URL.Path
	info.RelayFormat = types.RelayFormatOpenAIResponses
	p := installResponsesFailoverTestPolicy(t, omniTestInstallWaitBudget(info.OriginModelName, 3, 1, 4, 8), info.OriginModelName, 1, 3)
	ids := p.Models[info.OriginModelName]
	key := func(id int) string { return fmt.Sprintf("%d:%s", id, info.OriginModelName) }
	service.OmniCircuitResult(key(ids[1]), false, time.Now(), p)
	t.Cleanup(func() {
		for _, id := range ids {
			service.OmniCircuitResult(key(id), true, time.Now(), p)
		}
	})
	resp, err := (&Adaptor{}).omniRequest(c, info, strings.NewReader(`{"stream":true,"input":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatal(err)
	}
	decisions := service.OmniRouteDecisions(c)
	if len(decisions) != 3 || decisions[0].Reason != "first_progress_timeout" || decisions[1].Reason != "circuit_open" || middleCalls.Load() != 0 {
		t.Fatalf("unexpected route decisions: %+v", decisions)
	}
	if service.OmniCircuitAvailable(key(ids[0]), time.Now()) {
		t.Fatal("genuine no-progress timeout must count toward the circuit")
	}
}

func TestOmniResponsesFailoverCancellationStopsRetries(t *testing.T) {
	primaryStarted := make(chan struct{})
	var backupCalls atomic.Int32
	c, info := omniHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(primaryStarted)
		<-r.Context().Done()
	}, func(w http.ResponseWriter, r *http.Request) {
		backupCalls.Add(1)
		http.Error(w, "unexpected", 503)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil).WithContext(ctx)
	info.RequestURLPath = c.Request.URL.Path
	info.RelayFormat = types.RelayFormatOpenAIResponses
	p := installResponsesFailoverTestPolicy(t, omniTestInstallWaitBudget(info.OriginModelName, 1, 1, 3, 5), info.OriginModelName, 1, 2)
	go func() {
		select {
		case <-primaryStarted:
			cancel()
		case <-ctx.Done():
		}
	}()
	resp, err := (&Adaptor{}).omniRequest(c, info, strings.NewReader(`{"stream":true,"input":"hello"}`))
	if resp != nil {
		resp.Body.Close()
	}
	if !errors.Is(err, context.Canceled) || backupCalls.Load() != 0 {
		t.Fatalf("cancellation did not stop retries: %v, backup calls %d", err, backupCalls.Load())
	}
	decisions := service.OmniRouteDecisions(c)
	if len(decisions) != 1 || decisions[0].Reason != "client_cancelled" ||
		!service.OmniCircuitAvailable(fmt.Sprintf("%d:%s", p.Models[info.OriginModelName][0], info.OriginModelName), time.Now()) {
		t.Fatalf("unexpected cancellation decisions: %+v", decisions)
	}
}

func TestOmniResponsesFailoverBudgetAndScope(t *testing.T) {
	c, info := omniHTTPFixture(t, func(http.ResponseWriter, *http.Request) {}, func(http.ResponseWriter, *http.Request) {}, func(http.ResponseWriter, *http.Request) {})
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	info.RequestURLPath = c.Request.URL.Path
	p := installResponsesFailoverTestPolicy(t, omniTestInstallWaitBudget(info.OriginModelName, 2, 2, 6, 10), info.OriginModelName, 2, 6)
	ids := p.Models[info.OriginModelName]
	start := time.Now()
	omniInitWaitBudget(c, info, p)
	state := c.Request.Context().Value(omniWaitBudgetKey{}).(*omniRequestWaitBudget)
	state.deadline = start.Add(10 * time.Second)
	primary := omniAttemptBudgetFor(c, info, map[string]any{}, ids[0], p, start)
	primary.ObserveProgress()
	if primary.Remaining(start) != 6*time.Second || !errors.Is(primary.timeoutError(context.Background(), ""), errOmniBackupBudget) {
		t.Fatal("primary must leave two 2-second reservations")
	}
	late := start.Add(6 * time.Second)
	middle := omniAttemptBudgetFor(c, info, map[string]any{}, ids[1], p, late)
	middle.ObserveProgress()
	if middle.Remaining(late) != 2*time.Second {
		t.Fatal("middle must retain the last route's reservation", middle.Remaining(late))
	}
	last := omniAttemptBudgetFor(c, info, map[string]any{}, ids[2], p, start)
	if last.Remaining(start) != 2*time.Second {
		t.Fatal("last eligible route must retain its no-progress allowance")
	}
	last.ObserveProgress()
	if last.Remaining(start) != 6*time.Second || !last.backupDeadline.IsZero() {
		t.Fatal("last eligible route must retain the full completion allowance")
	}
	last = omniAttemptBudgetFor(c, info, map[string]any{}, ids[2], p, start.Add(9*time.Second))
	last.ObserveProgress()
	if last.Remaining(start.Add(9*time.Second)) != time.Second ||
		!errors.Is(last.timeoutError(context.Background(), ""), errOmniRequestBudget) {
		t.Fatal("last route must still respect the shared request deadline")
	}
	for _, raw := range []map[string]any{{"previous_response_id": "stateful"}, {"conversation": "stateful"}} {
		if omniAttemptBudgetFor(c, info, raw, ids[0], p, start).responsesFailover {
			t.Fatal("stateful requests must retain their existing deadline policy")
		}
	}
	info.IsStream = false
	if omniAttemptBudgetFor(c, info, map[string]any{}, ids[0], p, start).responsesFailover {
		t.Fatal("non-streaming requests must retain their existing deadline policy")
	}
	info.IsStream = true
	info.RequestURLPath = "/v1/chat/completions"
	if omniAttemptBudgetFor(c, info, map[string]any{}, ids[0], p, start).responsesFailover {
		t.Fatal("Chat requests must retain their existing deadline policy")
	}
	info.RequestURLPath = "/v1/responses"
	info.OmniPoolID = "frozen-shared-pool"
	info.OmniSharedDispatchBounds = nil
	solo := omniAttemptBudgetFor(c, info, map[string]any{}, ids[0], p, start)
	solo.ObserveProgress()
	if !solo.backupDeadline.IsZero() || solo.Remaining(start) != 6*time.Second {
		t.Fatal("unavailable shared routes must not consume reservation time")
	}
}

func TestOmniResponsesFailoverLongToolPayloadPreserved(t *testing.T) {
	padding := strings.Repeat("synthetic-context-", 2800)
	var received atomic.Int32
	var cancelled atomic.Int32
	thinking := func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request["input"] != padding {
			t.Error("long synthetic context was altered before failover", err)
		}
		received.Add(1)
		omniTestNoiseStream(w, r, `data: {"type":"response.function_call_arguments.delta","delta":"{"}`)
		cancelled.Add(1)
	}
	c, info := omniHTTPFixture(t, thinking, thinking, func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request["input"] != padding {
			t.Error("third route did not receive the original synthetic context", err)
		}
		received.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"final\",\"status\":\"completed\",\"output\":[{\"type\":\"function_call\",\"name\":\"ping\",\"call_id\":\"complete-call\",\"arguments\":\"{\\\"message\\\":\\\"OK\\\"}\"}]}}\n\n")
	})
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	info.RequestURLPath = c.Request.URL.Path
	info.RelayFormat = types.RelayFormatOpenAIResponses
	installResponsesFailoverTestPolicy(t, omniTestInstallWaitBudget(info.OriginModelName, 1, 1, 3, 5), info.OriginModelName, 1, 2)
	payload, err := json.Marshal(map[string]any{
		"stream": true, "input": padding, "tool_choice": "required",
		"tools": []any{map[string]any{"type": "function", "name": "ping", "parameters": map[string]any{"type": "object"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&Adaptor{}).omniRequest(c, info, strings.NewReader(string(payload)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || strings.Contains(string(body), "arguments.delta") || !strings.Contains(string(body), "complete-call") ||
		received.Load() != 3 {
		t.Fatalf("buffered tool failover corrupted the delivered turn: %s (%v), sent %d", body, err, received.Load())
	}
	until := time.Now().Add(time.Second)
	for cancelled.Load() < 2 && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	if cancelled.Load() != 2 {
		t.Fatal("both abandoned suppliers must observe cancellation")
	}
}
