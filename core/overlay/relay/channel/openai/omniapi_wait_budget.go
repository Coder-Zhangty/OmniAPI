package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

type omniWaitBudgetKey struct{}
type omniRequestWaitBudget struct {
	deadline  time.Time
	committed atomic.Bool
}
type omniAttemptBudget struct {
	enabled           bool
	firstDeadline     time.Time
	absoluteDeadline  time.Time
	request           *omniRequestWaitBudget
	progress          bool
	responsesFailover bool
	backupDeadline    time.Time
}

var (
	errOmniFirstProgressTimeout = errors.New("upstream first progress timeout")
	errOmniBackupBudget         = errors.New("upstream precommit limit with backup available")
	errOmniAttemptBudget        = errors.New("upstream precommit attempt limit")
	errOmniRequestBudget        = errors.New("precommit request budget exhausted")
)

func omniInitWaitBudget(c *gin.Context, info *relaycommon.RelayInfo, p service.OmniRoutePolicy) {
	if c.Request.Context().Value(omniWaitBudgetKey{}) != nil {
		return
	}
	cfg, ok := p.WaitBudgetFor(info.OriginModelName)
	if !ok {
		return
	}
	state := &omniRequestWaitBudget{deadline: time.Now().Add(time.Duration(cfg.PrecommitSeconds) * time.Second)}
	// Store a deadline value, not a cancellable request context: a successfully
	// committed response and safe continuation may legitimately outlive this budget.
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), omniWaitBudgetKey{}, state))
}

func omniCommitWaitBudget(c *gin.Context) {
	if state, ok := c.Request.Context().Value(omniWaitBudgetKey{}).(*omniRequestWaitBudget); ok {
		state.committed.Store(true)
	}
}

