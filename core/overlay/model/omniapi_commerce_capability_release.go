package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

func omniCapabilityOptionHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// Capabilities are an approved supplement to a purchase, never a replacement
// of its price, granted quota, expiry, usage counters or financial snapshot.
type OmniCommerceCapabilityRelease struct {
	Version         string              `json:"version"`
	ApprovedBy      int                 `json:"approved_by"`
	ApprovedAt      int64               `json:"approved_at"`
	EvidenceHash    string              `json:"evidence_hash"`
	Profile         OmniCommerceProfile `json:"profile"`
	SourceProfiles  map[string]bool     `json:"source_profiles"`
	PreserveSources map[string]bool     `json:"preserve_sources,omitempty"`
	CompatiblePairs []string            `json:"compatible_pairs"`
}

func omniCapabilityRelease() *OmniCommerceCapabilityRelease {
	return omniCapabilityReleaseFrom(OmniRequestOptionSnapshot())
}

func omniCapabilityReleaseFrom(snapshot map[string]string) *OmniCommerceCapabilityRelease {
	raw := snapshot["OmniAPICommerceCapabilities"]
	var release OmniCommerceCapabilityRelease
	if json.Unmarshal([]byte(raw), &release) != nil || release.Version == "" || len(release.Version) > 128 || release.ApprovedBy <= 0 || release.ApprovedAt <= 0 || release.ApprovedAt > time.Now().Unix() || len(release.EvidenceHash) != 64 || release.Profile.Validate() != nil || len(release.Profile.ContentHash) != 64 {
		return nil
	}
	if release.Profile.PricingHash != omniCapabilityOptionHash(snapshot["OmniAPIPricing"]) || release.Profile.RoutingHash != omniCapabilityOptionHash(snapshot["OmniAPIRouting"]) {
		return nil
	}
	return &release
}

func (p OmniCommerceProfile) EffectiveCapabilities() OmniCommerceProfile {
	return p.effectiveCapabilitiesFrom(OmniRequestOptionSnapshot())
}

func (p OmniCommerceProfile) effectiveCapabilitiesFrom(snapshot map[string]string) OmniCommerceProfile {
	if p.CapabilityReleaseVersion != "" {
		return p
	}
	r := omniCapabilityReleaseFrom(snapshot)
	if r == nil || !r.SourceProfiles[p.ContentHash] || r.Profile.WorstCostPPM > p.WorstCostPPM {
		return p
	}
	p.Models = r.Profile.Models
	p.CapabilityMatrix = r.Profile.CapabilityMatrix
	p.PricingHash = r.Profile.PricingHash
	p.RoutingHash = r.Profile.RoutingHash
	p.CapabilityReleaseVersion = r.Version
	return p
}

func OmniFreezeCapabilityProfile(raw string) string {
	return omniFreezeCapabilityProfileFrom(raw, OmniRequestOptionSnapshot())
}

func omniFreezeCapabilityProfileFrom(raw string, snapshot map[string]string) string {
	if raw == "" {
		return raw
	}
	var p OmniCommerceProfile
	if json.Unmarshal([]byte(raw), &p) != nil {
		return raw
	}
	effective := p.effectiveCapabilitiesFrom(snapshot)
	if effective.CapabilityReleaseVersion == "" {
		return raw
	}
	data, err := json.Marshal(effective)
	if err != nil {
		return raw
	}
	return string(data)
}

func OmniCapabilityProfileCurrent(p OmniCommerceProfile) bool {
	return omniCapabilityProfileCurrentFrom(p, OmniRequestOptionSnapshot())
}

func omniCapabilityProfileCurrentFrom(p OmniCommerceProfile, snapshot map[string]string) bool {
	p = p.effectiveCapabilitiesFrom(snapshot)
	if p.PricingHash == omniCapabilityOptionHash(snapshot["OmniAPIPricing"]) && p.RoutingHash == omniCapabilityOptionHash(snapshot["OmniAPIRouting"]) {
		return true
	}
	r := omniCapabilityReleaseFrom(snapshot)
	if r == nil {
		return false
	}
	if !r.PreserveSources[p.ContentHash] && (!r.SourceProfiles[p.ContentHash] || p.CapabilityReleaseVersion == "" || r.Profile.WorstCostPPM > p.WorstCostPPM) {
		return false
	}
	pair := p.PricingHash + ":" + p.RoutingHash
	for _, accepted := range r.CompatiblePairs {
		if accepted == pair && len(strings.Split(accepted, ":")) == 2 {
			return true
		}
	}
	return false
}
