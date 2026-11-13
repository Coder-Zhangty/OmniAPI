package model

import (
	"encoding/json"
	"github.com/QuantumNous/new-api/common"
)

type OmniPriceSnapshot struct {
	RiskFX  json.RawMessage            `json:"cny_per_usd"`
	Enabled bool                       `json:"enabled"`
	Version string                     `json:"version"`
	Models  map[string]json.RawMessage `json:"models"`
	Waiting map[string]string          `json:"waiting"`
	Quotes  map[string]json.RawMessage `json:"pricing_quotes"`
}

func GetOmniPrices() OmniPriceSnapshot {
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap["OmniAPIPricing"]
	common.OptionMapRWMutex.RUnlock()
	return OmniPricesFromSnapshot(raw)
}

func OmniPricesFromSnapshot(raw string) OmniPriceSnapshot {
	var p OmniPriceSnapshot
	if common.Unmarshal([]byte(raw), &p) != nil {
		return OmniPriceSnapshot{}
	}
	return p
}
func OmniPriceAvailability(name string) (string, string, string) {
	name = OmniCanonicalModel(name)
	p := GetOmniPrices()
	state:=GetOmniModelRegistry().Models[name].Lifecycle
	if state=="retired" {return "paused","该模型已退役，请选择其他可用型号",OmniPublicPriceVersion(p.Version)}
	if state=="unavailable" {return "paused","该模型的通道暂不可用，恢复后重新开放",OmniPublicPriceVersion(p.Version)}
	if !p.Enabled {
		return "", "", ""
	}
	if _, ok := p.Models[name]; ok {
		return "available", "", OmniPublicPriceVersion(p.Version)
	}
	return "paused", OmniPausedReason(name), OmniPublicPriceVersion(p.Version)
}
