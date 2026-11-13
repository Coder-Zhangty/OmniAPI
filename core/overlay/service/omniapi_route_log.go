package service

import (
	"fmt"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
)

// Shared by success and failure logs. Native selection only sees the primary;
// the attempt ledger contains the actual internal failover sequence.
func OmniAppendRouteLog(ctx *gin.Context, info *relaycommon.RelayInfo, other *model.LogOther) {
	if ctx == nil || other == nil {
		return
	}
	chain := ctx.GetStringSlice("use_channel")
	if info != nil {
		policy := OmniRoutingFor(info)
		if routes := policy.Models[info.OriginModelName]; len(routes) > 0 {
			other.SetAdmin("candidate_channels", routes)
		}
		if info.OmniRegistryVersion != "" {
			other.SetAdmin("route_registry_version", info.OmniRegistryVersion)
		}
		if tools := omniRequestToolRoute(info); tools != nil {
			other.SetAdmin("tool_route", tools)
		}
	}
	if decisions := OmniRouteDecisions(ctx); len(decisions) > 0 {
		other.SetAdmin("route_decisions", decisions)
	}
	if omniReturnedToolCall(ctx) {
		other.SetAdmin("returned_tool_call", true)
	}
	if info != nil {
		attempts := model.OmniRequestAttempts(info.RequestId)
		if len(attempts) > 0 {
			chain = make([]string, 0, len(attempts))
			for _, attempt := range attempts {
				chain = append(chain, fmt.Sprint(attempt.ChannelID))
			}
			other.SetAdmin("channel_attempts", model.OmniPublicAttempts(info.RequestId))
			other.SetAdmin("channel_costs", attempts)
		}
	}
	ctx.Set("omni_node_monitor_chain", chain)
	other.SetAdmin("use_channel", chain)
}
