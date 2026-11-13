package openai

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Some Claude-compatible suppliers omit the JSON delta for an empty tool input.
// Normalize only completed calls whose declared schema actually permits {}.
// Partial JSON, required inputs and unfinished streams remain invalid.
func omniNormalizeEmptyToolArguments(lines []string, raw map[string]any) {
	type callState struct {
		choice, name, arguments, id string
		firstLine                   int
		firstFunction               map[string]any
	}
	calls := map[string]*callState{}
	frames := map[int]map[string]any{}
	finished := map[string]bool{}
	done := false
	for lineIndex, line := range lines {
		if strings.TrimSpace(line) == "data: [DONE]" {
			done = true
			continue
		}
		frame, _, _, _ := omniParse(line)
		choices, _ := frame["choices"].([]any)
		for _, value := range choices {
			choice, _ := value.(map[string]any)
			choiceKey := fmt.Sprint(choice["index"])
			if reason, ok := choice["finish_reason"].(string); ok {
				finished[choiceKey] = reason == "tool_calls"
			}
			delta, _ := choice["delta"].(map[string]any)
			toolCalls, _ := delta["tool_calls"].([]any)
			for _, value := range toolCalls {
				call, _ := value.(map[string]any)
				function, ok := call["function"].(map[string]any)
				if !ok {
					continue
				}
				key := choiceKey + ":" + fmt.Sprint(call["index"])
				state := calls[key]
				if state == nil {
					state = &callState{choice: choiceKey, firstLine: lineIndex, firstFunction: function}
					calls[key] = state
					frames[lineIndex] = frame
				}
				name, _ := function["name"].(string)
				arguments, _ := function["arguments"].(string)
				id, _ := call["id"].(string)
				state.name += name
				state.arguments += arguments
				state.id += id
			}
		}
	}
	if !done {
		return
	}
	tools, _ := raw["tools"].([]any)
	changed := map[int]bool{}
	for _, state := range calls {
		if state.arguments != "" || state.name == "" || state.id == "" || !finished[state.choice] {
			continue
		}
		for _, value := range tools {
			tool, _ := value.(map[string]any)
			function, _ := tool["function"].(map[string]any)
			if function["name"] != state.name || function["parameters"] == nil {
				continue
			}
			format := map[string]any{"type": "json_schema", "json_schema": map[string]any{"schema": function["parameters"]}}
			if omniValidateStructured("{}", format) == nil {
				state.firstFunction["arguments"] = "{}"
				changed[state.firstLine] = true
			}
			break
		}
	}
	for index := range changed {
		encoded, err := json.Marshal(frames[index])
		if err == nil {
			lines[index] = "data: " + string(encoded)
		}
	}
}
