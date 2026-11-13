package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"strings"
	"time"
)

func omniResponsesPath(info *relaycommon.RelayInfo) bool {
	return strings.Split(info.RequestURLPath, "?")[0] == "/v1/responses"
}

func omniResponsesToolItem(item map[string]any) bool {
	switch item["type"] {
	case "function_call", "custom_tool_call":
		return true
	case "tool_search_call":
		return item["execution"] == "client"
	}
	return false
}

func omniResponsesFrame(line string) (effective, terminal bool, frame map[string]any) {
	if !strings.HasPrefix(line, "data:") {
		return
	}
	_ = json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &frame)
	kind, _ := frame["type"].(string)
	switch kind {
	case "response.completed":
		return true, true, frame
	case "response.failed", "response.incomplete", "error":
		return true, false, frame
	}
	if delta, ok := frame["delta"].(string); ok && delta != "" {
		return true, false, frame
	}
	if item, ok := frame["item"].(map[string]any); ok && omniResponsesToolItem(item) {
		return true, false, frame
	}
	return
}

// Native tool/thinking streams can pause while a complete argument or thinking
// block is generated. Preserve the model's bounded generation allowance after
// commitment; comments still do not count as generated progress.
func omniCommittedIdleSeconds(info *relaycommon.RelayInfo, raw map[string]any, p service.OmniRoutePolicy) int {
	if info.OmniNativeClaude {
		tools, _ := raw["tools"].([]any)
		thinking, _ := raw["thinking"].(map[string]any)
		if len(tools) > 0 || thinking["type"] == "enabled" || thinking["type"] == "adaptive" {
			if budget, ok := p.WaitBudgetFor(info.OriginModelName); ok {
				return max(p.IdleSeconds, budget.AttemptSeconds)
			}
		}
	}
	return p.IdleSeconds
}

func omniCommittedFailureReason(err error) string {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) { return "stream_upstream_eof" }
	if errors.Is(err, io.ErrClosedPipe) { return "stream_client_closed" }
	if err != nil {
		switch err.Error() {
		case "invalid native Claude frame": return "stream_invalid_frame"
		case "native Claude upstream error": return "stream_upstream_error"
		case "native Claude response model mismatch", "native Claude stream missing message start": return "stream_invalid_sequence"
		}
	}
	return "stream_read_failed"
}