func omniAttemptBudgetFor(c *gin.Context, info *relaycommon.RelayInfo, raw map[string]any, id int, p service.OmniRoutePolicy, start time.Time) *omniAttemptBudget {
	cfg, ok := p.WaitBudgetFor(info.OriginModelName)
	if !ok {
		return &omniAttemptBudget{}
	}
	omniInitWaitBudget(c, info, p)
	first := cfg.FirstProgressSecondsByChannel[id]
	// Newly added verified channels must retain this model's shared precommit
	// and absolute attempt limits even before an explicit channel entry exists.
	if first < 1 {
		first = min(p.FirstSecondsFor(info.OriginModelName), 60)
	}
	ids := p.Models[info.OriginModelName]
	state, _ := c.Request.Context().Value(omniWaitBudgetKey{}).(*omniRequestWaitBudget)
	var failover *service.OmniResponsesFailoverBudget
	if info.IsStream && omniResponsesPath(info) && raw["previous_response_id"] == nil && raw["conversation"] == nil {
		failover = cfg.ResponsesFailoverFor(ids)
	}
	currentSeen, later := false, 0
	for _, candidate := range ids {
		if !currentSeen {
			currentSeen = candidate == id
			continue
		}
		if (failover == nil || service.OmniSharedChannelAllowed(info, candidate)) && omniWaitCandidateEligible(info, raw, candidate, start) {
			later++
			if failover == nil {
				break
			}
		}
	}
	if later == 0 {
		first = cfg.LastRouteFirstProgressSeconds
	}
	var backupDeadline time.Time
	if failover != nil && later > 0 {
		channel := failover.Channels[id]
		first = channel.FirstProgressSeconds
		backupDeadline = start.Add(time.Duration(channel.AttemptSeconds) * time.Second)
		if state != nil {
			reserve := time.Duration(later*failover.ReserveSeconds) * time.Second
			backupDeadline = minTime(backupDeadline, state.deadline.Add(-reserve))
		}
	}
	// A non-streaming tool/structured response has no observable token progress
	// until its complete JSON can be validated. Using the streaming first-token
	// deadline cancels a healthy generation before it can satisfy the contract.
	// Keep the absolute attempt and shared precommit deadlines unchanged.
	if !info.IsStream && omniBufferedRequest(raw) {
		first = cfg.AttemptSeconds
	}
	return &omniAttemptBudget{
		enabled: true, firstDeadline: start.Add(time.Duration(first) * time.Second),
		absoluteDeadline: start.Add(time.Duration(cfg.AttemptSeconds) * time.Second),
		request:          state, responsesFailover: failover != nil, backupDeadline: backupDeadline,
	}
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func omniWaitCandidateEligible(info *relaycommon.RelayInfo, raw map[string]any, id int, now time.Time) bool {
	if !service.OmniCircuitAvailable(fmt.Sprintf("%d:%s", id, info.OriginModelName), now) {
		return false
	}
	ch, err := model.CacheGetChannel(id)
	if err != nil || ch.Status != 1 || (ch.Type != 1 && ch.Type != 14) || !strings.Contains(","+ch.Models+",", ","+info.OriginModelName+",") || !strings.Contains(","+ch.Group+",", ","+info.UsingGroup+",") {
		return false
	}
	if omniCandidateReject(info, id, ch.Type, raw) != nil {
		return false
	}
	_, err = omniChannelModel(ch.ModelMapping, info.OriginModelName, info.OmniRegistryVersion, id)
	return err == nil
}

func (b *omniAttemptBudget) Enabled() bool { return b != nil && b.enabled }
func (b *omniAttemptBudget) ObserveProgress() bool {
	if !b.Enabled() || b.progress {
		return false
	}
	b.progress = true
	return true
}
func (b *omniAttemptBudget) deadline() (time.Time, error) {
	deadline := b.absoluteDeadline
	reason := errOmniAttemptBudget
	if !b.progress && b.firstDeadline.Before(deadline) {
		deadline = b.firstDeadline
		reason = errOmniFirstProgressTimeout
	}
	if !b.backupDeadline.IsZero() && !b.backupDeadline.After(deadline) {
		deadline, reason = b.backupDeadline, errOmniBackupBudget
	}
	if b.request != nil && !b.request.committed.Load() && !b.request.deadline.After(deadline) {
		deadline, reason = b.request.deadline, errOmniRequestBudget
	}
	return deadline, reason
}

func (b *omniAttemptBudget) Remaining(now time.Time) time.Duration {
	deadline, _ := b.deadline()
	return max(0, deadline.Sub(now))
}

func (b *omniAttemptBudget) timeoutError(ctx context.Context, legacy string) error {
	if b.responsesFailover {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, reason := b.deadline()
		return reason
	}
	return errors.New(legacy)
}

// Transport headers, comments, role-only deltas and empty tool metadata do not
// establish model progress. Only substantive generated text/reasoning/tool data do.
func omniWaitFrameProgress(line string, info *relaycommon.RelayInfo) bool {
	if !strings.HasPrefix(line, "data:") {
		return false
	}
	var frame map[string]any
	if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &frame) != nil || frame["error"] != nil {
		return false
	}
	if omniResponsesPath(info) {
		kind, _ := frame["type"].(string)
		if strings.HasSuffix(kind, ".delta") {
			if text, ok := frame["delta"].(string); ok && text != "" {
				return true
			}
		}
		if item, ok := frame["item"].(map[string]any); ok && item["type"] == "function_call" {
			return omniWaitNonemptyString(item["name"]) || omniWaitNonemptyString(item["arguments"])
		}
		return false
	}
	choices, _ := frame["choices"].([]any)
	for _, value := range choices {
		choice, _ := value.(map[string]any)
		delta, _ := choice["delta"].(map[string]any)
		for _, key := range []string{"content", "reasoning_content", "reasoning"} {
			if omniWaitNonemptyString(delta[key]) {
				return true
			}
		}
		if call, ok := delta["function_call"].(map[string]any); ok && (omniWaitNonemptyString(call["name"]) || omniWaitNonemptyString(call["arguments"])) {
			return true
		}
		calls, _ := delta["tool_calls"].([]any)
		for _, value := range calls {
			call, _ := value.(map[string]any)
			function, _ := call["function"].(map[string]any)
			if omniWaitNonemptyString(function["name"]) || omniWaitNonemptyString(function["arguments"]) || omniWaitNonemptyString(call["input"]) {
				return true
			}
		}
	}
	return false
}
func omniWaitNonemptyString(value any) bool { text, ok := value.(string); return ok && text != "" }
