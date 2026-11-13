package service

import (
	"encoding/json"
	"errors"
	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"sync"
	"time"
)

type OmniRoutePolicy struct {
	RolloutUserIDs       []int                            `json:"rollout_user_ids,omitempty"`
	NativeClaudeModels   map[string]bool                  `json:"native_claude_models,omitempty"`
	Enabled              bool                             `json:"enabled"`
	ContinuationEnabled  bool                             `json:"continuation_enabled"`
	FirstSeconds         int                              `json:"first_seconds"`
	FirstSecondsByModel  map[string]int                   `json:"first_seconds_by_model,omitempty"`
	IdleSeconds          int                              `json:"idle_seconds"`
	FailureThreshold     int                              `json:"failure_threshold"`
	CooldownSeconds      int                              `json:"cooldown_seconds"`
	Models               map[string][]int                 `json:"models"`
	Costs                map[string]map[int]OmniRouteCost `json:"costs"`
	FallbackOnlyChannels []int                            `json:"fallback_only_channels,omitempty"`
	PrimaryExceptions    []string                         `json:"primary_exceptions,omitempty"`
	WaitBudgets          map[string]OmniRouteWaitBudget   `json:"wait_budgets,omitempty"`
}

// Wait budgets are opt-in per exact model. Unconfigured models retain the
// established first-output and idle timers.
type OmniRouteWaitBudget struct {
	FirstProgressSecondsByChannel map[int]int                  `json:"first_progress_seconds_by_channel"`
	LastRouteFirstProgressSeconds int                          `json:"last_route_first_progress_seconds"`
	AttemptSeconds                int                          `json:"attempt_seconds"`
	PrecommitSeconds              int                          `json:"precommit_seconds"`
	HeartbeatSeconds              int                          `json:"heartbeat_seconds,omitempty"`
	ResponsesFailover             *OmniResponsesFailoverBudget `json:"-"`
}

// Keep transport deadlines outside the routing snapshot sealed by commerce.
const OmniResponsesFailoverOption = "OmniAPIResponsesFailover"

type OmniResponsesFailoverBudget struct {
	ReserveSeconds int                                `json:"reserve_seconds"`
	Channels       map[int]OmniResponsesChannelBudget `json:"channels"`
}

type OmniResponsesChannelBudget struct {
	FirstProgressSeconds int `json:"first_progress_seconds"`
	AttemptSeconds       int `json:"attempt_seconds"`
}

func (b OmniRouteWaitBudget) ResponsesFailoverFor(routes []int) *OmniResponsesFailoverBudget {
	f := b.ResponsesFailover
	if f == nil || len(routes) < 2 || f.ReserveSeconds < 1 || f.ReserveSeconds > b.LastRouteFirstProgressSeconds ||
		f.ReserveSeconds*(len(routes)-1) >= b.PrecommitSeconds {
		return nil
	}
	for _, id := range routes[:len(routes)-1] {
		channel := f.Channels[id]
		if channel.FirstProgressSeconds < 1 || channel.FirstProgressSeconds > channel.AttemptSeconds ||
			channel.AttemptSeconds > b.AttemptSeconds {
			return nil
		}
	}
	return f
}

func (p OmniRoutePolicy) WaitBudgetFor(name string) (OmniRouteWaitBudget, bool) {
	b, ok := p.WaitBudgets[name]
	if !ok || len(b.FirstProgressSecondsByChannel) == 0 || b.LastRouteFirstProgressSeconds < 1 || b.LastRouteFirstProgressSeconds > 180 || b.AttemptSeconds < 1 || b.AttemptSeconds > 180 || b.PrecommitSeconds < 1 || b.PrecommitSeconds > 600 || b.HeartbeatSeconds < 0 || b.HeartbeatSeconds > 60 {
		return OmniRouteWaitBudget{}, false
	}
	for _, seconds := range b.FirstProgressSecondsByChannel {
		if seconds < 1 || seconds > 180 {
			return OmniRouteWaitBudget{}, false
		}
	}
	return b, true
}

func ValidateOmniSupplierOrder(p OmniRoutePolicy) error {
	fallback := map[int]bool{}
	for _, id := range p.FallbackOnlyChannels {
		fallback[id] = true
	}
	exceptions := map[string]bool{}
	for _, name := range p.PrimaryExceptions {
		exceptions[name] = true
	}
	for name, ids := range p.Models {
		if len(ids) == 0 {
			continue
		}
		if fallback[ids[0]] && !(exceptions[name] && len(ids) == 1) {
			return errors.New("fallback supplier cannot be the primary route")
		}
		seenFallback := false
		for _, id := range ids {
			if fallback[id] {
				seenFallback = true
			} else if seenFallback {
				return errors.New("fallback supplier must remain last")
			}
		}
	}
	return nil
}

