package model

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

func TestOmniCommerceCapabilitySupplementKeepsPurchaseAndReservationSnapshots(t *testing.T) {
	p := commerceFixture(t)
	require.NoError(t, OmniCommercePurchase(91, "v3_99", 1, p.ID, "capability-purchase-001"))
	var before OmniSubscription
	require.NoError(t, DB.First(&before, "user_id = ?", 91).Error)
	var scope map[string][]int
	require.NoError(t, json.Unmarshal([]byte(p.Models), &scope))
	scope["new-verified-model"] = []int{7}
	next := p
	data, _ := json.Marshal(scope)
	next.Models = string(data)
	next.ID = "capability-next"
	next.ContentHash = strings.Repeat("b", 64)
	release := OmniCommerceCapabilityRelease{Version: "capability-test", ApprovedBy: 1, ApprovedAt: time.Now().Unix(), EvidenceHash: strings.Repeat("a", 64), Profile: next, SourceProfiles: map[string]bool{p.ContentHash: true}}
	commerceOption(t, "OmniAPICommerceCapabilities", release)
	require.True(t, p.Allows("new-verified-model", 7))
	require.False(t, p.Allows("new-verified-model", 8))
	effective := p.EffectiveCapabilities()
	require.Equal(t, p.ID, effective.ID)
	require.Equal(t, p.ContentHash, effective.ContentHash)
	require.Equal(t, p.PriceSnapshot, effective.PriceSnapshot)
	require.Equal(t, p.WorstCostPPM, effective.WorstCostPPM)
	raw, _ := json.Marshal(p)
	frozen := OmniFreezeCapabilityProfile(string(raw))
	var frozenProfile OmniCommerceProfile
	require.NoError(t, json.Unmarshal([]byte(frozen), &frozenProfile))
	require.Equal(t, "capability-test", frozenProfile.CapabilityReleaseVersion)
	scope["later-model"] = []int{7}
	data, _ = json.Marshal(scope)
	release.Profile.Models = string(data)
	commerceOption(t, "OmniAPICommerceCapabilities", release)
	require.True(t, p.Allows("later-model", 7))
	require.False(t, frozenProfile.Allows("later-model", 7))
	var after OmniSubscription
	require.NoError(t, DB.First(&after, "user_id = ?", 91).Error)
	require.Equal(t, before, after)
	release.Profile.WorstCostPPM = p.WorstCostPPM + 1
	commerceOption(t, "OmniAPICommerceCapabilities", release)
	require.False(t, p.Allows("new-verified-model", 7))
}

func TestOmniPrimaryPublicationPreservesOlderFundingScope(t *testing.T) {
	old := commerceFixture(t)
	previous := old.PricingHash + ":" + old.RoutingHash
	commerceOption(t, "OmniAPIRouting", map[string]any{"models": map[string][]int{"gpt-6.1-sol": {9, 7, 8}}})
	current := old
	current.RoutingHash = omniOptionHash("OmniAPIRouting")
	current.WorstCostPPM = old.WorstCostPPM + 1
	current.ContentHash = strings.Repeat("b", 64)
	release := OmniCommerceCapabilityRelease{Version: "primary-test", ApprovedBy: 1, ApprovedAt: time.Now().Unix(),
		EvidenceHash: strings.Repeat("a", 64), Profile: current, PreserveSources: map[string]bool{old.ContentHash: true}, CompatiblePairs: []string{previous}}
	commerceOption(t, "OmniAPICommerceCapabilities", release)
	require.True(t, OmniCapabilityProfileCurrent(old))
	require.True(t, old.Allows("gpt-6.1-sol", 7))
	require.False(t, old.Allows("gpt-6.1-sol", 9))
	release.CompatiblePairs = nil
	commerceOption(t, "OmniAPICommerceCapabilities", release)
	require.False(t, OmniCapabilityProfileCurrent(old))
}

func TestOmniCommerceCapabilityPreviewCannotOverwriteVersion(t *testing.T) {
	p := commerceFixture(t)
	require.NoError(t, DB.AutoMigrate(&Option{}))
	cfg, _ := json.Marshal(OmniCommerceSwitches{ProfileID: p.ID})
	require.NoError(t, DB.Create(&Option{Key: "OmniAPICommerceV3", Value: string(cfg)}).Error)
	_, err := OmniPreviewCapabilityPublication(OmniCapabilityPublicationInput{Profile: p, EvidenceHash: strings.Repeat("a", 64)}, 1)
	require.ErrorContains(t, err, "不可变")
}
