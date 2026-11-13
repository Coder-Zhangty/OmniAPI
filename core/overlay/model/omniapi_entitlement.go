package model

import (
	"encoding/json"
	"sort"
)

func OmniModelSnapshot() string {
	accepted := map[string]OmniModelPolicy{}
	for name, m := range GetOmniPolicy().Models {
		if OmniEligible(name) {
			accepted[name] = m
		}
	}
	data, _ := json.Marshal(accepted)
	return string(data)
}
func (s OmniSubscription) Includes(name string) bool {
	if s.CommerceProfile != "" {
		var p OmniCommerceProfile
		if json.Unmarshal([]byte(s.CommerceProfile), &p) != nil {
			return false
		}
		return p.Allows(name, 0)
	}
	if GetOmniPolicy().ModelScope == "all_available" {
		status, _, _ := OmniPriceAvailability(name)
		return status == "available"
	}
	var included map[string]OmniModelPolicy
	if s.IncludedModels != "" {
		if json.Unmarshal([]byte(s.IncludedModels), &included) != nil {
			return false
		}
		if m, ok := included[name]; ok && m.Eligible {
			return true
		}
	}
	return OmniEligible(name)
}
func (s OmniSubscription) ModelNames() []string {
	if s.CommerceProfile != "" {
		var p OmniCommerceProfile
		_ = json.Unmarshal([]byte(s.CommerceProfile), &p)
		return omniSortedModelNames(p)
	}
	if GetOmniPolicy().ModelScope == "all_available" {
		names := []string{}
		for name := range GetOmniPrices().Models {
			names = append(names, name)
		}
		sort.Strings(names)
		return names
	}
	names := []string{}
	var included map[string]OmniModelPolicy
	json.Unmarshal([]byte(s.IncludedModels), &included)
	for name := range included {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
func OmniSubscriptionReserveFactor(uid int, name string) float64 {
	factor := OmniReserveInputFactor(name)
	var s OmniSubscription
	if DB.Select("included_models").First(&s, "user_id = ?", uid).Error == nil {
		var included map[string]OmniModelPolicy
		if json.Unmarshal([]byte(s.IncludedModels), &included) == nil {
			if m, ok := included[name]; ok {
				factor = max(factor, m.ReserveInputFactor)
			}
		}
	}
	return factor
}
