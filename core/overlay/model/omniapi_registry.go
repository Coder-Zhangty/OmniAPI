package model

import (
	"errors"
	"github.com/QuantumNous/new-api/common"
	"math"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

type OmniModelBinding struct {
	RequestLimits        map[string]OmniRequestLimits `json:"request_limits,omitempty"`
	KuaiMetering         *OmniKuaiMeteringPolicy      `json:"kuai_metering,omitempty"`
	VerifiedPathsOnly    bool                         `json:"verified_paths_only,omitempty"`
	ChannelID            int                          `json:"channel_id"`
	UpstreamModel        string                       `json:"upstream_model"`
	Status               string                       `json:"status"`
	Protocols            []string                     `json:"protocols"`
	Supports             []string                     `json:"supports,omitempty"`
	ContextLength        int                          `json:"context_length,omitempty"`
	AcceptedOutputTokens int                          `json:"accepted_output_tokens,omitempty"`
	OutputLimitEvidence  string                       `json:"output_limit_evidence,omitempty"`
	Evidence             string                       `json:"evidence,omitempty"`
	Procurement          map[string]float64           `json:"procurement,omitempty"`
}

// Exact native request path, never a maximum borrowed from another protocol.
// Accepted parameters remain distinguishable from documented supplier limits.
type OmniRequestLimits struct {
	MaxInputTokens      int64    `json:"max_input_tokens,omitempty"`
	MaxContextTokens    int64    `json:"max_context_tokens,omitempty"`
	MaxOutputTokens     int64    `json:"max_output_tokens,omitempty"`
	MaxRequestBytes     int64    `json:"max_request_bytes,omitempty"`
	MaxMediaBytes       int64    `json:"max_media_bytes,omitempty"`
	InputOverheadTokens int64    `json:"input_overhead_tokens,omitempty"`
	Basis               string   `json:"basis"`
	EvidenceHash        string   `json:"evidence_hash"`
	Sources             []string `json:"sources,omitempty"`
	VerifiedAt          int64    `json:"verified_at"`
	FullOutputVerified  bool     `json:"full_output_verified"`
}
type OmniModelIdentity struct {
	Lifecycle             string                         `json:"lifecycle,omitempty"`
	LifecycleEvidenceHash string                         `json:"lifecycle_evidence_hash,omitempty"`
	Version               string                         `json:"version"`
	Capability            string                         `json:"capability"`
	Aliases               []string                       `json:"aliases"`
	Bindings              []OmniModelBinding             `json:"bindings"`
	NativeClientTools     []string                       `json:"native_client_tools,omitempty"`
	HostedToolPrices      map[string]OmniHostedToolPrice `json:"hosted_tool_prices,omitempty"`
}

// Per-call USD prices use integer millionths; evidence distinguishes an
// explicit zero-price tool from missing supplier pricing.
type OmniHostedToolPrice struct {
	RetailUSDMicros        int64    `json:"retail_usd_micros"`
	EstimatedCostUSDMicros int64    `json:"estimated_cost_usd_micros"`
	DefaultMaxUses         int      `json:"default_max_uses"`
	EvidenceHash           string   `json:"evidence_hash"`
	Basis                  string   `json:"basis"`
	AcceptedTypes          []string `json:"accepted_types,omitempty"`
}
type OmniModelRegistry struct {
	Enabled           bool                         `json:"enabled"`
	Version           string                       `json:"version"`
	Models            map[string]OmniModelIdentity `json:"models"`
	UserModelUpgrades map[int]map[string]string    `json:"user_model_upgrades,omitempty"`
	aliases           map[string]string
}

var omniRegistryCache = struct {
	sync.Mutex
	raw          string
	current      OmniModelRegistry
	history      map[string]OmniModelRegistry
	registeredAt map[string]time.Time
}{history: map[string]OmniModelRegistry{}, registeredAt: map[string]time.Time{}}
var omniRegistryWrite sync.Mutex

// Compare the stored version and value atomically, including across instances.
func OmniUpdateRegistry(expected string, next OmniModelRegistry) error {
	omniRegistryWrite.Lock()
	defer omniRegistryWrite.Unlock()
	var stored Option
	if err := DB.Where("key = ?", "OmniAPIModelRegistry").First(&stored).Error; err != nil {
		return err
	}
	var current OmniModelRegistry
	if common.Unmarshal([]byte(stored.Value), &current) != nil || current.Version != expected {
		return errors.New("registry version conflict")
	}
	data, err := common.Marshal(next)
	if err != nil {
		return err
	}
	result := DB.Model(&Option{}).Where("key = ? AND value = ?", stored.Key, stored.Value).Update("value", string(data))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("registry version conflict")
	}
	return updateOptionMap(stored.Key, string(data))
}

