package openai

import "fmt"

// Detect protocol tool events without exposing names, arguments or identifiers.
func omniResponseToolCall(result map[string]any) bool {
	if item, ok := result["item"].(map[string]any); ok && omniResponsesToolItem(item) {
		return true
	}
	if block, ok := result["content_block"].(map[string]any); ok && block["type"] == "tool_use" {
		return true
	}
	if response, ok := result["response"].(map[string]any); ok {
		result = response
	}
	if output, ok := result["output"].([]any); ok {
		for _, item := range output {
			if value, ok := item.(map[string]any); ok && omniResponsesToolItem(value) {
				return true
			}
		}
	}
	if content, ok := result["content"].([]any); ok {
		for _, item := range content {
			if value, ok := item.(map[string]any); ok && value["type"] == "tool_use" {
				return true
			}
		}
	}
	if choices, ok := result["choices"].([]any); ok {
		for _, choice := range choices {
			row, _ := choice.(map[string]any)
			for _, key := range []string{"message", "delta"} {
				message, _ := row[key].(map[string]any)
				if calls, _ := message["tool_calls"].([]any); len(calls) > 0 || message["function_call"] != nil {
					return true
				}
			}
		}
	}
	return false
}

func omniForcedTool(raw map[string]any) (bool, string) {
	switch choice := raw["tool_choice"].(type) {
	case string:
		return choice == "required" || choice == "any", ""
	case map[string]any:
		if choice["type"] == "any" {
			return true, ""
		}
		if choice["type"] == "tool_search" {
			return true, "tool_search"
		}
		if choice["type"] == "web_search" || choice["type"] == "web_search_2025_08_26" {
			return true, "web_search"
		}
		if choice["type"] == "function" || choice["type"] == "tool" || choice["type"] == "custom" {
			name, _ := choice["name"].(string)
			if function, ok := choice["function"].(map[string]any); ok {
				name, _ = function["name"].(string)
			}
			return true, name
		}
	}
	return false, ""
}

// Opus 5.5 rejects provider-side forced choice. Generate through its supported
// auto mode, restricting named choices, then validate the original contract
// before any complete JSON or buffered stream is committed to the client.
func omniOpusToolRequest(modelName string, raw map[string]any) map[string]any {
	forced, name := omniForcedTool(raw)
	if !forced {
		return raw
	}
	if modelName == "claude-sonnet-5-5" {
		// Exact Sonnet routes reject forced choice but accept ordinary functions in
		// auto mode. Keep custom/hosted tool contracts untouched until separately
		// verified. The coordinator validates the ORIGINAL contract before commit.
		tools, ok := raw["tools"].([]any)
		if !ok || len(tools) == 0 {
			return raw
		}
		for _, value := range tools {
			tool, ok := value.(map[string]any)
			if !ok || tool["type"] != "function" {
				return raw
			}
			fn, ok := tool["function"].(map[string]any)
			if !ok || fn["name"] == nil {
				return raw
			}
		}
		if choice, ok := raw["tool_choice"].(map[string]any); ok && choice["type"] != "function" {
			return raw
		}
	} else if modelName != "claude-opus-5-5" {
		return raw
	}
	converted := make(map[string]any, len(raw))
	for k, v := range raw {
		converted[k] = v
	}
	converted["tool_choice"] = "auto"
	if name != "" {
		if tools, ok := raw["tools"].([]any); ok {
			selected := []any{}
			for _, value := range tools {
				tool, _ := value.(map[string]any)
				toolName, _ := tool["name"].(string)
				if function, ok := tool["function"].(map[string]any); ok {
					toolName, _ = function["name"].(string)
				}
				if toolName == name {
					selected = append(selected, value)
				}
			}
			if len(selected) > 0 {
				converted["tools"] = selected
			}
		}
	}
	return converted
}

func omniValidateToolNames(names []string, raw map[string]any) error {
	forced, expected := omniForcedTool(raw)
	if !forced {
		return nil
	}
	if len(names) == 0 {
		return fmt.Errorf("required tool call missing from upstream result")
	}
	for _, name := range names {
		if name == "" || (expected != "" && name != expected) {
			return fmt.Errorf("upstream did not call the required tool")
		}
	}
	return nil
}

func omniValidateToolResult(result, raw map[string]any) error {
	names := []string{}
	expectedKind := ""
	if choice, ok := raw["tool_choice"].(map[string]any); ok {
		switch choice["type"] {
		case "function", "tool":
			expectedKind = "function"
		case "custom":
			expectedKind = "custom"
		case "tool_search":
			expectedKind = "tool_search"
		case "web_search", "web_search_2025_08_26":
			expectedKind = "web_search"
		}
	}
	add := func(name, kind string) {
		if expectedKind == "" || expectedKind == kind {
			names = append(names, name)
		}
	}
	if choices, ok := result["choices"].([]any); ok {
		for _, value := range choices {
			choice, _ := value.(map[string]any)
			message, _ := choice["message"].(map[string]any)
			if calls, ok := message["tool_calls"].([]any); ok {
				for _, value := range calls {
					call, _ := value.(map[string]any)
					function, _ := call["function"].(map[string]any)
					name, _ := function["name"].(string)
					add(name, "function")
				}
			}
			if function, ok := message["function_call"].(map[string]any); ok {
				name, _ := function["name"].(string)
				add(name, "function")
			}
		}
	}
	if output, ok := result["output"].([]any); ok {
		for _, value := range output {
			item, _ := value.(map[string]any)
			name, _ := item["name"].(string)
			switch item["type"] {
			case "function_call":
				add(name, "function")
			case "custom_tool_call":
				add(name, "custom")
			case "web_search_call":
				if item["status"] == "completed" {
					add("web_search", "web_search")
				}
			case "tool_search_call":
				if item["execution"] != "client" {
					return fmt.Errorf("upstream returned non-client tool search")
				}
				add("tool_search", "tool_search")
			}
		}
	}
	return omniValidateToolNames(names, raw)
}
