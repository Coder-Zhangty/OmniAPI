package model

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"slices"
	"strings"
	"time"
)

//go:embed commerce-launch-models.json
var omniCommerceLaunchModelsJSON string

// Each bound covers the WHOLE provider request, including hidden history,
// decoded remote media and hosted tool work, not just serialized prompt text.
// A provider without a verified hard ceiling cannot receive a certificate.
type OmniCapabilityBound struct {
	InputImages             bool   `json:"input_images,omitempty"`
	MaxQuota                int64  `json:"max_quota"`
	MaxRequestBytes         int64  `json:"max_request_bytes"`
	MaxOutputTokens         int64  `json:"max_output_tokens"`
	ProviderHardCapEvidence string `json:"provider_hard_cap_evidence"`
	AcceptanceHash          string `json:"acceptance_hash"`
	VerifiedAt              int64  `json:"verified_at"`
	LocalControlEvidence    string `json:"local_control_evidence,omitempty"`
	RequestBasedReservation bool   `json:"request_based_reservation,omitempty"`
	InputOverheadTokens     int64  `json:"input_overhead_tokens,omitempty"`
}
type OmniCapabilityMatrix struct {
	ScopeVersion            string                      `json:"scope_version,omitempty"`
	ApprovedModels          []string                    `json:"approved_models,omitempty"`
	SharedScope             string                      `json:"shared_scope,omitempty"`
	ScopeApproval           string                      `json:"scope_approval,omitempty"`
	PricingHash             string                      `json:"pricing_hash"`
	RoutingHash             string                      `json:"routing_hash"`
	AllExistingCapabilities bool                        `json:"all_existing_capabilities"`
	InventoryHash           string                      `json:"inventory_hash"`
	NativeMessageOnly       map[string]int              `json:"native_message_only,omitempty"`
	ScopedChannelPaths      map[string]map[int][]string `json:"scoped_channel_paths,omitempty"`
	// model -> channel -> request URL path -> provider-enforced maximum cost.
	Bounds map[string]map[int]map[string]OmniCapabilityBound `json:"bounds"`
}

func OmniCommerceLaunchModels() []string {
	var out []string
	_ = json.Unmarshal([]byte(omniCommerceLaunchModelsJSON), &out)
	return out
}
func (p OmniCommerceProfile) Capability(modelName string, channel int, path string) (OmniCapabilityBound, error) {
	return p.EffectiveCapabilities().capabilityFromSnapshot(modelName, channel, path)
}

func (p OmniCommerceProfile) capabilityFromSnapshot(modelName string, channel int, path string) (OmniCapabilityBound, error) {
	var m OmniCapabilityMatrix
	if json.Unmarshal([]byte(p.CapabilityMatrix), &m) != nil || m.PricingHash != p.PricingHash || m.RoutingHash != p.RoutingHash {
		return OmniCapabilityBound{}, errors.New("共享请求能力尚未完成核验")
	}
	b, ok := m.Bounds[modelName][channel][path]
	evidenceOK := b.ProviderHardCapEvidence != ""
	if m.SharedScope == "bounded_text" {
		evidenceOK = b.LocalControlEvidence != "" && len(m.ScopeApproval) == 64
	}
	if !ok || b.MaxQuota <= 0 || b.MaxRequestBytes <= 0 || b.MaxOutputTokens <= 0 || !evidenceOK || len(b.AcceptanceHash) != 64 {
		return b, errors.New("该通道和请求能力缺少预留上界验收")
	}
	return b, nil
}
func OmniCommerceLaunchReady(p OmniCommerceProfile) error {
	common.OptionMapRWMutex.RLock()
	pricingRaw := common.OptionMap["OmniAPIPricing"]
	routingRaw := common.OptionMap["OmniAPIRouting"]
	common.OptionMapRWMutex.RUnlock()
	return OmniCommerceLaunchReadySnapshot(p, pricingRaw, routingRaw)
}

