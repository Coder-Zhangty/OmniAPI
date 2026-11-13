package openai

import (
	"encoding/json"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// Supplier usage is still an estimate until reconciled with its invoice.
func omniAttemptUsage(a *model.OmniAttempt, usage any, cost service.OmniRouteCost, infos ...*relaycommon.RelayInfo) {
	data, err := json.Marshal(usage)
	if err != nil {
		return
	}
	var u map[string]any
	if json.Unmarshal(data, &u) != nil {
		return
	}
	n := func(row map[string]any, key string) int { v, _ := row[key].(float64); return max(0, int(v)) }
	input := max(n(u, "prompt_tokens"), n(u, "input_tokens"))
	output := max(n(u, "completion_tokens"), n(u, "output_tokens"))
	details, _ := u["prompt_tokens_details"].(map[string]any)
	if details == nil {
		details, _ = u["input_tokens_details"].(map[string]any)
	}
	read := max(n(details, "cached_tokens"), n(u, "prompt_cache_hit_tokens"))
	write := max(n(details, "cached_creation_tokens"), n(details, "cache_write_tokens"))
	oneHour := n(u, "claude_cache_creation_1_h_tokens")
	fiveMinute := n(u, "claude_cache_creation_5_m_tokens")
	write = max(write, fiveMinute+oneHour)
	a.InputTokens = input
	a.OutputTokens = output
	cost = cost.ForTokens(input)
	a.CostUSD = (float64(max(0, input-read-write))*cost.Input + float64(read)*cost.CacheRead + float64(max(0, write-oneHour))*cost.CacheWrite + float64(oneHour)*cost.CacheWrite1h + float64(output)*cost.Output) / 1000000
	if len(infos)>0&&infos[0]!=nil {
		info:=infos[0];search,fetch:=info.OmniHostedToolUsage.Counts()
		for name,count:=range map[string]int{"web_search":search,"web_fetch":fetch} {
			if price,ok:=service.OmniHostedToolPriceFor(info,name);ok&&count>0 {a.CostUSD+=float64(int64(count)*price.EstimatedCostUSDMicros)/1000000;a.CostEstimated=true}
		}
	}
}
