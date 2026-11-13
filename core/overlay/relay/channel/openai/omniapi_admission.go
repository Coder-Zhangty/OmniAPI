package openai

import (
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// Classify an entirely inadmissible request before any upstream attempt. This
// cannot replace a real upstream failure with the rejection of a later backup.
func omniAdmissionError(c *gin.Context, info *relaycommon.RelayInfo, raw map[string]any, ids []int) error {
	required := omniRequiredCapabilities(raw)
	failures := make([]*model.OmniBindingRejection, 0, len(ids))
	var best *model.OmniBindingRejection
	for _, id := range ids {
		failure := service.OmniBindingRequestReject(info, id, required)
		if channel, err := model.CacheGetChannel(id); err == nil {
			failure = omniCandidateReject(info, id, channel.Type, raw)
		}
		if failure == nil {
			return nil
		}
		failures = append(failures, failure)
		if best == nil || (failure.Reason == "context_length_exceeded" && (best.Reason != "context_length_exceeded" || failure.Limit > best.Limit)) {
			best = failure
		}
	}
	if best == nil {
		return nil
	}
	for i, id := range ids {
		service.RecordOmniRouteRejection(c, id, failures[i])
	}
	status := 503
	if best.Reason == "context_length_exceeded" || best.Reason == "capability_not_verified" || best.Reason == "input_limit_exceeded" || best.Reason == "output_limit_exceeded" || best.Reason == "request_envelope_invalid" {
		status = 400
	}
	if best.Reason == "request_body_too_large" {
		status = 413
	}
	return types.NewError(best, types.ErrorCode(best.Reason), types.ErrOptionWithStatusCode(status), types.ErrOptionWithSkipRetry())
}
