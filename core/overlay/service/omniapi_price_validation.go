package service

import (
	"encoding/json"
	"errors"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"slices"
	"strings"
)

func omniMediaPart(value any, allowImages bool) bool {
	switch v := value.(type) {
	case []any:
		for _, item := range v {
			if omniMediaPart(item, allowImages) {
				return true
			}
		}
	case map[string]any:
		kind, _ := v["type"].(string)
		if kind == "image" || kind == "input_image" || kind == "image_url" {
			return !allowImages
		}
		for key, item := range v {
			if kind == "tool_search_output" && key == "tools" {
				continue
			}
			if key == "image_url" {
				if !allowImages {
					return true
				}
				continue
			}
			if key == "input_audio" {
				return true
			}
			if key == "inlineData" || key == "fileData" {
				data, _ := item.(map[string]any)
				mime, _ := data["mimeType"].(string)
				if !allowImages || !strings.HasPrefix(mime, "image/") {
					return true
				}
				continue
			}
			if key == "type" && (kind == "audio" || kind == "input_audio" || kind == "video" || kind == "input_video" || kind == "document" || kind == "input_file") {
				return true
			}
			if omniMediaPart(item, allowImages) {
				return true
			}
		}
	}
	return false
}
func omniUnquotedMedia(value any) bool { return omniRequestMedia(value, false) }
func omniRequestMedia(value any, allowImages bool) bool {
	raw, ok := value.(map[string]any)
	if !ok {
		return omniMediaPart(value, allowImages)
	}
	// Tool definitions and output schemas describe data, not media inputs.
	for _, key := range []string{"messages", "input", "contents", "system", "systemInstruction"} {
		if omniMediaPart(raw[key], allowImages) {
			return true
		}
	}
	return false
}
func omniQuoteRestriction(info *relaycommon.RelayInfo, restriction string) bool {
	data := info.OmniPriceQuote
	if len(data) == 0 {
		data = model.GetOmniPrices().Models[info.OriginModelName]
	}
	var quote struct {
		Restrictions []string `json:"restrictions"`
	}
	return json.Unmarshal(data, &quote) == nil && slices.Contains(quote.Restrictions, restriction)
}
func omniInputImageQuote(info *relaycommon.RelayInfo) bool {
	return omniQuoteRestriction(info, "input_images_token_priced")
}

// These tools execute in the client; namespace children must also be client tools.
func omniResponsesClientTool(info *relaycommon.RelayInfo, tool map[string]any) (bool, error) {
	kind, _ := tool["type"].(string)
	switch kind {
	case "custom":
		if !omniQuoteRestriction(info, "client_custom_tools_token_priced") {
			return true, errors.New("该模型暂未通过客户端自定义工具兼容性验证，请选择支持该工具格式的模型")
		}
		name, _ := tool["name"].(string)
		if strings.TrimSpace(name) == "" {
			return true, errors.New("invalid custom tool name")
		}
		return true, nil
	case "namespace":
		if !omniQuoteRestriction(info, "client_tool_namespaces_token_priced") {
			return true, errors.New("该模型暂未通过客户端工具分组兼容性验证，请选择支持该工具格式的模型")
		}
		name, _ := tool["name"].(string)
		children, ok := tool["tools"].([]any)
		if strings.TrimSpace(name) == "" || !ok || len(children) == 0 {
			return true, errors.New("invalid client tool namespace")
		}
		for _, value := range children {
			child, ok := value.(map[string]any)
			if !ok {
				return true, errors.New("invalid namespace tool")
			}
			childName, _ := child["name"].(string)
			if strings.TrimSpace(childName) == "" {
				return true, errors.New("invalid namespace tool name")
			}
			if child["type"] == "function" {
				continue
			}
			if child["type"] != "custom" {
				return true, errors.New("namespace only supports client function and custom tools")
			}
			allowed, err := omniResponsesClientTool(info, child)
			if err != nil {
				return true, err
			}
			if !allowed {
				return true, errors.New("custom tool quote is not verified")
			}
		}
		return true, nil
	case "tool_search":
		if tool["execution"] != "client" {
			return false, nil
		}
		if !omniQuoteRestriction(info, "client_tool_search_token_priced") {
			return true, errors.New("该模型暂未通过客户端工具搜索兼容性验证，请选择支持该工具格式的模型")
		}
		return true, nil
	}
	return false, nil
}