// Responses retain their native identifiers and events. There is no mid-stream
// continuation, including for stateless requests, so stored state is never forked.
func (a *Adaptor) omniResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, raw map[string]any, p service.OmniRoutePolicy) (*http.Response, error) {
	omniInitWaitBudget(c, info, p)
	ctx, cancel := context.WithCancel(c.Request.Context())
	child := c.Copy()
	child.Request = c.Request.Clone(ctx)
	child.Writer = c.Writer
	var current *omniStream
	var last error
	attempts := 0
	for _, id := range p.Models[info.OriginModelName] {
		if err := ctx.Err(); err != nil {
			cancel()
			return nil, err
		}
		key := fmt.Sprintf("%d:%s", id, info.OriginModelName)
		if !service.OmniCircuitAllow(key, time.Now()) {
			service.RecordOmniRouteDecision(c, id, "skipped", "circuit_open")
			continue
		}
		attempts++
		start := time.Now()
		current, last = omniOpen(a, child, info, raw, id, p)
		if last == nil {
			service.RecordOmniRouteDecision(c, id, "selected", "first_valid_output")
			current.audit = model.OmniAttempt{RequestID: info.RequestId, ChannelID: id, Model: info.OriginModelName, Ordinal: attempts, FirstMillis: current.firstMillis, CommitMillis: time.Since(start).Milliseconds(), Buffered: !info.OmniNativeClaude && omniBufferedRequest(raw), CostEstimated: true}
			break
		}
		var unsent *omniUnsentRouteError
		audit := model.OmniAttempt{RequestID: info.RequestId, ChannelID: id, Model: info.OriginModelName, Ordinal: attempts, DurationMillis: time.Since(start).Milliseconds(), Outcome: "before_output_failed", CostEstimated: true}
		decision := "failed"
		if errors.As(last, &unsent) {
			audit.Outcome = "skipped_before_send"
			audit.CostEstimated = false
			decision = "skipped"
		}
		model.OmniRecordAttempt(audit)
		if err := ctx.Err(); err != nil {
			service.RecordOmniRouteDecision(c, id, decision, "client_cancelled")
			service.OmniCircuitNeutral(key)
			cancel()
			return nil, err
		}
		var rejection *model.OmniBindingRejection
		if decision == "skipped" && errors.As(last, &rejection) {
			service.RecordOmniRouteRejection(c, id, rejection)
		} else {
			service.RecordOmniRouteDecision(c, id, decision, omniRouteFailureReason(last))
		}
		if !omniRetryable(last) {
			service.OmniCircuitNeutral(key)
			cancel()
			return nil, types.NewError(last, types.ErrorCodeDoRequestFailed, types.ErrOptionWithSkipRetry())
		}
		if unsent != nil || omniSupplierRequestLimited(last) || omniSupplierHistoryRejected(last) || errors.Is(last, errOmniBackupBudget) || errors.Is(last, errOmniRequestBudget) {
			service.OmniCircuitNeutral(key)
		} else {
			service.OmniCircuitResult(key, false, time.Now(), p)
		}
	}
	if last != nil || current == nil {
		cancel()
		if last == nil {
			last = errors.New("no healthy verified route")
		}
		return nil, types.NewError(last, types.ErrorCodeDoRequestFailed, types.ErrOptionWithSkipRetry())
	}
	info.ChannelMeta = current.meta
	omniCommitWaitBudget(c)
	reader, writer := io.Pipe()
	resp := *current.resp
	resp.Body = &omniBody{reader, cancel}
	resp.ContentLength = -1
	go func() {
		defer cancel()
		defer current.cancel()
		defer writer.Close()
		key := fmt.Sprintf("%d:%s", current.channelID, info.OriginModelName)
		defer func() {
			current.audit.DurationMillis = time.Since(current.started).Milliseconds()
			if current.audit.Outcome == "" {
				service.OmniWaiveInterruptedKuai(c, info, current.channelID)
				current.audit.Outcome = "client_cancelled"
				service.OmniCircuitNeutral(key)
			}
			model.OmniRecordAttempt(current.audit)
		}()
		idle := time.Duration(omniCommittedIdleSeconds(info, raw, p)) * time.Second
		timer := time.NewTimer(idle)
		defer timer.Stop()
		terminal := false
		failureReason := "stream_upstream_eof"
		nativeStarted := false
		nativeUsage := &relayconvert.ClaudeResponseInfo{Usage: &dto.Usage{}}
		emit := func(line string) error {
			effective, done, frame := omniResponsesFrame(line)
			if omniResponseToolCall(frame) {
				service.RecordOmniReturnedToolCall(c)
			}
			if info.OmniNativeClaude {
				var err error
				effective, done, err = omniClaudeProgress(line, current.meta.UpstreamModelName, &nativeStarted)
				if err != nil {
					return err
				}
				if strings.HasPrefix(line, "data:") {
					var event dto.ClaudeResponse
					if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &event) == nil {
						relayconvert.FormatClaudeResponseInfo(&event, nil, nativeUsage)
						if done {
							relayconvert.FinalizeClaudeStreamBillingUsage(nativeUsage)
							omniAttemptUsage(&current.audit, relayconvert.UsageFromClaudeUsage(nativeUsage.Usage), p.Costs[info.OriginModelName][current.channelID], info)
						}
					}
				}
			}
			if effective {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(idle)
			}
			if response, ok := frame["response"].(map[string]any); ok {
				if response["usage"] != nil {
					omniAttemptUsage(&current.audit, response["usage"], p.Costs[info.OriginModelName][current.channelID], info)
				}
			}
			if done && !info.OmniNativeClaude {
				response, _ := frame["response"].(map[string]any)
				if !service.OmniCheckKuaiUsage(c, info, current.channelID, response["usage"], false) {
					delete(response, "usage")
					if data, err := json.Marshal(frame); err == nil {
						line = "data: " + string(data)
					}
				}
			}
			terminal = terminal || done
			_, err := io.WriteString(writer, line+"\n")
			return err
		}
		for _, line := range current.prefix {
			if err := emit(line); err != nil {
				failureReason = omniCommittedFailureReason(err)
				goto interrupted
			}
		}
		for !terminal {
			select {
			case f, ok := <-current.frames:
				if !ok || f.err != nil {
					failureReason = omniCommittedFailureReason(f.err)
					goto interrupted
				}
				if err := emit(f.line); err != nil {
					failureReason = omniCommittedFailureReason(err)
					goto interrupted
				}
			case <-timer.C:
				failureReason = "stream_idle_timeout"
				goto interrupted
			case <-ctx.Done():
				return
			}
		}
		_, _ = io.WriteString(writer, "\n")
		current.audit.Outcome = "completed"
		service.OmniCircuitResult(key, true, time.Now(), p)
		return
	interrupted:
		if ctx.Err() != nil {
			return
		}
		current.audit.Outcome = "interrupted"
		service.RecordOmniRouteDecision(c, current.channelID, "failed", failureReason)
		service.OmniWaiveInterruptedKuai(c, info, current.channelID)
		service.OmniCircuitResult(key, false, time.Now(), p)
		data, _ := json.Marshal(map[string]any{"type": "error", "code": "stream_interrupted", "message": "Upstream stream interrupted; retry the request", "param": nil})
		if info.OmniNativeClaude {
			data = []byte(`{"type":"error","error":{"type":"api_error","message":"Upstream Claude stream interrupted"}}`)
		}
		_, _ = io.WriteString(writer, "\nevent: error\ndata: "+string(data)+"\n\n")
		writer.CloseWithError(errors.New("upstream responses stream interrupted"))
	}()
	return &resp, nil
}
