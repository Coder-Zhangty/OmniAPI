package service

import (
    relaycommon "github.com/QuantumNous/new-api/relay/common"
    "github.com/QuantumNous/new-api/relaykit/dto"
    "github.com/gin-gonic/gin"
)

// Native usage fields are cumulative. Repeated start/delta/terminal events must
// not add the same searches twice, including native protocol conversion routes.
func OmniObserveClaudeHostedUsage(c *gin.Context, info *relaycommon.RelayInfo, response *dto.ClaudeResponse) {
    if c == nil || info == nil || response == nil { return }
    usage := response.Usage
    if response.Message != nil && response.Message.Usage != nil { usage = response.Message.Usage }
    if usage == nil || usage.ServerToolUse == nil { return }
    if info.OmniHostedToolUsage!=nil {info.OmniHostedToolUsage.Observe(info.OmniNativeToolAttempt,usage.ServerToolUse.WebSearchRequests,usage.ServerToolUse.WebFetchRequests)}
    attempt := info.RetryIndex + 1
    if c.GetInt("omni_claude_tool_attempt") != attempt {
        c.Set("omni_claude_tool_attempt",attempt)
        c.Set("claude_web_search_requests",0)
        c.Set("claude_web_fetch_requests",0)
    }
    c.Set("claude_web_search_requests",max(c.GetInt("claude_web_search_requests"),usage.ServerToolUse.WebSearchRequests))
    c.Set("claude_web_fetch_requests",max(c.GetInt("claude_web_fetch_requests"),usage.ServerToolUse.WebFetchRequests))
}