func OmniValidateRegistry(p OmniModelRegistry) error {
	if p.Version == "" || len(p.Version) > 128 || len(p.Models) > 2000 {
		return errors.New("invalid model registry")
	}
	seen := map[string]string{}
	for name, row := range p.Models {
		if row.Lifecycle != "" && row.Lifecycle != "active" && row.Lifecycle != "unavailable" && row.Lifecycle != "retired" {
			return errors.New("invalid model lifecycle")
		}
		if (row.Lifecycle == "retired" || row.Lifecycle == "unavailable") && len(row.LifecycleEvidenceHash) != 64 {
			return errors.New("model lifecycle needs evidence")
		}
		for tool, price := range row.HostedToolPrices {
			if len(price.AcceptedTypes) > 16 {
				return errors.New("too many hosted tool versions")
			}
			for _, kind := range price.AcceptedTypes {
				if len(kind) > 64 || !strings.HasPrefix(kind, tool+"_") {
					return errors.New("invalid accepted hosted tool version")
				}
			}
			if (tool != "web_search" && tool != "web_fetch") || price.RetailUSDMicros < 0 || price.RetailUSDMicros > 1000000000 || price.EstimatedCostUSDMicros < 0 || len(price.EvidenceHash) != 64 || price.Basis == "" || price.DefaultMaxUses < 1 || price.DefaultMaxUses > 10000 {
				return errors.New("invalid hosted tool quote")
			}
		}
		for _, tool := range row.NativeClientTools {
			if tool != "bash" && tool != "text_editor" {
				return errors.New("invalid native client tool acceptance")
			}
		}
		if strings.TrimSpace(name) != name || name == "" || strings.ContainsAny(name, "/ ,\n\r\t") || row.Version == "" || row.Capability == "" {
			return errors.New("invalid standard model identity")
		}
		if previous := seen[name]; previous != "" && previous != name {
			return errors.New("ambiguous model alias")
		}
		seen[name] = name
		for _, alias := range row.Aliases {
			if alias == "" || len(alias) > 256 || strings.ContainsAny(alias, " ,\n\r\t") {
				return errors.New("invalid model alias")
			}
			if previous := seen[alias]; previous != "" && previous != name {
				return errors.New("ambiguous model alias")
			}
			seen[alias] = name
		}
		ids := map[int]bool{}
		for _, b := range row.Bindings {
			if err := omniValidateRequestLimits(b.RequestLimits); err != nil {
				return err
			}
			if b.ChannelID <= 0 || b.UpstreamModel == "" || ids[b.ChannelID] || (b.Status != "verified" && b.Status != "pending" && b.Status != "failed") {
				return errors.New("invalid channel model binding")
			}
			if b.KuaiMetering != nil && (!b.KuaiMetering.Valid() || !strings.HasPrefix(name, "gpt-") || b.UpstreamModel != name || !b.VerifiedPathsOnly || b.Status != "verified") {
				return errors.New("invalid KuaiAPI metering certification")
			}
			ids[b.ChannelID] = true
			if b.Status == "verified" && len(b.Protocols) == 0 {
				return errors.New("verified binding needs protocol evidence")
			}
			if b.ContextLength < 0 {
				return errors.New("invalid context length")
			}
			if b.AcceptedOutputTokens < 0 || b.AcceptedOutputTokens > 1<<30 || b.AcceptedOutputTokens > 0 && len(b.OutputLimitEvidence) != 64 {
				return errors.New("invalid accepted output budget")
			}
			for _, cost := range b.Procurement {
				if math.IsNaN(cost) || math.IsInf(cost, 0) || cost < 0 {
					return errors.New("invalid procurement cost")
				}
			}
		}
	}
	for uid, upgrades := range p.UserModelUpgrades {
		if uid <= 0 || len(upgrades) > 100 {
			return errors.New("invalid user model upgrades")
		}
		for source, target := range upgrades {
			if _, ok := p.Models[source]; !ok || source == target {
				return errors.New("invalid model upgrade source")
			}
			if _, ok := p.Models[target]; !ok || upgrades[target] != "" {
				return errors.New("invalid model upgrade target")
			}
		}
	}
	return nil
}
func GetOmniModelRegistry() OmniModelRegistry {
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap["OmniAPIModelRegistry"]
	common.OptionMapRWMutex.RUnlock()
	return OmniRegistryFromSnapshot(raw)
}

func OmniRegistryFromSnapshot(raw string) OmniModelRegistry {
	omniRegistryCache.Lock()
	defer omniRegistryCache.Unlock()
	if raw == omniRegistryCache.raw {
		return omniRegistryCache.current
	}
	var p OmniModelRegistry
	if common.Unmarshal([]byte(raw), &p) != nil || OmniValidateRegistry(p) != nil {
		p = OmniModelRegistry{Enabled: raw != "", Version: "invalid", Models: map[string]OmniModelIdentity{}}
	}
	p.aliases = map[string]string{}
	for name, row := range p.Models {
		p.aliases[name] = name
		for _, alias := range row.Aliases {
			p.aliases[alias] = name
		}
	}
	omniRegistryCache.raw = raw
	omniRegistryCache.current = p
	if p.Enabled {
		omniRegistryCache.history[p.Version] = p
		omniRegistryCache.registeredAt[p.Version] = time.Now()
		for version, registered := range omniRegistryCache.registeredAt {
			if version != p.Version && time.Since(registered) > 48*time.Hour {
				delete(omniRegistryCache.history, version)
				delete(omniRegistryCache.registeredAt, version)
			}
		}
	}
	return p
}
func OmniRegistryAt(version string) OmniModelRegistry {
	p := GetOmniModelRegistry()
	if version == "" || version == p.Version {
		return p
	}
	omniRegistryCache.Lock()
	defer omniRegistryCache.Unlock()
	return omniRegistryCache.history[version]
}
func (p OmniModelRegistry) Canonical(name string) string {
	if p.Enabled {
		if standard := p.aliases[name]; standard != "" {
			return standard
		}
	}
	return name
}
func OmniCanonicalModel(name string) string { return GetOmniModelRegistry().Canonical(name) }