// Unquoted hosted tools, media and paid service tiers cannot bypass the quote gate.
func OmniValidatePricedRequest(info *relaycommon.RelayInfo) error {
	if info.OmniHostedToolUsage==nil {info.OmniHostedToolUsage=&relaycommon.OmniHostedToolUsage{}}
	if handled, err := omniValidateImageQuote(info); handled {
		return err
	}
	if r, ok := info.Request.(*dto.ClaudeRequest); ok && omniQuoteRestriction(info, "input_pdfs_via_images") {
		if err := omniNormalizeCoworkPDF(r); err != nil {
			return err
		}
	}
	data, err := json.Marshal(info.Request)
	if err != nil {
		return err
	}
	var raw map[string]any
	if err = json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if omniRequestMedia(raw, omniInputImageQuote(info)) {
		return errors.New("该模型尚未支持此图片、音频或文件输入，请使用已支持相应输入的模型")
	}
	if err := omniValidateInputImages(raw); err != nil {
		return err
	}
	if format, ok := raw["response_format"].(map[string]any); ok && format["type"] == "json_schema" {
		definition, _ := format["json_schema"].(map[string]any)
		compiler := jsonschema.NewCompiler()
		compiler.UseLoader(jsonschema.SchemeURLLoader{})
		if compiler.AddResource("https://omniapi.invalid/response-schema", definition["schema"]) != nil {
			return errors.New("invalid requested JSON schema")
		}
		if _, err := compiler.Compile("https://omniapi.invalid/response-schema"); err != nil {
			return errors.New("invalid requested JSON schema or external reference")
		}
	}
	for _, key := range []string{"audio", "modalities", "service_tier", "speed", "cachedContent", "container", "mcp_servers"} {
		if raw[key] != nil {
			return errors.New("此请求包含尚未核验报价的附加功能，暂时无法调用")
		}
	}
	for _, key := range []string{"tools", "functions"} {
		if tools, ok := raw[key].([]any); ok {
			for _, value := range tools {
				tool, ok := value.(map[string]any)
				if !ok {
					return errors.New("invalid tool definition")
				}
				kind, _ := tool["type"].(string)
				if handled,err:=omniNativeClientTool(info,tool);handled { if err!=nil{return err};continue }
				if handled,err:=omniNativeHostedTool(info,tool);handled {
					if err!=nil{return err}
					info.Request.(*dto.ClaudeRequest).Tools=raw["tools"]
					continue
				}
				// Responses custom tools execute in the client. Only models with verified
				// token-only billing may use them; hosted tools keep their quote gate.
				if _, responses := info.Request.(*dto.OpenAIResponsesRequest); responses && key == "tools" {
					if (kind == "web_search" || kind == "web_search_2025_08_26") && omniQuoteRestriction(info, "hosted_web_search_token_priced") {
						// Search is hosted, but this exact supplier product includes its cost
						// in token usage. Route admission requires the matching verified capability.
						continue
					}
					allowed, err := omniResponsesClientTool(info, tool)
					if err != nil {
						return err
					}
					if allowed {
						continue
					}
				}
				if kind != "" && kind != "function" {
					return errors.New("平台工具附加费用待核验，请使用普通函数工具")
				}
				if kind == "" && tool["name"] == nil && tool["functionDeclarations"] == nil && tool["function"] == nil {
					return errors.New("平台工具附加费用待核验，请使用普通函数工具")
				}
			}
		}
	}
	if _, responses := info.Request.(*dto.OpenAIResponsesRequest); responses {
		if input, ok := raw["input"].([]any); ok {
			for _, value := range input {
				item, ok := value.(map[string]any)
				if !ok || item["type"] != "tool_search_output" {
					continue
				}
				if item["execution"] != "client" || !omniQuoteRestriction(info, "client_tool_search_token_priced") {
					return errors.New("该模型暂未通过客户端工具搜索兼容性验证")
				}
				tools, ok := item["tools"].([]any)
				if !ok {
					return errors.New("invalid discovered tools")
				}
				for _, v := range tools {
					tool, ok := v.(map[string]any)
					if !ok {
						return errors.New("invalid discovered tool")
					}
					if tool["type"] == "function" {
						continue
					}
					if tool["type"] != "custom" && tool["type"] != "namespace" {
						return errors.New("discovered tool must execute in client")
					}
					allowed, err := omniResponsesClientTool(info, tool)
					if err != nil {
						return err
					}
					if !allowed {
						return errors.New("discovered tool quote is not verified")
					}
				}
			}
		}
	}
	return nil
}
