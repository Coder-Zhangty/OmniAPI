package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"math"
	"net/url"
	"strconv"
	"strings"
)

type OmniSharedRequestLimitError struct {
	RequestBytes int64
	OutputTokens uint64
	Budgets      []string
}

func (e *OmniSharedRequestLimitError) Error() string {
	return fmt.Sprintf("共享请求超出已验收预算：本次请求体 %d 字节，输出预算 %d Token；可用预算为 %s。请调整上下文或输出上限，本次不会转扣个人资金。", e.RequestBytes, e.OutputTokens, strings.Join(e.Budgets, "；"))
}

// Maximum cost is certified for all provider work, including decoded media,
// remote documents, context retrieval and hosted tools. Reserve the maximum
// over every approved retry route before choosing a funding source.
func omniValidateCertifiedShared(info *relaycommon.RelayInfo, p model.OmniCommerceProfile) error {
	var matrix model.OmniCapabilityMatrix
	if json.Unmarshal([]byte(p.CapabilityMatrix), &matrix) != nil {
		return errors.New("共享能力范围损坏")
	}
	imageInput := false
	if matrix.SharedScope == "bounded_text" && !OmniSafeTextRequest(info) {
		imageInput = omniSharedInlineImageRequest(info)
		if !imageInput {
			return errors.New("共享池当前仅开放基础文本和普通函数工具；媒体、托管工具及服务端历史尚未开放，本次不会转扣个人资金")
		}
	}
	if matrix.SharedScope == "bounded_text" && OmniReservedOutput(info) <= 0 {
		return errors.New("共享文本请求必须保留明确的输出上限")
	}
	data, err := json.Marshal(info.Request)
	if err != nil {
		return err
	}
	var routes map[string][]int
	if json.Unmarshal([]byte(p.Models), &routes) != nil {
		return errors.New("核价范围损坏")
	}
	parsed, err := url.Parse(info.RequestURLPath)
	if err != nil || parsed.Path == "" {
		return errors.New("请求路径不可核验")
	}
	upper := int64(0)
	budgets := []string{}
	dynamic := matrix.SharedScope == "bounded_text" && info.OmniPriceProtected && info.OmniPriceExpression != ""
	info.OmniSharedInputOverhead = 0
	info.OmniSharedDispatchBounds = make(map[int]relaycommon.OmniSharedDispatchBound)
	for _, id := range routes[info.GetBillingModelName()] {
		bound, err := p.Capability(info.GetBillingModelName(), id, parsed.Path)
		if err != nil {
			continue
		}
		if dynamic && bound.RequestBasedReservation && !imageInput {
			output, err := omniWireOutput(data)
			if err != nil || output > math.MaxInt64 {
				return errors.New("共享请求的输出预算无效或溢出")
			}
			// Each certificate prices the whole envelope at its frozen worst
			// rate. Scale that financial ceiling for this request, then hold
			// its actual frozen quote before dispatch. Never alter the order.
			requestBytes := int64(len(data)) + 4096
			scale := max((requestBytes-1)/bound.MaxRequestBytes+1, (int64(output)-1)/bound.MaxOutputTokens+1)
			if scale > math.MaxInt64/bound.MaxQuota {
				return errors.New("共享请求所需资金预留溢出")
			}
			bound.MaxQuota *= scale
			bound.MaxRequestBytes = max(bound.MaxRequestBytes, requestBytes)
			bound.MaxOutputTokens = max(bound.MaxOutputTokens, int64(output))
		}
		if imageInput && (!bound.InputImages || model.OmniBindingReject(info.OmniRegistryVersion, info.GetBillingModelName(), id, info.GetEstimatePromptTokens(), []string{"vision"}) != nil) {
			continue
		}
		label := fmt.Sprintf("请求体 ≤%d 字节 / 输出 ≤%d Token", bound.MaxRequestBytes, bound.MaxOutputTokens)
		found := false
		for _, existing := range budgets {
			if existing == label {
				found = true
				break
			}
		}
		if !found {
			budgets = append(budgets, label)
		}
		if int64(len(data)) > bound.MaxRequestBytes || !omniCertifiedOutputFits(data, bound.MaxOutputTokens) {
			continue
		}
		upper = max(upper, bound.MaxQuota)
		dynamic = dynamic && bound.RequestBasedReservation
		if bound.InputOverheadTokens < 0 || bound.InputOverheadTokens > 1000000 {
			return errors.New("共享输入附加预留无效")
		}
		info.OmniSharedInputOverhead = max(info.OmniSharedInputOverhead, bound.InputOverheadTokens)
		info.OmniSharedDispatchBounds[id] = relaycommon.OmniSharedDispatchBound{MaxRequestBytes: bound.MaxRequestBytes, MaxOutputTokens: bound.MaxOutputTokens, MaxQuota: bound.MaxQuota, InputImages: bound.InputImages || matrix.SharedScope != "bounded_text"}
	}
	if upper <= 0 || upper > int64(^uint(0)>>1) {
		if len(budgets) > 0 {
			output, _ := omniWireOutput(data)
			failure := &OmniSharedRequestLimitError{RequestBytes: int64(len(data)), OutputTokens: output, Budgets: budgets}
			// Operational dimensions only; never log a prompt, tool argument,
			// response, key, wallet balance or raw request.
			common.SysLog(fmt.Sprintf("shared_request_limit request_id=%q user_id=%d model=%q path=%q bytes=%d output_budget=%d", info.RequestId, info.UserId, info.GetBillingModelName(), parsed.Path, len(data), output))
			return failure
		}
		return errors.New("没有能承载此请求大小与输出预算的已验收共享通道")
	}
	info.OmniSharedUpperQuota = upper
	// Text-only requests have no hidden media or hosted tool work. Their exact
	// frozen retail expression can reserve the request, rather than an entire
	// maximum-size request. The certificate remains an absolute ceiling.
	info.OmniSharedDynamic = dynamic
	return nil
}

func omniCertifiedOutputFits(data []byte, limit int64) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var raw map[string]any
	if decoder.Decode(&raw) != nil || limit <= 0 {
		return false
	}
	output, count := uint64(0), uint64(1)
	var scan func(map[string]any) bool
	scan = func(row map[string]any) bool {
		for _, key := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens", "maxOutputTokens"} {
			if v, ok := row[key]; ok && v != nil {
				n, ok := v.(json.Number)
				if !ok {
					return false
				}
				value, err := strconv.ParseUint(string(n), 10, 64)
				if err != nil || value > uint64(limit) {
					return false
				}
				output = max(output, value)
			}
		}
		for _, key := range []string{"n", "candidateCount"} {
			if v, ok := row[key]; ok && v != nil {
				n, ok := v.(json.Number)
				if !ok {
					return false
				}
				value, err := strconv.ParseUint(string(n), 10, 64)
				if err != nil || value > uint64(limit) {
					return false
				}
				count = max(count, value)
			}
		}
		return true
	}
	if !scan(raw) {
		return false
	}
	if config, ok := raw["generationConfig"].(map[string]any); ok && !scan(config) {
		return false
	}
	return output == 0 || count <= uint64(limit)/output
}