type OmniRouteCost struct {
	Tiers        []OmniRouteCostTier `json:"tiers,omitempty"`
	CacheRead    float64             `json:"cache_read_usd_per_million"`
	CacheWrite   float64             `json:"cache_write_usd_per_million"`
	CacheWrite1h float64             `json:"cache_write_1h_usd_per_million"`
	Input        float64             `json:"input_usd_per_million"`
	Output       float64             `json:"output_usd_per_million"`
}

// Explicit bounded overrides preserve the existing default for other models.
func (p OmniRoutePolicy) FirstSecondsFor(name string) int {
	if seconds := p.FirstSecondsByModel[name]; seconds >= 1 && seconds <= 180 {
		return seconds
	}
	if p.FirstSeconds >= 1 && p.FirstSeconds <= 120 {
		return p.FirstSeconds
	}
	return 30
}

type OmniRouteCostTier struct {
	MinTokens int           `json:"min_tokens"`
	MaxTokens *int          `json:"max_tokens"`
	Prices    OmniRouteCost `json:"prices"`
}

func (cost OmniRouteCost) ForTokens(tokens int) OmniRouteCost {
	for _, tier := range cost.Tiers {
		if tokens >= tier.MinTokens && (tier.MaxTokens == nil || tokens <= *tier.MaxTokens) {
			return tier.Prices
		}
	}
	return cost
}

func GetOmniRouting() OmniRoutePolicy {
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap["OmniAPIRouting"]
	failoverRaw := common.OptionMap[OmniResponsesFailoverOption]
	common.OptionMapRWMutex.RUnlock()
	return parseOmniRouting(raw, failoverRaw)
}

func OmniRoutingFor(info *relaycommon.RelayInfo) OmniRoutePolicy {
	if info == nil || info.OmniRoutingSnapshot == nil {
		return GetOmniRouting()
	}
	return parseOmniRouting(info.OmniRoutingSnapshot["OmniAPIRouting"], info.OmniRoutingSnapshot[OmniResponsesFailoverOption])
}

func parseOmniRouting(raw, failoverRaw string) OmniRoutePolicy {
	var p OmniRoutePolicy
	if json.Unmarshal([]byte(raw), &p) != nil {
		return OmniRoutePolicy{}
	}
	if p.FirstSeconds < 1 || p.FirstSeconds > 120 {
		p.FirstSeconds = 30
	}
	if p.IdleSeconds < 1 || p.IdleSeconds > 180 {
		p.IdleSeconds = 45
	}
	if p.FailureThreshold < 1 || p.FailureThreshold > 10 {
		p.FailureThreshold = 3
	}
	if p.CooldownSeconds < 1 || p.CooldownSeconds > 600 {
		p.CooldownSeconds = 60
	}
	if failoverRaw != "" {
		var overrides map[string]*OmniResponsesFailoverBudget
		if json.Unmarshal([]byte(failoverRaw), &overrides) == nil {
			for name, override := range overrides {
				if budget, ok := p.WaitBudgets[name]; ok {
					budget.ResponsesFailover = override
					p.WaitBudgets[name] = budget
				}
			}
		}
	}
	return p
}

type omniBreaker struct {
	Failures int
	Until    time.Time
	Probe    bool
}

var omniBreakers = struct {
	sync.Mutex
	Items map[string]*omniBreaker
}{Items: map[string]*omniBreaker{}}

// Lookahead must never consume the sole half-open probe. The actual transport
// path still acquires that probe with OmniCircuitAllow immediately before use.
func OmniCircuitAvailable(key string, now time.Time) bool {
	omniBreakers.Lock()
	defer omniBreakers.Unlock()
	b := omniBreakers.Items[key]
	return b == nil || b.Until.IsZero() || (!now.Before(b.Until) && !b.Probe)
}

func OmniCircuitAllow(key string, now time.Time) bool {
	omniBreakers.Lock()
	defer omniBreakers.Unlock()
	b := omniBreakers.Items[key]
	if b == nil {
		return true
	}
	if !b.Until.IsZero() {
		if now.Before(b.Until) || b.Probe {
			return false
		}
		b.Probe = true
	}
	return true
}
func OmniCircuitResult(key string, success bool, now time.Time, p OmniRoutePolicy) {
	omniBreakers.Lock()
	defer omniBreakers.Unlock()
	b := omniBreakers.Items[key]
	if b == nil {
		b = &omniBreaker{}
		omniBreakers.Items[key] = b
	}
	b.Probe = false
	if success {
		delete(omniBreakers.Items, key)
		return
	}
	b.Failures++
	if b.Failures >= p.FailureThreshold {
		b.Until = now.Add(time.Duration(p.CooldownSeconds) * time.Second)
	}
}
func OmniCircuitNeutral(key string) {
	omniBreakers.Lock()
	defer omniBreakers.Unlock()
	if b := omniBreakers.Items[key]; b != nil {
		b.Probe = false
	}
}

func OmniRoutingUserAllowed(p OmniRoutePolicy, uid int) bool {
	if len(p.RolloutUserIDs) == 0 {
		return true
	}
	for _, id := range p.RolloutUserIDs {
		if id == uid {
			return true
		}
	}
	return false
}
