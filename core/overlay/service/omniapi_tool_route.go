package service

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

type omniToolRoute struct {
	Kinds          []string `json:"kinds"`
	Choice         string   `json:"choice"`
	Parallel       string   `json:"parallel,omitempty"`
	HasDefinitions bool     `json:"has_definitions,omitempty"`
	HasCallHistory bool     `json:"has_call_history,omitempty"`
	HasResult      bool     `json:"has_result,omitempty"`
}

type omniToolItem struct {
	Type      string         `json:"type"`
	Execution string         `json:"execution"`
	Tools     []omniToolItem `json:"tools"`
}

func omniRouteToolChoice(value any) string {
	if value == nil {
		return "default"
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "other"
	}
	if bytes.Equal(encoded, []byte("null")) {
		return "default"
	}
	var mode string
	if json.Unmarshal(encoded, &mode) != nil {
		var object struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(encoded, &object) == nil {
			mode = object.Type
		}
	}
	switch mode {
	case "auto", "none", "required":
		return mode
	case "any":
		return "required"
	case "tool", "function", "custom":
		return "named"
	default:
		return "other"
	}
}

func (audit *omniToolRoute) add(kind string) {
	if !slices.Contains(audit.Kinds, kind) {
		audit.Kinds = append(audit.Kinds, kind)
	}
}

func (audit *omniToolRoute) definition(tool omniToolItem, native bool) {
	audit.HasDefinitions = true
	switch {
	case tool.Type == "function" || (native && tool.Type == ""):
		audit.add("function")
	case tool.Type == "custom", tool.Type == "namespace":
		audit.add(tool.Type)
	case tool.Type == "tool_search" && tool.Execution == "client":
		audit.add("client_tool_search")
	case tool.Type == "web_search", tool.Type == "web_search_preview", strings.HasPrefix(tool.Type, "web_search_"):
		audit.add("hosted_web_search")
	case strings.HasPrefix(tool.Type, "web_fetch_"):
		audit.add("hosted_web_fetch")
	case tool.Type == "file_search", tool.Type == "code_interpreter", tool.Type == "mcp", tool.Type == "image_generation":
		audit.add("hosted_" + tool.Type)
	case native && strings.HasPrefix(tool.Type, "bash_"):
		audit.add("client_bash")
	case native && strings.HasPrefix(tool.Type, "text_editor_"):
		audit.add("client_text_editor")
	default:
		audit.add("other")
	}
	if tool.Type == "namespace" {
		for _, child := range tool.Tools {
			audit.definition(child, native)
		}
	}
}

func (audit *omniToolRoute) definitions(data []byte, native bool) {
	var tools []omniToolItem
	if json.Unmarshal(data, &tools) == nil {
		for _, tool := range tools {
			audit.definition(tool, native)
		}
	}
}

func omniRouteParallel(value string) string {
	switch value {
	case "true":
		return "enabled"
	case "false":
		return "disabled"
	default:
		return ""
	}
}

func omniRequestToolRoute(info *relaycommon.RelayInfo) *omniToolRoute {
	audit := &omniToolRoute{}
	switch request := info.Request.(type) {
	case *dto.ClaudeRequest:
		audit.Choice = omniRouteToolChoice(request.ToolChoice)
		if encoded, err := json.Marshal(request.ToolChoice); err == nil {
			var choice struct {
				DisableParallel *bool `json:"disable_parallel_tool_use"`
			}
			if json.Unmarshal(encoded, &choice) == nil && choice.DisableParallel != nil {
				audit.Parallel = "enabled"
				if *choice.DisableParallel {
					audit.Parallel = "disabled"
				}
			}
		}
		if tools, err := json.Marshal(request.Tools); err == nil {
			audit.definitions(tools, true)
		}
		for _, msg := range request.Messages {
			blocks, _ := msg.ParseContent()
			for _, block := range blocks {
				switch block.Type {
				case "tool_use":
					audit.HasCallHistory = true
				case "tool_result":
					audit.HasResult = true
				case "server_tool_use":
					audit.HasCallHistory = true
					audit.add("hosted_other")
				case "web_search_tool_result", "web_fetch_tool_result":
					audit.HasResult = true
					audit.add("hosted_" + strings.TrimSuffix(block.Type, "_tool_result"))
				}
			}
		}
	case *dto.OpenAIResponsesRequest:
		audit.Choice = omniRouteToolChoice(request.ToolChoice)
		audit.Parallel = omniRouteParallel(strings.TrimSpace(string(request.ParallelToolCalls)))
		audit.definitions(request.Tools, false)
		var items []omniToolItem
		if json.Unmarshal(request.Input, &items) == nil {
			for _, item := range items {
				switch item.Type {
				case "function_call", "custom_tool_call", "tool_search_call":
					audit.HasCallHistory = true
				case "function_call_output", "custom_tool_call_output", "tool_search_output":
					audit.HasResult = true
				default:
					continue
				}
				switch item.Type {
				case "function_call", "function_call_output":
					audit.add("function")
				case "custom_tool_call", "custom_tool_call_output":
					audit.add("custom")
				default:
					if item.Execution == "client" {
						audit.add("client_tool_search")
					} else {
						audit.add("hosted_other")
					}
				}
			}
		}
	case *dto.GeneralOpenAIRequest:
		audit.Choice = omniRouteToolChoice(request.ToolChoice)
		if request.ParallelTooCalls != nil {
			audit.Parallel = "disabled"
			if *request.ParallelTooCalls {
				audit.Parallel = "enabled"
			}
		}
		var functions []json.RawMessage
		if json.Unmarshal(request.Functions, &functions) == nil && len(functions) > 0 {
			audit.HasDefinitions = true
			audit.add("function")
		}
		if request.WebSearchOptions != nil {
			audit.HasDefinitions = true
			audit.add("hosted_web_search")
		}
		for _, tool := range request.Tools {
			audit.definition(omniToolItem{Type: tool.Type}, false)
		}
		for _, msg := range request.Messages {
			var calls []omniToolItem
			if json.Unmarshal(msg.ToolCalls, &calls) == nil && len(calls) > 0 {
				audit.HasCallHistory = true
				for _, call := range calls {
					switch call.Type {
					case "function", "custom":
						audit.add(call.Type)
					}
				}
			}
			if msg.Role == "tool" || msg.Role == "function" {
				audit.HasResult = true
			}
		}
	}
	if !audit.HasDefinitions && !audit.HasCallHistory && !audit.HasResult {
		return nil
	}
	if len(audit.Kinds) == 0 {
		audit.add("other")
	}
	return audit
}
