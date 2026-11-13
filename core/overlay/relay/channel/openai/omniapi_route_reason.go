package openai

import (
	"context"
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/model"
	"strings"
)

func omniRouteFailureReason(err error) string {
	var trial *model.OmniTrialRejection
	if errors.As(err, &trial) {
		return trial.Code
	}
	var admission *model.OmniBindingRejection
	if errors.As(err, &admission) {
		return admission.Reason
	}
	var upstream *omniHTTPError
	if errors.As(err, &upstream) {
		if upstream.Reason != "" {
			return upstream.Reason
		}
		return fmt.Sprintf("upstream_http_%d", upstream.Status)
	}
	if errors.Is(err, context.Canceled) {
		return "client_cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "upstream_timeout"
	}
	switch {
	case errors.Is(err, errOmniFirstProgressTimeout):
		return "first_progress_timeout"
	case errors.Is(err, errOmniBackupBudget):
		return "precommit_backup_limit"
	case errors.Is(err, errOmniAttemptBudget):
		return "precommit_attempt_limit"
	case errors.Is(err, errOmniRequestBudget):
		return "precommit_budget_exhausted"
	}
	if err == nil {
		return "none"
	}
	switch err.Error() {
	case "first response timeout", "first content timeout":
		return "first_output_timeout"
	case "route capability not accepted":
		return "binding_capability_or_context_unverified"
	case "route capability mismatch":
		return "model_or_group_mismatch"
	case "route is not enabled OpenAI channel":
		return "channel_disabled_or_protocol_unsupported"
	case "route setup failed":
		return "channel_setup_failed"
	}
	if strings.Contains(err.Error(), "mapping") {
		return "invalid_model_mapping"
	}
	return "upstream_request_failed"
}