func OmniCommerceLaunchReadySnapshot(p OmniCommerceProfile, pricingRaw, routingRaw string) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if p.AuditExpiresAt <= time.Now().Unix() || p.PricingHash != omniCapabilityOptionHash(pricingRaw) || p.RoutingHash != omniCapabilityOptionHash(routingRaw) {
		return ErrOmniCommerceClosed
	}
	var scope map[string][]int
	var matrix OmniCapabilityMatrix
	if json.Unmarshal([]byte(p.Models), &scope) != nil || json.Unmarshal([]byte(p.CapabilityMatrix), &matrix) != nil || len(matrix.InventoryHash) != 64 {
		return errors.New("全部模型能力验收尚未完成")
	}
	restricted := matrix.SharedScope == "bounded_text" && len(matrix.ScopeApproval) == 64 && !matrix.AllExistingCapabilities
	if !matrix.AllExistingCapabilities && !restricted {
		return errors.New("共享能力范围缺少明确批准")
	}
	if matrix.PricingHash != p.PricingHash || matrix.RoutingHash != p.RoutingHash {
		return errors.New("能力验收快照已变化")
	}
	required := OmniCommerceLaunchModels()
	if matrix.ScopeVersion != "" {
		if len(matrix.ApprovedModels) == 0 || len(matrix.ApprovedModels) > 2000 {
			return errors.New("版本化模型范围无效")
		}
		seen := make(map[string]bool)
		for _, name := range matrix.ApprovedModels {
			if name == "" || seen[name] {
				return errors.New("版本化模型范围存在空值或重复项")
			}
			seen[name] = true
		}
		required = matrix.ApprovedModels
	}
	var pricing struct {
		Models map[string]struct {
			BillingUnit  string   `json:"billing_unit"`
			Protocols    []string `json:"protocols"`
			Restrictions []string `json:"restrictions"`
		} `json:"models"`
	}
	if json.Unmarshal([]byte(pricingRaw), &pricing) != nil {
		return errors.New("模型能力目录不可用")
	}
	if len(scope) != len(required) || len(matrix.Bounds) != len(required) {
		return fmt.Errorf("须核齐本批次全部%d个模型", len(required))
	}
	for _, name := range required {
		ids := scope[name]
		if len(ids) == 0 || len(matrix.Bounds[name]) != len(ids) {
			return fmt.Errorf("模型%s的通道能力未齐", name)
		}
		for _, id := range ids {
			row := pricing.Models[name]
			image := row.BillingUnit == "image" || strings.HasPrefix(name, "gpt-image-") || strings.Contains(name, "nano-banana") || name == "gemini-3.1-flash-image-preview"
			// Personal model scope stays intact. Image models are explicitly
			// unavailable to a restricted shared key, before funding selection.
			if restricted && image {
				continue
			}
			if len(matrix.Bounds[name][id]) == 0 {
				return errors.New("缺少通道能力证据")
			}
			paths := []string{"/v1/chat/completions"}
			if image {
				paths = []string{"/v1/images/generations"}
			} else {
				for _, protocol := range row.Protocols {
					if protocol == "claude" {
						paths = append(paths, "/v1/messages")
					}
				}
				if strings.HasPrefix(name, "gpt-") {
					paths = append(paths, "/v1/responses")
				}
				for _, capability := range row.Restrictions {
					if capability == "hosted_web_search_token_priced" {
						paths = append(paths, "/v1/responses")
					}
				}
			}
			for _, path := range paths {
				if path == "/v1/chat/completions" && matrix.NativeMessageOnly[name] == id {
					continue
				}
				if scoped := matrix.ScopedChannelPaths[name][id]; len(scoped) > 0 && !slices.Contains(scoped, path) {
					if !omniCommerceHasPathFallback(p, name, id, path, ids) {
						return fmt.Errorf("%s 的受限通道缺少 %s 备用证书", name, path)
					}
					continue
				}
				if _, err := p.Capability(name, id, path); err != nil {
					return err
				}
			}
			for path, b := range matrix.Bounds[name][id] {
				if b.InputImages {
					quoted := false
					for _, restriction := range row.Restrictions {
						if restriction == "input_images_token_priced" {
							quoted = true
						}
					}
					if !quoted || image {
						return errors.New("共享图片输入缺少独立Token计价依据")
					}
				}
				if len(path) == 0 || path[0] != '/' || b.VerifiedAt > time.Now().Unix() || b.VerifiedAt < time.Now().Unix()-7*86400 {
					return errors.New("能力证据过期或路径无效")
				}
				if _, err := p.Capability(name, id, path); err != nil {
					return err
				}
			}
		}
	}
	for name, id := range matrix.NativeMessageOnly {
		paths := matrix.Bounds[name][id]
		if len(paths) != 1 || !slices.Contains(scope[name], id) {
			return errors.New("原生订阅通道缺少独立能力证书")
		}
		if _, err := p.Capability(name, id, "/v1/messages"); err != nil {
			return err
		}
		hasChatFallback := false
		for _, other := range scope[name] {
			if other == id {
				continue
			}
			if _, err := p.Capability(name, other, "/v1/chat/completions"); err == nil {
				hasChatFallback = true
				break
			}
		}
		if !hasChatFallback {
			return errors.New("原生订阅通道必须保留兼容接口备用线路")
		}
	}
	for name, channels := range matrix.ScopedChannelPaths {
		for id, paths := range channels {
			certificates := matrix.Bounds[name][id]
			if !slices.Contains(scope[name], id) || len(paths) == 0 || len(paths) != len(certificates) {
				return errors.New("受限通道与预留证书范围不一致")
			}
			seen := make(map[string]bool, len(paths))
			for _, path := range paths {
				if seen[path] || (path != "/v1/chat/completions" && path != "/v1/messages" && path != "/v1/responses") {
					return errors.New("受限通道的协议范围无效")
				}
				seen[path] = true
				if _, err := p.capabilityFromSnapshot(name, id, path); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func omniCommerceHasPathFallback(p OmniCommerceProfile, name string, primary int, path string, ids []int) bool {
	for _, id := range ids {
		if id != primary {
			if _, err := p.capabilityFromSnapshot(name, id, path); err == nil {
				return true
			}
		}
	}
	return false
}