// Operator-authorized migrations apply before routing, permissions and pricing.
// Global aliases and other customers retain their original model identity.
func (p OmniModelRegistry) CanonicalForUser(uid int, name string) string {
	name = p.Canonical(name)
	if p.Enabled && uid > 0 {
		if target := p.UserModelUpgrades[uid][name]; target != "" {
			return target
		}
	}
	return name
}
func OmniCanonicalLimit(limit map[string]bool) map[string]bool {
	out := map[string]bool{}
	p := GetOmniModelRegistry()
	for name, allowed := range limit {
		if allowed {
			out[p.Canonical(name)] = true
		}
	}
	return out
}
func OmniCanonicalModels(names []string, availableOnly bool) []string {
	seen := map[string]bool{}
	out := []string{}
	p := GetOmniModelRegistry()
	for _, raw := range names {
		name := p.Canonical(raw)
		if seen[name] {
			continue
		}
		if availableOnly {
			status, _, _ := OmniPriceAvailability(name)
			if status == "paused" {
				continue
			}
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
func OmniChannelUpstream(version, name string, id int) (string, bool) {
	p := OmniRegistryAt(version)
	row, known := p.Models[p.Canonical(name)]
	if !p.Enabled || !known {
		return "", false
	}
	for _, b := range row.Bindings {
		if b.ChannelID == id && b.Status == "verified" {
			return b.UpstreamModel, true
		}
	}
	return "", false
}

// Diagnostics have no customer reservation and never use failover. Costs can
// remain unknown; a missing quote must never be represented as a free test.
type OmniDiagnosticAttempt struct {
	ID             uint     `json:"id" gorm:"primaryKey"`
	RequestID      string   `json:"request_id" gorm:"uniqueIndex;size:128"`
	AdminID        int      `json:"admin_id"`
	ChannelID      int      `json:"channel_id"`
	Model          string   `json:"model"`
	State          string   `json:"state"`
	StatusCode     int      `json:"status_code"`
	DurationMillis int64    `json:"duration_millis"`
	InputTokens    int      `json:"input_tokens"`
	OutputTokens   int      `json:"output_tokens"`
	CostUSD        *float64 `json:"cost_usd"`
	CostEstimated  bool     `json:"cost_estimated"`
	CreatedAt      int64    `json:"created_at"`
}

func OmniRecordDiagnostic(row OmniDiagnosticAttempt) error {
	row.CreatedAt = time.Now().Unix()
	return DB.Create(&row).Error
}

func OmniPublicPricing(rows []Pricing) []Pricing {
	p := GetOmniModelRegistry()
	if !p.Enabled {
		return rows
	}
	out := []Pricing{}
	seen := map[string]int{}
	for _, row := range rows {
		name := p.Canonical(row.ModelName)
		if state := p.Models[name].Lifecycle; state == "retired" || state == "unavailable" {
			continue
		}
		if index, ok := seen[name]; ok {
			for _, group := range row.EnableGroup {
				if !slices.Contains(out[index].EnableGroup, group) {
					out[index].EnableGroup = append(out[index].EnableGroup, group)
				}
			}
			for _, endpoint := range row.SupportedEndpointTypes {
				if !slices.Contains(out[index].SupportedEndpointTypes, endpoint) {
					out[index].SupportedEndpointTypes = append(out[index].SupportedEndpointTypes, endpoint)
				}
			}
			continue
		}
		status, reason, version := OmniPriceAvailability(name)
		row.ModelName = name
		row.Availability = status
		row.AvailabilityReason = reason
		row.PriceVersion = version
		if status == "paused" {
			row.ModelRatio = 0
			row.ModelPrice = 0
			row.CompletionRatio = 0
			row.BillingExpr = ""
			row.BillingPluginVariants = nil
		}
		if quote, ok := GetOmniPrices().Models[name]; ok {
			var price struct {
				Expression string `json:"billing_expr"`
			}
			if common.Unmarshal(quote, &price) == nil && price.Expression != "" {
				row.BillingExpr = price.Expression
				row.BillingMode = "tiered_expr"
			}
		}
		seen[name] = len(out)
		out = append(out, row)
	}
	return out
}

func OmniBindingAllows(version, name string, id, tokens int, required []string) bool {
	return OmniBindingReject(version, name, id, tokens, required) == nil
}
