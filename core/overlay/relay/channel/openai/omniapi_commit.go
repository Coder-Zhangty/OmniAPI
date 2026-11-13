package openai

import (
	"encoding/json"
	"fmt"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"strings"
)

func omniValidateStructured(text string, format map[string]any) error {
	if format == nil || format["type"] == "text" {
		return nil
	}
	value, err := jsonschema.UnmarshalJSON(strings.NewReader(text))
	if err != nil {
		return fmt.Errorf("invalid structured JSON output")
	}
	if format["type"] == "json_schema" {
		definition, _ := format["json_schema"].(map[string]any)
		compiler := jsonschema.NewCompiler()
		compiler.UseLoader(jsonschema.SchemeURLLoader{}) // local $defs are allowed; network/file references are not.
		if err = compiler.AddResource("https://omniapi.invalid/response-schema", definition["schema"]); err != nil {
			return fmt.Errorf("invalid requested JSON schema")
		}
		schema, err := compiler.Compile("https://omniapi.invalid/response-schema")
		if err != nil {
			return fmt.Errorf("invalid requested JSON schema")
		}
		if schema.Validate(value) != nil {
			return fmt.Errorf("structured output does not match requested schema")
		}
	}
	return nil
}

func omniValidateBuffered(lines []string, raw map[string]any) error {
	omniNormalizeEmptyToolArguments(lines, raw)
	var text strings.Builder
	arguments := map[string]string{}
	names := map[string]string{}
	for _, line := range lines {
		if strings.HasPrefix(line, "data:") {
			var event map[string]any
			if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &event) == nil && event["type"] == "response.completed" {
				response, _ := event["response"].(map[string]any)
				return omniValidateJSONResult(response, raw)
			}
		}
		frame, part, _, _ := omniParse(line)
		text.WriteString(part)
		choices, _ := frame["choices"].([]any)
		for _, value := range choices {
			choice, _ := value.(map[string]any)
			delta, _ := choice["delta"].(map[string]any)
			calls, _ := delta["tool_calls"].([]any)
			for _, value := range calls {
				call, _ := value.(map[string]any)
				function, _ := call["function"].(map[string]any)
				key := fmt.Sprintf("%v:%v", choice["index"], call["index"])
				if name, ok := function["name"].(string); ok {
					names[key] += name
				}
				part, _ := function["arguments"].(string)
				arguments[key] += part
			}
		}
	}
	for _, value := range arguments {
		if !json.Valid([]byte(value)) {
			return fmt.Errorf("incomplete tool arguments")
		}
	}
	toolNames := []string{}
	for _, name := range names {
		toolNames = append(toolNames, name)
	}
	if err := omniValidateToolNames(toolNames, raw); err != nil {
		return err
	}
	if format, ok := raw["response_format"].(map[string]any); ok && format["type"] != "text" {
		return omniValidateStructured(text.String(), format)
	}
	return nil
}
func omniValidateJSONResult(result, raw map[string]any) error {
	if err := omniValidateToolResult(result, raw); err != nil {
		return err
	}
	var text strings.Builder
	validateCalls := func(calls []any) error {
		for _, v := range calls {
			call, _ := v.(map[string]any)
			function, _ := call["function"].(map[string]any)
			arg, _ := function["arguments"].(string)
			if !json.Valid([]byte(arg)) {
				return fmt.Errorf("incomplete tool arguments")
			}
		}
		return nil
	}
	if choices, ok := result["choices"].([]any); ok {
		if len(choices) == 0 {
			return fmt.Errorf("empty upstream result")
		}
		for _, v := range choices {
			choice, _ := v.(map[string]any)
			message, _ := choice["message"].(map[string]any)
			part, _ := message["content"].(string)
			text.WriteString(part)
			calls, _ := message["tool_calls"].([]any)
			if err := validateCalls(calls); err != nil {
				return err
			}
			if call, ok := message["function_call"].(map[string]any); ok {
				arg, _ := call["arguments"].(string)
				if !json.Valid([]byte(arg)) {
					return fmt.Errorf("incomplete function arguments")
				}
			}
		}
	} else if output, ok := result["output"].([]any); ok {
		if result["status"] != "completed" {
			return fmt.Errorf("incomplete Responses result")
		}
		for _, v := range output {
			item, _ := v.(map[string]any)
			if item["type"] == "function_call" {
				arg, _ := item["arguments"].(string)
				if !json.Valid([]byte(arg)) {
					return fmt.Errorf("incomplete tool arguments")
				}
			}
			content, _ := item["content"].([]any)
			for _, v := range content {
				part, _ := v.(map[string]any)
				s, _ := part["text"].(string)
				text.WriteString(s)
			}
		}
	} else {
		return fmt.Errorf("invalid upstream result")
	}
	format, _ := raw["response_format"].(map[string]any)
	if config, ok := raw["text"].(map[string]any); ok {
		if f, ok := config["format"].(map[string]any); ok {
			format = f
			if f["type"] == "json_schema" {
				format = map[string]any{"type": "json_schema", "json_schema": f}
			}
		}
	}
	if format != nil {
		return omniValidateStructured(text.String(), format)
	}
	return nil
}

// Tool and structured responses are never partially committed to the client.
func omniBufferedRequest(raw map[string]any) bool {
	for _, key := range []string{"tools", "functions", "response_format", "text"} {
		if value, ok := raw[key]; ok && value != nil {
			return true
		}
	}
	return false
}
func omniFrameError(line string) bool {
	if !strings.HasPrefix(line, "data:") {
		return false
	}
	data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if data == "[DONE]" {
		return false
	}
	var frame map[string]any
	if json.Unmarshal([]byte(data), &frame) != nil {
		return true
	}
	if frame["error"] != nil {
		return true
	}
	return frame["type"] == "error" || frame["type"] == "response.failed" || frame["type"] == "response.incomplete"
}
