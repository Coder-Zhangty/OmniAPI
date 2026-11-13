package openai

import (
	"encoding/json"
	"errors"
	"github.com/QuantumNous/new-api/model"
	"strings"
)

// An unset mapping means pass the requested model unchanged, including the
// non-null empty strings saved by the channel editor. Invalid configured
// mappings fail this candidate rather than leaking configuration or renaming
// the requested model silently.
func omniMappedModel(config *string, original string) (string, error) {
	if config == nil || strings.TrimSpace(*config) == "" {
		return original, nil
	}
	var mapping map[string]string
	if err := json.Unmarshal([]byte(*config), &mapping); err != nil {
		return "", errors.New("invalid channel model mapping")
	}
	if mapped := mapping[original]; strings.TrimSpace(mapped) != "" {
		return mapped, nil
	}
	return original, nil
}

func omniChannelModel(config *string, original, version string, id int) (string, error) {
	if upstream, ok := model.OmniChannelUpstream(version, original, id); ok {
		return upstream, nil
	}
	if model.OmniRegistryAt(version).Enabled {
		return "", errors.New("channel binding is not accepted")
	}
	return omniMappedModel(config, original)
}

func omniChannelKind(version, name string, id, fallback int) int {
	registry := model.OmniRegistryAt(version)
	for _, binding := range registry.Models[registry.Canonical(name)].Bindings {
		if binding.ChannelID == id {
			for _, protocol := range binding.Protocols {
				if protocol == "claude" || protocol == "anthropic" {
					return 14
				}
			}
		}
	}
	return fallback
}

func omniRequiredCapabilities(raw map[string]any) []string {
	required := []string{}
	if raw["tools"] != nil || raw["functions"] != nil {
		required = append(required, "tools")
	}
	strictReport := false
	var clientTools func(any)
	clientTools = func(value any) {
		switch v := value.(type) {
		case []any:
			for _, child := range v {
				clientTools(child)
			}
		case map[string]any:
			function, _ := v["function"].(map[string]any)
			if v["type"] == "function" && function["name"] == "submit_risk_report" && function["strict"] == true {
				strictReport = true
			}
			capability := ""
			switch v["type"] {
			case "custom":
				capability = "custom_tools"
			case "namespace":
				capability = "tool_namespaces"
			case "tool_search":
				if v["execution"] == "client" {
					capability = "client_tool_search"
				}
			case "tool_search_output":
				capability = "client_tool_search"
			case "web_search", "web_search_2025_08_26":
				capability = "web_search"
			}
			if kind,ok:=v["type"].(string);ok {
				if strings.HasPrefix(kind,"web_search_")&&v["name"]=="web_search" {capability="native_web_search"}
				if strings.HasPrefix(kind,"web_fetch_")&&v["name"]=="web_fetch" {capability="native_web_fetch"}
				if strings.HasPrefix(kind,"bash_")&&v["name"]=="bash" {capability="native_bash"}
				if strings.HasPrefix(kind,"text_editor_") {capability="native_text_editor"}
			}
			if capability != "" {
				found := false
				for _, c := range required {
					if c == capability {
						found = true
						break
					}
				}
				if !found {
					required = append(required, capability)
				}
			}
			if v["type"] == "namespace" || v["type"] == "tool_search_output" {
				clientTools(v["tools"])
			}
		}
	}
	clientTools(raw["tools"])
	for _, capability := range required {
		if capability == "web_search" {
			required = append(required, "web_search_token_priced")
			break
		}
	}
	if input, ok := raw["input"].([]any); ok {
		for _, value := range input {
			if item, ok := value.(map[string]any); ok && item["type"] == "tool_search_output" {
				clientTools(item)
			}
		}
	}
	structured := func(value any) bool {
		format, ok := value.(map[string]any)
		return ok && (format["type"] == "json_schema" || format["type"] == "json_object")
	}
	text, _ := raw["text"].(map[string]any)
	if strictReport {
		required = append(required, "risk_report")
	}
	if structured(raw["response_format"]) || structured(text["format"]) {
		required = append(required, "structured")
	}
	if omniImageInput(raw["messages"]) || omniImageInput(raw["input"]) {
		required = append(required, "vision")
	}
	cache, oneHour := omniExplicitCache(raw["messages"])
	inputCache, inputOneHour := omniExplicitCache(raw["input"])
	systemCache, systemOneHour := omniExplicitCache(raw["system"])
	if oneHour || inputOneHour || systemOneHour {
		required = append(required, "cache_write_1h")
	} else if cache || inputCache || systemCache {
		required = append(required, "cache_write")
	}
	return required
}
func omniExplicitCache(value any) (bool, bool) {
	cache, oneHour := false, false
	switch v := value.(type) {
	case []any:
		for _, item := range v {
			found, hour := omniExplicitCache(item)
			cache = cache || found
			oneHour = oneHour || hour
		}
	case map[string]any:
		if control, ok := v["cache_control"].(map[string]any); ok && control["type"] == "ephemeral" {
			cache = true
			oneHour = control["ttl"] == "1h"
		}
		for _, key := range []string{"content"} {
			if item, ok := v[key]; ok {
				found, hour := omniExplicitCache(item)
				cache = cache || found
				oneHour = oneHour || hour
			}
		}
	}
	return cache, oneHour
}
func omniImageInput(value any) bool {
	switch v := value.(type) {
	case []any:
		for _, item := range v {
			if omniImageInput(item) {
				return true
			}
		}
	case map[string]any:
		if v["type"] == "image_url" || v["type"] == "input_image" || v["type"] == "image" {
			return true
		}
		for _, key := range []string{"content", "image_url"} {
			if item, ok := v[key]; ok {
				if key == "image_url" || omniImageInput(item) {
					return true
				}
			}
		}
	}
	return false
}
