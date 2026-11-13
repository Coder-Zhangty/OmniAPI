package model

import (
	"encoding/json"
	"slices"
	"sort"
)

// Public capabilities contain retail fees and supported request shapes only.
// Supplier costs, private channel settings and credentials stay private.
func OmniPublicCapabilities() map[string]any {
	registry := GetOmniModelRegistry()
	rows := make(map[string]any)
	var matrix OmniCapabilityMatrix
	cfg := OmniCommerceSettings()
	if DB != nil && OmniCommerceReady() && cfg.ProfileID != "" {
		var profile OmniCommerceProfile
		if DB.First(&profile, "id = ?", cfg.ProfileID).Error == nil && profile.Validate() == nil {
			profile = profile.EffectiveCapabilities()
			_ = json.Unmarshal([]byte(profile.CapabilityMatrix), &matrix)
		}
	}
	for name, row := range registry.Models {
		state, _, _ := OmniPriceAvailability(name)
		if state != "available" {
			continue
		}
		protocols, client := []string{}, []string{}
		protocolLimits := []map[string]any{}
		input := 0
		for _, b := range row.Bindings {
			if b.Status != "verified" {
				continue
			}
			paths := make([]string, 0, len(b.RequestLimits))
			for path := range b.RequestLimits {
				paths = append(paths, path)
			}
			sort.Strings(paths)
			for _, path := range paths {
				evidence, ok := b.LimitsFor(path)
				if !ok {
					continue
				}
				item := map[string]any{"path": path, "basis": evidence.Basis, "verified_at": evidence.VerifiedAt, "full_output_verified": evidence.FullOutputVerified}
				for key, value := range map[string]int64{"max_input_tokens": evidence.MaxInputTokens, "max_context_tokens": evidence.MaxContextTokens, "max_output_tokens": evidence.MaxOutputTokens, "max_request_bytes": evidence.MaxRequestBytes, "max_media_bytes": evidence.MaxMediaBytes, "input_overhead_tokens": evidence.InputOverheadTokens} {
					if value > 0 {
						item[key] = value
					}
				}
				// Each row stays an indivisible route/path envelope. Never merge maxima
				// from separate channels or expose supplier IDs, quotes or credentials.
				duplicate := false
				encoded, _ := json.Marshal(item)
				for _, existing := range protocolLimits {
					other, _ := json.Marshal(existing)
					if string(encoded) == string(other) {
						duplicate = true
						break
					}
				}
				if !duplicate {
					protocolLimits = append(protocolLimits, item)
				}
			}
			input = max(input, b.ContextLength)
			for _, p := range b.Protocols {
				if !slices.Contains(protocols, p) {
					protocols = append(protocols, p)
				}
			}
			for _, tool := range []string{"tools", "custom_tools", "tool_namespaces", "client_tool_search", "native_bash", "native_text_editor"} {
				if slices.Contains(b.Supports, tool) && !slices.Contains(client, tool) {
					client = append(client, tool)
				}
			}
		}
		if len(protocols) == 0 {
			continue
		}
		hosted := make(map[string]any)
		for tool, price := range row.HostedToolPrices {
			supported := false
			for _, b := range row.Bindings {
				if b.Status == "verified" && slices.Contains(b.Supports, "native_"+tool) {
					supported = true
					break
				}
			}
			if supported {
				hosted[tool] = map[string]any{"usd_micros_per_call": price.RetailUSDMicros, "default_max_uses": price.DefaultMaxUses, "accepted_types": price.AcceptedTypes, "estimated": true}
			}
		}
		limits := []map[string]any{}
		sharedImages := false
		for channel, paths := range matrix.Bounds[name] {
			verified := false
			vision := false
			for _, b := range row.Bindings {
				if b.ChannelID == channel && b.Status == "verified" {
					verified = true
					vision = slices.Contains(b.Supports, "vision")
					break
				}
			}
			if !verified {
				continue
			}
			for path, bound := range paths {
				if bound.MaxRequestBytes <= 0 || bound.MaxOutputTokens <= 0 {
					continue
				}
				images := bound.InputImages && vision
				sharedImages = sharedImages || images
				item := map[string]any{"path": path, "max_request_bytes": bound.MaxRequestBytes, "max_output_tokens": bound.MaxOutputTokens, "request_based_reservation": bound.RequestBasedReservation, "input_images": images}
				duplicate := false
				for _, existing := range limits {
					if existing["path"] == path && existing["max_request_bytes"] == bound.MaxRequestBytes && existing["max_output_tokens"] == bound.MaxOutputTokens && existing["request_based_reservation"] == bound.RequestBasedReservation && existing["input_images"] == images {
						duplicate = true
						break
					}
				}
				if !duplicate {
					limits = append(limits, item)
				}
			}
		}
		scope := "bounded_text"
		if sharedImages {
			scope = "text_and_verified_inline_images"
		}
		rows[name] = map[string]any{"protocols": protocols, "client_tools": client, "hosted_tools": hosted, "platform_input_tokens": input, "input_limit_basis": "platform_configuration", "provider_physical_maximum_verified": false, "protocol_request_limits": protocolLimits, "shared_request_limits": limits, "shared_limit_basis": "accepted_request_budget", "shared_scope": scope, "shared_hosted_tools": false, "shared_input_images": sharedImages}
	}
	return map[string]any{"version": registry.Version, "models": rows}
}
