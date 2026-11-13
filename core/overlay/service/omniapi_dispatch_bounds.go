package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"math"
	"strconv"
	"strings"
)

func omniWireOutput(data []byte) (uint64, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var raw map[string]any
	if err := decoder.Decode(&raw); err != nil {
		return 0, err
	}
	output, count := uint64(0), uint64(1)
	scan := func(row map[string]any) error {
		for _, key := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens", "maxOutputTokens", "n", "candidateCount"} {
			value, exists := row[key]
			if !exists || value == nil {
				continue
			}
			number, ok := value.(json.Number)
			if !ok {
				return errors.New("invalid wire output limit")
			}
			n, err := strconv.ParseUint(string(number), 10, 64)
			if err != nil || n == 0 {
				return errors.New("invalid wire output limit")
			}
			if key == "n" || key == "candidateCount" {
				count = max(count, n)
			} else {
				output = max(output, n)
			}
		}
		return nil
	}
	if err := scan(raw); err != nil {
		return 0, err
	}
	if config, ok := raw["generationConfig"].(map[string]any); ok {
		if err := scan(config); err != nil {
			return 0, err
		}
	}
	if output == 0 || count > math.MaxUint64/output {
		return 0, errors.New("missing or overflowing wire output limit")
	}
	return output * count, nil
}

func OmniValidateDispatchBody(info *relaycommon.RelayInfo, body []byte) error {
	if info == nil {
		return nil
	}
	if rejection := OmniDeclaredRequestReject(info, info.GetChannelID()); rejection != nil {
		return rejection
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return err
	}
	if rejection := omniDeclaredWireReject(info, body, raw); rejection != nil {
		return rejection
	}
	if info.RelayFormat == types.RelayFormatClaude && !model.OmniNativeToolChoiceAllows(info.OmniRegistryVersion, info.GetBillingModelName(), info.GetChannelID(), raw["tool_choice"]) {
		return errors.New("当前原生通道不支持强制工具选择，请使用自动模式或兼容通道")
	}
	tools, _ := raw["tools"].([]any)
	required := []string{}
	for _, value := range tools {
		tool, _ := value.(map[string]any)
		kind, _ := tool["type"].(string)
		if strings.HasPrefix(kind, "web_search_") && tool["name"] == "web_search" {
			required = append(required, "native_web_search")
		}
		if strings.HasPrefix(kind, "web_fetch_") && tool["name"] == "web_fetch" {
			required = append(required, "native_web_fetch")
		}
		if strings.HasPrefix(kind, "bash_") {
			required = append(required, "native_bash")
		}
		if strings.HasPrefix(kind, "text_editor_") {
			required = append(required, "native_text_editor")
		}
	}
	if len(required) > 0 {
		if rejection := OmniBindingRequestReject(info, info.GetChannelID(), required); rejection != nil {
			return rejection
		}
	}
	if info.OmniPoolID == "" {
		return nil
	}
	bound, ok := info.OmniSharedDispatchBounds[info.GetChannelID()]
	if !ok || int64(len(body)) > bound.MaxRequestBytes || !omniCertifiedOutputFits(body, bound.MaxOutputTokens) {
		return errors.New("最终发送的请求超过该通道已验收能力，不会改扣个人资金")
	}
	if info.OmniCustomerActual && omniRequestMedia(raw, false) && (!bound.InputImages || !omniSharedFinalImageRequest(info, body, raw)) {
		return errors.New("最终发送请求的通道或图片形态尚未通过共享输入验收")
	}
	if !info.OmniSharedDynamic || info.OmniCustomerActual {
		return nil
	}
	snapshot := info.TieredBillingSnapshot
	if snapshot == nil || info.BillingRequestInput == nil {
		return errors.New("缺少冻结的共享请求报价")
	}
	output, err := omniWireOutput(body)
	if err != nil {
		return err
	}
	input := float64(int64(len(body)+64) + info.OmniSharedInputOverhead)
	cost, trace, err := billingexpr.RunExprByHashWithRequest(snapshot.ExprString, snapshot.ExprHash, billingexpr.TokenParams{P: input * OmniInputReserveFactor(info), C: float64(output), Len: input}, *info.BillingRequestInput)
	if err != nil {
		return err
	}
	quota := cost / 1000000 * common.QuotaPerUnit
	if trace.BillingUnit != billingexpr.BillingUnitRequest {
		quota *= OmniReserveMultiplier(info, snapshot.PreConsumeMultiplier)
	}
	toolsUSD, err := OmniHostedToolReserveUSD(info)
	if err != nil {
		return err
	}
	quota += toolsUSD * common.QuotaPerUnit
	requiredQuota, err := billingexpr.QuotaRoundStrict(quota * snapshot.GroupRatio)
	if err != nil || int64(requiredQuota) > info.OmniSharedUpperQuota {
		return errors.New("最终发送的请求需要更多预留，已停止发送，请调整请求后重试")
	}
	return nil
}
