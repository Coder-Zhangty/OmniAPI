package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"strings"
	"time"
)

func omniRetryable(err error) bool {
	if service.OmniIsRiskError(err) {
		return service.OmniTrialBackupError(err)
	}
	var e *omniHTTPError
	if errors.As(err, &e) {
		if e.Reason == "supplier_balance_insufficient" || e.Reason == "supplier_request_limit" || e.Reason == "supplier_signed_history_rejected" {
			return true
		}
		if e.Reason == "request_invalid" {
			return false
		}
		switch e.Status {
		case 413, 415, 422:
			return false
		}
	}
	return true
}

func (a *Adaptor) omniJSONRequest(c *gin.Context, info *relaycommon.RelayInfo, raw map[string]any, p service.OmniRoutePolicy) (*http.Response, error) {
	omniInitWaitBudget(c, info, p)
	attempts := 0
	var last error
	for _, id := range p.Models[info.OriginModelName] {
		if !service.OmniSharedChannelAllowed(info, id) {
			last = errors.New("route not in frozen shared capability set")
			service.RecordOmniRouteDecision(c, id, "skipped", "shared_capability_not_verified")
			continue
		}
		key := fmt.Sprintf("%d:%s", id, info.OriginModelName)
		if !service.OmniCircuitAllow(key, time.Now()) {
			service.RecordOmniRouteDecision(c, id, "skipped", "circuit_open")
			continue
		}
		attempts++
		ch, err := model.CacheGetChannel(id)
		if err != nil {
			last = err
			service.RecordOmniRouteDecision(c, id, "skipped", "channel_unavailable")
			continue
		}
		if ch.Status != 1 || (ch.Type != 1 && ch.Type != 14) || !strings.Contains(","+ch.Models+",", ","+info.OriginModelName+",") || !strings.Contains(","+ch.Group+",", ","+info.UsingGroup+",") {
			last = errors.New("route capability mismatch")
			service.RecordOmniRouteDecision(c, id, "skipped", "model_or_group_mismatch")
			continue
		}
		if rejection := omniCandidateReject(info, id, ch.Type, raw); rejection != nil {
			last = rejection
			service.RecordOmniRouteRejection(c, id, rejection)
			continue
		}
		attemptStart := time.Now()
		wait := time.Duration(p.FirstSecondsFor(info.OriginModelName)) * time.Second
		budget := omniAttemptBudgetFor(c, info, raw, id, p, attemptStart)
		if budget.Enabled() {
			wait = budget.Remaining(attemptStart)
			if wait <= 0 {
				service.OmniCircuitNeutral(key)
				service.RecordOmniRouteDecision(c, id, "skipped", "request_budget_exhausted")
				return nil, types.NewError(errors.New("precommit request budget exhausted"), types.ErrorCodeDoRequestFailed, types.ErrOptionWithSkipRetry())
			}
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), wait)
		child := c.Copy()
		child.Request = c.Request.Clone(ctx)
		child.Writer = c.Writer
		clone := *info
		clone.ChannelMeta = nil
		clone.DisablePing = true
		if err := middleware.SetupContextForSelectedChannel(child, ch, info.OriginModelName); err != nil {
			cancel()
			last = errors.New("route setup failed")
			service.RecordOmniRouteDecision(c, id, "skipped", "channel_setup_failed")
			continue
		}
		clone.InitChannelMeta(child)
		name, err := omniChannelModel(ch.ModelMapping, info.OriginModelName, info.OmniRegistryVersion, id)
		if err != nil {
			cancel()
			last = err
			model.OmniRecordAttempt(model.OmniAttempt{RequestID: info.RequestId, ChannelID: id, Model: info.OriginModelName, Ordinal: attempts, Outcome: "invalid_model_mapping", CostEstimated: true})
			continue
		}
		body := map[string]any{}
		for k, v := range raw {
			body[k] = v
		}
		body["model"] = name
		clone.UpstreamModelName = name
		_, err = json.Marshal(body)
		if err != nil {
			cancel()
			return nil, err
		}
		start := time.Now()
		audit := model.OmniAttempt{RequestID: info.RequestId, ChannelID: id, Model: info.OriginModelName, Ordinal: attempts, CostEstimated: true, Outcome: "before_output_failed"}
		resp, err := omniNativeRequest(child, &clone, body, ch.Type)
		if service.OmniIsRiskError(err) {
			cancel()
			service.OmniCircuitNeutral(key)
			if !service.OmniTrialBackupError(err) {
				return nil, err
			}
			audit.DurationMillis = time.Since(start).Milliseconds()
			audit.Outcome, audit.CostEstimated = "skipped_before_send", false
			model.OmniRecordAttempt(audit)
			service.RecordOmniRouteDecision(c, id, "skipped", omniRouteFailureReason(err))
			last = err
			continue
		}
		var data []byte
		if err == nil {
			if resp.StatusCode != 200 {
				err = omniReadHTTPError(resp)
			} else {
				data, err = io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
				if err == nil && (len(data) > 16<<20 || !json.Valid(data)) {
					err = errors.New("invalid upstream JSON response")
				}
			}
			resp.Body.Close()
		}
		if err == nil && budget.Enabled() {
			if ctx.Err() != nil {
				err = ctx.Err()
			} else if budget.Remaining(time.Now()) <= 0 {
				err = context.DeadlineExceeded
			}
		}
		cancel()
		audit.DurationMillis = time.Since(start).Milliseconds()
		audit.FirstMillis = audit.DurationMillis
		if err == nil {
			var decoded map[string]any
			_ = json.Unmarshal(data, &decoded)
			if model.OmniKuaiMeteringFor(info.OmniRegistryVersion, info.OriginModelName, id) != nil && decoded["model"] != name {
				audit.Outcome = "supplier_model_mismatch"
				model.OmniRecordAttempt(audit)
				service.OmniQuarantineKuai(c, info, id, "supplier_model_mismatch")
				service.OmniCircuitNeutral(key)
				last = errors.New("supplier model mismatch")
				continue
			}
			if decoded["error"] != nil || (info.OmniNativeClaude && !omniClaudeJSONValid(decoded, name)) || (!info.OmniNativeClaude && decoded["choices"] == nil && decoded["output"] == nil) {
				audit.Outcome = "invalid_response"
				model.OmniRecordAttempt(audit)
				service.OmniCircuitResult(key, false, time.Now(), p)
				service.RecordOmniRouteDecision(c, id, "failed", "invalid_upstream_result")
				last = errors.New("invalid upstream result")
				continue
			}
			if err := omniValidateCoordinatedJSON(info, decoded, raw); err != nil {
				audit.Outcome = "invalid_result"
				model.OmniRecordAttempt(audit)
				service.OmniCircuitResult(key, false, time.Now(), p)
				last = err
				service.RecordOmniRouteDecision(c, id, "failed", "invalid_upstream_result")
				continue
			}
			if budget.Enabled() && budget.Remaining(time.Now()) <= 0 {
				audit.Outcome = "upstream_timeout"
				model.OmniRecordAttempt(audit)
				service.OmniCircuitResult(key, false, time.Now(), p)
				service.RecordOmniRouteDecision(c, id, "failed", "upstream_timeout")
				last = context.DeadlineExceeded
				continue
			}
			if info.OmniNativeClaude {
				omniClaudeJSONUsage(&audit, data, p.Costs[info.OriginModelName][id], info)
			} else {
				omniAttemptUsage(&audit, decoded["usage"], p.Costs[info.OriginModelName][id], info)
			}
			if !service.OmniCheckKuaiUsage(c, info, id, decoded["usage"], true) {
				audit.Outcome = "usage_unverified_before_output"
				service.OmniCircuitNeutral(key)
				model.OmniRecordAttempt(audit)
				last = errors.New("supplier usage failed validation")
				continue
			}
			service.RecordOmniRouteDecision(c, id, "selected", "validated_response")
			if omniResponseToolCall(decoded) {
				service.RecordOmniReturnedToolCall(c)
			}
			audit.Outcome = "completed"
			model.OmniRecordAttempt(audit)
			service.OmniCircuitResult(key, true, time.Now(), p)
			info.ChannelMeta = clone.ChannelMeta
			if !info.OmniNativeClaude {
				decoded["model"] = info.OriginModelName
				data, _ = json.Marshal(decoded)
			}
			resp.Body = io.NopCloser(bytes.NewReader(data))
			resp.ContentLength = int64(len(data))
			omniCommitWaitBudget(c)
			return resp, nil
		}
		audit.Outcome = omniRouteFailureReason(err)
		model.OmniRecordAttempt(audit)
		last = err
		service.RecordOmniRouteDecision(c, id, "failed", omniRouteFailureReason(err))
		if c.Request.Context().Err() != nil {
			service.OmniCircuitNeutral(key)
			return nil, c.Request.Context().Err()
		}
		if !omniRetryable(err) {
			service.OmniCircuitNeutral(key)
			return nil, types.NewError(err, types.ErrorCodeDoRequestFailed, types.ErrOptionWithSkipRetry())
		}
		if omniSupplierRequestLimited(err) || omniSupplierHistoryRejected(err) {
			service.OmniCircuitNeutral(key)
		} else {
			service.OmniCircuitResult(key, false, time.Now(), p)
		}
	}
	if last == nil {
		last = errors.New("no healthy verified route")
	}
	return nil, types.NewError(last, types.ErrorCodeDoRequestFailed, types.ErrOptionWithSkipRetry())
}
