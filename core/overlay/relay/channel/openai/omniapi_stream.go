package openai

// This wrapper only handles stateless OpenAI chat SSE. It keeps the native
// adaptor, credentials, header overrides, proxy and billing session intact.
import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

type omniFrame struct {
	line string
	err  error
}
type omniStream struct {
	resp        *http.Response
	frames      <-chan omniFrame
	cancel      context.CancelFunc
	channelID   int
	started     time.Time
	prefix      []string
	audit       model.OmniAttempt
	meta        *relaycommon.ChannelMeta
	firstMillis int64
}

func omniPlainRequest(raw map[string]any) bool {
	for _, key := range []string{"tools", "functions", "response_format", "audio", "modalities", "previous_response_id"} {
		if v, ok := raw[key]; ok && v != nil {
			return false
		}
	}
	if raw["store"] == true {
		return false
	}
	if n, ok := raw["n"].(float64); ok && n != 1 {
		return false
	}
	msgs, ok := raw["messages"].([]any)
	if !ok {
		return false
	}
	for _, v := range msgs {
		m, ok := v.(map[string]any)
		if !ok {
			return false
		}
		if _, ok = m["content"].(string); !ok {
			return false
		}
		if m["role"] == "tool" || m["function_call"] != nil || m["tool_calls"] != nil {
			return false
		}
	}
	return true
}
func omniParse(line string) (map[string]any, string, bool, bool) {
	if !strings.HasPrefix(line, "data:") {
		return nil, "", false, false
	}
	data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if data == "[DONE]" {
		return nil, "", true, true
	}
	var frame map[string]any
	if json.Unmarshal([]byte(data), &frame) != nil {
		return nil, "", false, false
	}
	safe := frame["error"] == nil
	text := ""
	effective := false
	if choices, ok := frame["choices"].([]any); ok {
		for _, v := range choices {
			ch, ok := v.(map[string]any)
			if !ok {
				continue
			}
			d, _ := ch["delta"].(map[string]any)
			if s, ok := d["content"].(string); ok {
				text += s
				effective = effective || s != ""
			}
			for _, key := range []string{"tool_calls", "function_call", "reasoning_content", "reasoning"} {
				if value := d[key]; value != nil {
					safe = false
					encoded, _ := json.Marshal(value)
					effective = effective || (string(encoded) != "\"\"" && string(encoded) != "[]" && string(encoded) != "{}" && string(encoded) != "null")
				}
			}
			if ch["finish_reason"] != nil {
				effective = true
			}
		}
	}
	return frame, text, effective, safe
}
func omniOverlap(previous, next string) string {
	a, b := []rune(previous), []rune(next)
	limit := min(len(a), len(b), 2048)
	for n := limit; n >= 8; n-- {
		if string(a[len(a)-n:]) == string(b[:n]) {
			return string(b[n:])
		}
	}
	return next
}

// These failures are proven to precede the native transport call. Never infer
// zero procurement cost from a timeout or HTTP failure after transport started.
type omniUnsentRouteError struct{ error }

func (e *omniUnsentRouteError) Unwrap() error { return e.error }

