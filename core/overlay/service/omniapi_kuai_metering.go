package service

import (
	"math"
	"net/http"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
)

const OmniKuaiUserAgent = "OmniAPI/1.0"
const omniKuaiWaiverKey = "omni_kuai_usage_waiver"

var omniKuaiLocalQuarantine sync.Map

func omniKuaiRoute(info *relaycommon.RelayInfo, id int) (*model.OmniKuaiMeteringPolicy, model.OmniKuaiQuarantine) {
	if info == nil {
		return nil, model.OmniKuaiQuarantine{}
	}
	policy := model.OmniKuaiMeteringFor(info.OmniRegistryVersion, info.OriginModelName, id)
	if policy == nil {
		return nil, model.OmniKuaiQuarantine{}
	}
	return policy, model.OmniKuaiQuarantine{ChannelID: id, Model: info.OriginModelName, Path: strings.Split(info.RequestURLPath, "?")[0], EvidenceHash: policy.EvidenceHash}
}

func OmniKuaiDispatchAllowed(info *relaycommon.RelayInfo, id int) bool {
	policy, route := omniKuaiRoute(info, id)
	if policy == nil {
		return true
	}
	if _, quarantined := omniKuaiLocalQuarantine.Load(route); quarantined {
		return false
	}
	return route.Available()
}

