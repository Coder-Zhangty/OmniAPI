package model

import (
	"fmt"
	"slices"
)

// Admission failures contain configuration metadata only, never request content.
type OmniBindingRejection struct {
	Reason       string
	Field        string
	BlockType    string
	Tokens       int
	Limit        int
	Missing      []string
	InputTokens  int64
	OutputTokens int64
	KnownLimit   bool
}

func (e *OmniBindingRejection) Error() string {
	switch e.Reason {
	case "context_length_exceeded":
		if e.KnownLimit {
			return fmt.Sprintf("请求预估输入 %d Token＋输出预算 %d Token，超过该接口已确认的上下文上限 %d Token", e.InputTokens, e.OutputTokens, e.Limit)
		}
		return fmt.Sprintf("请求预估输入 %d Token，超过当前同型号线路的平台输入上限 %d Token；请压缩或缩短上下文", e.Tokens, e.Limit)
	case "output_limit_exceeded":
		return fmt.Sprintf("请求输出预算 %d Token，超过该接口已确认的输出上限 %d Token", e.OutputTokens, e.Limit)
	case "input_limit_exceeded":
		return fmt.Sprintf("请求预估输入 %d Token，超过该接口已确认的输入上限 %d Token", e.InputTokens, e.Limit)
	case "request_body_too_large":
		return fmt.Sprintf("请求体超过该接口已确认的上限 %d 字节", e.Limit)
	case "capability_not_verified":
		return fmt.Sprintf("当前同型号线路尚未验证请求所需能力 %v；请调整请求能力或选择已验证型号", e.Missing)
	default:
		return "当前型号暂无已验证且匹配的线路，请联系管理员检查渠道配置"
	}
}

func OmniBindingReject(version, name string, id, tokens int, required []string) *OmniBindingRejection {
	p := OmniRegistryAt(version)
	if !p.Enabled {
		return nil
	}
	row, known := p.Models[p.Canonical(name)]
	if !known {
		return &OmniBindingRejection{Reason: "model_not_registered"}
	}
	for _, b := range row.Bindings {
		if b.ChannelID != id {
			continue
		}
		if b.Status != "verified" {
			return &OmniBindingRejection{Reason: "binding_not_verified"}
		}
		missing := []string{}
		for _, need := range required {
			found := false
			for _, capability := range b.Supports {
				if capability == need {
					found = true
					break
				}
			}
			if !found {
				missing = append(missing, need)
			}
		}
		if len(missing) > 0 {
			return &OmniBindingRejection{Reason: "capability_not_verified", Missing: missing}
		}
		if b.ContextLength > 0 && tokens > b.ContextLength {
			return &OmniBindingRejection{Reason: "context_length_exceeded", Tokens: tokens, Limit: b.ContextLength}
		}
		return nil
	}
	return &OmniBindingRejection{Reason: "channel_not_bound"}
}

// An explicit registry binding takes precedence over the legacy channel type.
func OmniChannelNativeClaude(version, name string, id, channelType int) bool {
	registry := OmniRegistryAt(version)
	if !registry.Enabled {
		return channelType == 14
	}
	for _, binding := range registry.Models[registry.Canonical(name)].Bindings {
		if binding.ChannelID == id && binding.Status == "verified" {
			return slices.Contains(binding.Protocols, "claude") || slices.Contains(binding.Protocols, "anthropic")
		}
	}
	return false
}