func omniOpen(a *Adaptor, c *gin.Context, info *relaycommon.RelayInfo, raw map[string]any, id int, p service.OmniRoutePolicy) (*omniStream, error) {
	if !service.OmniSharedChannelAllowed(info, id) {
		return nil, &omniUnsentRouteError{errors.New("route not in frozen shared capability set")}
	}
	ch, err := model.CacheGetChannel(id)
	if err != nil {
		return nil, &omniUnsentRouteError{err}
	}
	if ch.Status != 1 || (ch.Type != 1 && ch.Type != 14) {
		return nil, &omniUnsentRouteError{errors.New("route is not enabled OpenAI channel")}
	}
	if !strings.Contains(","+ch.Models+",", ","+info.OriginModelName+",") || !strings.Contains(","+ch.Group+",", ","+info.UsingGroup+",") {
		return nil, &omniUnsentRouteError{errors.New("route capability mismatch")}
	}
	if rejection := omniCandidateReject(info, id, ch.Type, raw); rejection != nil {
		return nil, &omniUnsentRouteError{rejection}
	}
	ctx, cancel := context.WithCancel(c.Request.Context())
	child := c.Copy()
	child.Request = c.Request.Clone(ctx)
	child.Writer = c.Writer
	clone := *info
	clone.ChannelMeta = nil
	clone.DisablePing = true
	if apiErr := middleware.SetupContextForSelectedChannel(child, ch, info.OriginModelName); apiErr != nil {
		cancel()
		return nil, &omniUnsentRouteError{errors.New("route setup failed")}
	}
	clone.InitChannelMeta(child)
	name, err := omniChannelModel(ch.ModelMapping, info.OriginModelName, info.OmniRegistryVersion, id)
	if err != nil {
		cancel()
		return nil, &omniUnsentRouteError{err}
	}
	clone.UpstreamModelName = name
	body := map[string]any{}
	for k, v := range raw {
		body[k] = v
	}
	body["model"] = name
	start := time.Now()
	budget := omniAttemptBudgetFor(c, info, raw, id, p, start)
	if budget.Enabled() && budget.Remaining(time.Now()) <= 0 {
		cancel()
		return nil, &omniUnsentRouteError{budget.timeoutError(c.Request.Context(), "precommit request budget exhausted")}
	}
	result := make(chan omniFrame, 1)
	responses := make(chan *http.Response)
	go func() {
		resp, e := omniNativeRequest(child, &clone, body, ch.Type)
		if e != nil {
			result <- omniFrame{err: e}
			return
		}
		if ctx.Err() != nil {
			resp.Body.Close()
			return
		}
		if resp.StatusCode != 200 {
			err := omniReadHTTPError(resp)
			resp.Body.Close()
			result <- omniFrame{err: err}
			return
		}
		select {
		case responses <- resp:
		case <-ctx.Done():
			resp.Body.Close()
		}
	}()
	initialWait := time.Duration(p.FirstSecondsFor(info.OriginModelName)) * time.Second
	if budget.Enabled() {
		initialWait = budget.Remaining(start)
	}
	timer := time.NewTimer(initialWait)
	defer timer.Stop()
	var resp *http.Response
	select {
	case resp = <-responses:
	case r := <-result:
		cancel()
		if service.OmniTrialBackupError(r.err) {
			return nil, &omniUnsentRouteError{r.err}
		}
		return nil, r.err
	case <-timer.C:
		cancel()
		return nil, budget.timeoutError(c.Request.Context(), "first response timeout")
	case <-ctx.Done():
		cancel()
		return nil, ctx.Err()
	}
	frames := make(chan omniFrame)
	go func() {
		defer close(frames)
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			select {
			case frames <- omniFrame{line: scanner.Text()}:
			case <-ctx.Done():
				return
			}
		}
		e := scanner.Err()
		if e == nil {
			e = io.ErrUnexpectedEOF
		}
		select {
		case frames <- omniFrame{err: e}:
		case <-ctx.Done():
		}
	}()
	s := &omniStream{resp: resp, frames: frames, cancel: cancel, channelID: id, started: start, meta: clone.ChannelMeta}
	size := 0
	buffered := !info.OmniNativeClaude && omniBufferedRequest(raw)
	terminalSeen := false
	claudeStarted := false
	kuaiMetered := model.OmniKuaiMeteringFor(info.OmniRegistryVersion, info.OriginModelName, id) != nil
	kuaiIdentitySeen := false
	for {
		select {
		case f, ok := <-frames:
			if budget.Enabled() && budget.Remaining(time.Now()) <= 0 {
				cancel()
				return nil, budget.timeoutError(c.Request.Context(), "first content timeout")
			}
			if !ok || f.err != nil {
				cancel()
				return nil, errors.New("stream closed before content")
			}
			size += len(f.line)
			if size > 8<<20 {
				cancel()
				return nil, errors.New("first frame buffer exceeded")
			}
			s.prefix = append(s.prefix, f.line)
			frame, part, effective, _ := omniParse(f.line)
			if frame != nil {
				if choices, ok := frame["choices"].([]any); ok {
					for _, v := range choices {
						if row, ok := v.(map[string]any); ok && row["finish_reason"] != nil {
							terminalSeen = true
						}
					}
				}
			}
			responseDone := false
			if omniResponsesPath(info) {
				var responseFrame map[string]any
				effective, responseDone, responseFrame = omniResponsesFrame(f.line)
				frame, _ = responseFrame["response"].(map[string]any)
				if responseFrame["type"] == "response.output_text.delta" {
					part, _ = responseFrame["delta"].(string)
				}
			}

			if info.OmniNativeClaude {
				var parseErr error
				effective, responseDone, parseErr = omniClaudeProgress(f.line, name, &claudeStarted)
				if parseErr != nil {
					cancel()
					return nil, parseErr
				}
				if effective {
					part = "native content"
				}
			}
			if kuaiMetered {
				identity, declared := frame["model"].(string)
				if (declared && identity != name) || ((effective || responseDone) && !kuaiIdentitySeen && !declared) {
					service.OmniQuarantineKuai(c, info, id, "supplier_model_mismatch")
					cancel()
					return nil, errors.New("supplier model mismatch")
				}
				kuaiIdentitySeen = kuaiIdentitySeen || declared
			}
			if omniFrameError(f.line) {
				cancel()
				return nil, errors.New("upstream error frame")
			}
			if effective && s.firstMillis == 0 {
				s.firstMillis = time.Since(start).Milliseconds()
			}
			if budget.Enabled() && (effective && info.OmniNativeClaude || omniWaitFrameProgress(f.line, info)) && budget.ObserveProgress() {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(budget.Remaining(time.Now()))
			}
			if responseDone || strings.TrimSpace(f.line) == "data: [DONE]" {
				if !responseDone && !terminalSeen {
					cancel()
					return nil, errors.New("missing terminal finish")
				}
				if buffered {
					if err := omniValidateBuffered(s.prefix, raw); err != nil {
						cancel()
						return nil, err
					}
				}
				if budget.Enabled() && budget.Remaining(time.Now()) <= 0 {
					cancel()
					return nil, budget.timeoutError(c.Request.Context(), "first content timeout")
				}
				return s, nil
			}
			if effective {
				if !buffered && part != "" {
					return s, nil
				}
				if budget.Enabled() {
					continue
				}
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				// Reasoning and buffered tool arguments have not committed a
				// response yet. Keep the model's initial waiting allowance;
				// a shorter idle window here aborts valid long reasoning.
				timer.Reset(time.Duration(max(p.IdleSeconds, p.FirstSecondsFor(info.OriginModelName))) * time.Second)
			}
		case <-timer.C:
			cancel()
			return nil, budget.timeoutError(c.Request.Context(), "first content timeout")
		case <-ctx.Done():
			cancel()
			return nil, ctx.Err()
		}
	}
}