// Called after channel header overrides, before the transport or risk ledger.
func OmniApplyKuaiHeaders(req *http.Request, info *relaycommon.RelayInfo) error {
	policy, route := omniKuaiRoute(info, info.GetChannelID())
	if policy == nil {
		return nil
	}
	if req == nil || req.URL == nil || req.URL.Scheme != "https" || req.URL.Host != "kuaiapi.net" || req.URL.User != nil || req.URL.Path != route.Path ||
		(route.Path != "/v1/chat/completions" && route.Path != "/v1/responses") {
		return &model.OmniBindingRejection{Reason: "kuai_transport_not_verified"}
	}
	if !OmniKuaiDispatchAllowed(info, info.GetChannelID()) {
		return &model.OmniBindingRejection{Reason: "kuai_usage_quarantined"}
	}
	req.Host = req.URL.Host
	req.Header.Set("User-Agent", OmniKuaiUserAgent)
	req.Header.Set("Authorization", "Bearer "+info.ApiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Del("OpenAI-Organization")
	req.Header.Del("OpenAI-Project")
	req.Header.Del("X-API-Key")
	if info.IsStream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	return nil
}

type omniKuaiUsageEvidence struct {
	Reason    string `json:"reason"`
	Input     int64  `json:"supplier_input_tokens"`
	Output    int64  `json:"supplier_output_tokens"`
	Cached    int64  `json:"supplier_cached_tokens"`
	InputCap  int64  `json:"certified_input_ceiling"`
	OutputCap int64  `json:"requested_output_ceiling"`
}

func omniKuaiUsageNumber(row map[string]any, key string) (int64, bool) {
	n, ok := row[key].(float64)
	if !ok || n < 0 || n > 1<<30 || math.Trunc(n) != n {
		return 0, false
	}
	return int64(n), true
}

func omniValidateKuaiUsage(info *relaycommon.RelayInfo, path string, policy *model.OmniKuaiMeteringPolicy, raw any) omniKuaiUsageEvidence {
	evidence := omniKuaiUsageEvidence{
		InputCap:  int64(max(0, info.GetEstimatePromptTokens()))*policy.MaxInputRatioPPM/1_000_000 + policy.MaxInputOverhead,
		OutputCap: omniRequestCandidateOutput(info),
	}
	row, ok := raw.(map[string]any)
	if !ok {
		evidence.Reason = "usage_missing"
		return evidence
	}
	inputKey, outputKey := "prompt_tokens", "completion_tokens"
	if path == "/v1/responses" {
		inputKey, outputKey = "input_tokens", "output_tokens"
	}
	input, inputOK := omniKuaiUsageNumber(row, inputKey)
	output, outputOK := omniKuaiUsageNumber(row, outputKey)
	total, totalOK := omniKuaiUsageNumber(row, "total_tokens")
	evidence.Input, evidence.Output = input, output
	if details, exists := row[inputKey+"_details"]; exists && details != nil {
		fields, valid := details.(map[string]any)
		if !valid {
			evidence.Reason = "usage_shape_invalid"
			return evidence
		}
		if _, exists := fields["cached_tokens"]; exists {
			cached, valid := omniKuaiUsageNumber(fields, "cached_tokens")
			if !valid || cached > input {
				evidence.Reason = "usage_cache_invalid"
				return evidence
			}
			evidence.Cached = cached
		}
	}
	switch {
	case !inputOK || !outputOK || !totalOK || input <= 0 || total != input+output:
		evidence.Reason = "usage_totals_invalid"
	case input > evidence.InputCap:
		evidence.Reason = "usage_input_above_certified_ceiling"
	case evidence.OutputCap <= 0 || output > evidence.OutputCap:
		evidence.Reason = "usage_output_above_requested_ceiling"
	}
	return evidence
}

func OmniCheckKuaiUsage(c *gin.Context, info *relaycommon.RelayInfo, id int, raw any, beforeOutput bool) bool {
	policy, route := omniKuaiRoute(info, id)
	if policy == nil {
		return true
	}
	evidence := omniValidateKuaiUsage(info, route.Path, policy, raw)
	if evidence.Reason == "" {
		return true
	}
	OmniQuarantineKuai(c, info, id, evidence.Reason)
	if !beforeOutput {
		c.Set(omniKuaiWaiverKey, evidence)
	}
	return false
}

func OmniQuarantineKuai(c *gin.Context, info *relaycommon.RelayInfo, id int, reason string) {
	policy, route := omniKuaiRoute(info, id)
	if policy == nil {
		return
	}
	omniKuaiLocalQuarantine.Store(route, struct{}{})
	if err := route.Record(reason); err != nil {
		logger.LogError(c, "failed to persist KuaiAPI route quarantine")
	}
	RecordOmniRouteDecision(c, id, "quarantined", reason)
}

func OmniWaiveInterruptedKuai(c *gin.Context, info *relaycommon.RelayInfo, id int) bool {
	policy, _ := omniKuaiRoute(info, id)
	if policy == nil {
		return false
	}
	if c.Request.Context().Err() == nil {
		OmniCheckKuaiUsage(c, info, id, nil, false)
	} else {
		c.Set(omniKuaiWaiverKey, omniKuaiUsageEvidence{Reason: "usage_unavailable_after_client_cancel"})
	}
	return true
}

func settleOmniKuaiWaiver(c *gin.Context, info *relaycommon.RelayInfo) bool {
	value, exists := c.Get(omniKuaiWaiverKey)
	if !exists {
		return false
	}
	evidence, ok := value.(omniKuaiUsageEvidence)
	if !ok || evidence.Reason == "" {
		return false
	}
	if err := SettleBilling(c, info, 0); err != nil {
		logger.LogError(c, "KuaiAPI zero settlement failed; releasing reservation")
		if info.Billing != nil {
			info.Billing.Refund(c)
		}
	}
	other := GenerateTextOtherInfo(c, info, 0, 0, 0, 0, 0, 0, 0)
	other.SetPublic("free_reason", "upstream_usage_unverified")
	other.SetAdmin("supplier_usage_waiver", evidence)
	OmniAppendRouteLog(c, info, other)
	model.RecordConsumeLog(c, info.UserId, model.RecordConsumeLogParams{
		ChannelId: info.GetChannelID(), ModelName: info.GetBillingModelName(), TokenId: info.TokenId,
		TokenName: c.GetString("token_name"), Group: info.UsingGroup, IsStream: info.IsStream, Other: other,
		Content: "上游用量无法核验，本次免单",
	})
	return true
}