type omniBody struct {
	*io.PipeReader
	cancel context.CancelFunc
}

func (b *omniBody) Close() error { b.cancel(); return b.PipeReader.Close() }
func (a *Adaptor) omniRequest(c *gin.Context, info *relaycommon.RelayInfo, body io.Reader) (*http.Response, error) {
	p := service.OmniRoutingFor(info)
	ids := p.Models[info.OriginModelName]
	if _, pinned, _ := service.GetChannelConstraints(c).ResolvedPin(); pinned {
		return channel.DoApiRequest(a, c, info, body)
	}
	if !p.Enabled || !service.OmniRoutingUserAllowed(p, info.UserId) || (c.Request.URL.Path != "/v1/chat/completions" && info.RelayFormat != types.RelayFormatClaude && info.RelayFormat != types.RelayFormatGemini && !omniResponsesPath(info)) || len(ids) == 0 {
		return channel.DoApiRequest(a, c, info, body)
	}
	data, err := omniReadResilientBody(body)
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if json.Unmarshal(data, &raw) != nil {
		return nil, errors.New("invalid chat request")
	}
	if _, ok := raw["messages"].([]any); !ok && !omniResponsesPath(info) {
		return nil, errors.New("messages must be an array")
	}
	if omniResponsesPath(info) && (raw["previous_response_id"] != nil || raw["conversation"] != nil) {
		return channel.DoApiRequest(a, c, info, bytes.NewReader(data))
	}
	service.InitOmniRouteTrace(c)
	if err := omniAdmissionError(c, info, raw, ids); err != nil {
		return nil, err
	}
	omniInitWaitBudget(c, info, p)
	if cfg, ok := p.WaitBudgetFor(info.OriginModelName); ok && info.IsStream && cfg.HeartbeatSeconds > 0 {
		stopHeartbeat := service.StartOmniPrecommitHeartbeat(c, info.RelayFormat, time.Duration(cfg.HeartbeatSeconds)*time.Second)
		defer stopHeartbeat()
	}
	if !info.IsStream {
		return a.omniJSONRequest(c, info, raw, p)
	}
	if omniResponsesPath(info) {
		return a.omniResponsesRequest(c, info, raw, p)
	}
	ctx, cancel := context.WithCancel(c.Request.Context())
	child := c.Copy()
	child.Request = c.Request.Clone(ctx)
	child.Writer = c.Writer
	var current *omniStream
	attempts := 0
	next := 0
	attemptLimit := len(ids)
	openNext := func(payload map[string]any) (*omniStream, error) {
		var last error
		for next < len(ids) && attempts < attemptLimit {
			if err := service.OmniRiskRequestGuard(c, info); err != nil {
				return nil, service.OmniRiskAPIError(err)
			}
			id := ids[next]
			next++
			key := fmt.Sprintf("%d:%s", id, info.OriginModelName)
			if !service.OmniCircuitAllow(key, time.Now()) {
				service.RecordOmniRouteDecision(c, id, "skipped", "circuit_open")
				continue
			}
			attempts++
			start := time.Now()
			input, _ := json.Marshal(payload["messages"])
			tokens := service.CountTextToken(string(input), info.OriginModelName)
			audit := model.OmniAttempt{RequestID: info.RequestId, ChannelID: id, Model: info.OriginModelName, Ordinal: attempts, Continuation: len(payload["messages"].([]any)) > len(raw["messages"].([]any)), InputTokens: tokens, CostEstimated: true}
			cost := p.Costs[info.OriginModelName][id]
			audit.CostUSD = float64(tokens) * cost.Input / 1000000
			s, e := omniOpen(a, child, info, payload, id, p)
			if service.OmniIsRiskError(e) && !service.OmniTrialBackupError(e) {
				service.OmniCircuitNeutral(key)
				return nil, e
			}
			if e == nil {
				service.RecordOmniRouteDecision(c, id, "selected", "first_valid_output")
				audit.FirstMillis = s.firstMillis
				audit.Buffered = omniBufferedRequest(payload)
				audit.CommitMillis = time.Since(start).Milliseconds()
				s.audit = audit
				return s, nil
			}
			audit.DurationMillis = time.Since(start).Milliseconds()
			audit.Outcome = "before_output_failed"
			decision := "failed"
			var unsent *omniUnsentRouteError
			if errors.As(e, &unsent) {
				audit.Outcome = "skipped_before_send"
				audit.InputTokens = 0
				audit.CostUSD = 0
				audit.CostEstimated = false
				decision = "skipped"
			}
			model.OmniRecordAttempt(audit)
			last = e
			service.RecordOmniRouteDecision(c, id, decision, omniRouteFailureReason(e))
			if !omniRetryable(e) {
				service.OmniCircuitNeutral(key)
				return nil, e
			}
			if ctx.Err() != nil {
				service.OmniCircuitNeutral(key)
				return nil, ctx.Err()
			}
			if unsent != nil || omniSupplierRequestLimited(e) {
				service.OmniCircuitNeutral(key)
			} else {
				service.OmniCircuitResult(key, false, time.Now(), p)
			}
		}
		if last == nil {
			last = errors.New("no healthy verified route")
		}
		return nil, last
	}
	current, err = openNext(raw)
	if err != nil {
		cancel()
		return nil, types.NewError(err, types.ErrorCodeDoRequestFailed, types.ErrOptionWithSkipRetry())
	}
	info.ChannelMeta = current.meta
	omniCommitWaitBudget(c)
	reader, writer := io.Pipe()
	response := *current.resp
	response.Body = &omniBody{reader, cancel}
	response.ContentLength = -1
	go func() {
		defer cancel()
		defer writer.Close()
		defer func() {
			current.cancel()
			if current.audit.Outcome == "" {
				service.OmniCircuitNeutral(fmt.Sprintf("%d:%s", current.channelID, info.OriginModelName))
				service.OmniWaiveInterruptedKuai(c, info, current.channelID)
				current.audit.Outcome = "client_cancelled"
				current.audit.DurationMillis = time.Since(current.started).Milliseconds()
				model.OmniRecordAttempt(current.audit)
			}
		}()
		text := ""
		stableID := any(nil)
		stableCreated := any(nil)
		safe := omniPlainRequest(raw)
		if info.Request != nil {
			safe = safe && service.OmniSafeTextRequest(info)
		}
		continued := false
		continuationCommitted := false
		trimmed := false
		pendingText := ""
		terminal := false
		pendingFinish := ""
		pendingUsage := ""
		var originalUsage map[string]any
		emit := func(line string) error {
			if omniFrameError(line) {
				return errors.New("upstream error frame")
			}
			frame, part, effective, frameSafe := omniParse(line)
			if strings.TrimSpace(line) == "data: [DONE]" {
				if !continued && !service.OmniCheckKuaiUsage(c, info, current.channelID, originalUsage, false) {
					pendingUsage = ""
				}
				if continued && !continuationCommitted {
					_, part, _, _ := omniParse(pendingFinish)
					if part == "" {
						return errors.New("continuation made no new progress")
					}
				}
				if pendingFinish != "" {
					if _, e := io.WriteString(writer, pendingFinish+"\n\n"); e != nil {
						return e
					}
					pendingFinish = ""
				}
				if continued {
					usage := map[string]any{}
					for key, value := range originalUsage {
						usage[key] = value
					}
					prompt := info.GetEstimatePromptTokens()
					if value, ok := usage["prompt_tokens"].(float64); ok && value > 0 {
						prompt = int(value)
					}
					output := service.CountTextToken(text, info.OriginModelName)
					usage["prompt_tokens"] = prompt
					usage["completion_tokens"] = output
					usage["total_tokens"] = prompt + output
					encoded, _ := json.Marshal(map[string]any{"id": stableID, "model": info.OriginModelName, "choices": []any{}, "usage": usage})
					if _, e := io.WriteString(writer, "data: "+string(encoded)+"\n\n"); e != nil {
						return e
					}
				}
				if !continued && pendingUsage != "" {
					if _, e := io.WriteString(writer, pendingUsage+"\n\n"); e != nil {
						return e
					}
				}
				terminal = true
			}
			if frame != nil {
				if !continued && omniResponseToolCall(frame) {
					service.RecordOmniReturnedToolCall(c)
				}
				current.audit.OutputTokens += service.CountTextToken(part, info.OriginModelName)
				if frame["usage"] != nil {
					if !continued {
						if usage, ok := frame["usage"].(map[string]any); ok {
							originalUsage = usage
						}
					}
					omniAttemptUsage(&current.audit, frame["usage"], p.Costs[info.OriginModelName][current.channelID], info)
					encoded, _ := json.Marshal(frame)
					pendingUsage = "data: " + string(encoded)
				}
				delete(frame, "usage") // hold upstream usage until successful completion
				safe = safe && frameSafe
				if stableID == nil {
					stableID = frame["id"]
					stableCreated = frame["created"]
				}
				frame["id"] = stableID
				frame["created"] = stableCreated
				frame["model"] = info.OriginModelName
				if continued {
					delete(frame, "usage")
					if !trimmed && (part != "" || effective) {
						pendingText += part
						if len([]rune(pendingText)) < 64 && part != "" {
							return nil
						}
						part = omniOverlap(text, pendingText)
						pendingText = ""
						trimmed = true
						choices, _ := frame["choices"].([]any)
						if len(choices) > 0 {
							ch, _ := choices[0].(map[string]any)
							d, _ := ch["delta"].(map[string]any)
							if d == nil {
								d = map[string]any{}
								ch["delta"] = d
							}
							d["content"] = part
						}
					}
				}
				text += part
				if len(text) > 8<<20 {
					safe = false
				}
				encoded, e := json.Marshal(frame)
				if e != nil {
					return e
				}
				line = "data: " + string(encoded)
				if choices, ok := frame["choices"].([]any); ok {
					for _, v := range choices {
						ch, _ := v.(map[string]any)
						if ch["finish_reason"] != nil {
							pendingFinish = line
							return nil
						}
					}
				}
			}
			_, e := io.WriteString(writer, line+"\n")
			if continued && part != "" && e == nil {
				continuationCommitted = true
			}
			return e
		}
		for {
			failed := false
			for _, line := range current.prefix {
				if err := emit(line); err != nil {
					if ctx.Err() != nil || errors.Is(err, io.ErrClosedPipe) {
						return
					}
					failed = true
					break
				}
			}
			current.prefix = nil
			timer := time.NewTimer(time.Duration(p.IdleSeconds) * time.Second)
			for !failed && !terminal {
				select {
				case f, ok := <-current.frames:
					if !ok || f.err != nil {
						failed = true
						break
					}
					_, _, effective, _ := omniParse(f.line)
					if omniResponsesPath(info) {
						effective, _, _ = omniResponsesFrame(f.line)
					}
					if effective {
						if !timer.Stop() {
							select {
							case <-timer.C:
							default:
							}
						}
						timer.Reset(time.Duration(p.IdleSeconds) * time.Second)
					}
					if omniFrameError(f.line) {
						failed = true
						break
					}
					if err := emit(f.line); err != nil {
						if ctx.Err() != nil || errors.Is(err, io.ErrClosedPipe) {
							timer.Stop()
							return
						}
						failed = true
						break
					}
				case <-timer.C:
					failed = true
				case <-ctx.Done():
					timer.Stop()
					return
				}
			}
			timer.Stop()
			current.cancel()
			key := fmt.Sprintf("%d:%s", current.channelID, info.OriginModelName)
			current.audit.DurationMillis = time.Since(current.started).Milliseconds()
			current.audit.Outcome = "interrupted"
			if terminal {
				current.audit.Outcome = "completed"
			}
			cost := p.Costs[info.OriginModelName][current.channelID]
			if pendingUsage == "" {
				current.audit.CostUSD += float64(current.audit.OutputTokens) * cost.Output / 1000000
			}
			model.OmniRecordAttempt(current.audit)
			if terminal {
				service.OmniCircuitResult(key, true, time.Now(), p)
				return
			}
			service.OmniCircuitResult(key, false, time.Now(), p)
			if service.OmniWaiveInterruptedKuai(c, info, current.channelID) || !p.ContinuationEnabled || !safe || continuationCommitted || text == "" {
				writer.CloseWithError(errors.New("upstream stream interrupted"))
				return
			}
			payload := map[string]any{}
			for k, v := range raw {
				payload[k] = v
			}
			messages := append([]any{}, raw["messages"].([]any)...)
			messages = append(messages, map[string]any{"role": "assistant", "content": text}, map[string]any{"role": "user", "content": "Continue exactly from the end of the assistant text above. Do not repeat it, do not call tools, and do not add a preamble."})
			payload["messages"] = messages
			limit := service.OmniReservedOutput(info)
			for _, key := range []string{"max_completion_tokens", "max_tokens"} {
				if value, ok := raw[key].(float64); ok && value > 0 && (limit == 0 || int(value) < limit) {
					limit = int(value)
				}
			}
			if limit == 0 {
				limit = 4096
			}
			remaining := limit - service.CountTextToken(text, info.OriginModelName)
			if remaining <= 0 {
				writer.CloseWithError(errors.New("output budget exhausted"))
				return
			}
			payload["max_completion_tokens"] = remaining
			delete(payload, "max_tokens")
			pendingFinish = ""
			trimmed = false
			pendingText = ""
			nextStream, e := openNext(payload)
			if e != nil {
				writer.CloseWithError(errors.New("backup continuation failed"))
				return
			}
			current = nextStream
			pendingUsage = ""
			continued = true
		}
	}()
	return &response, nil
}
