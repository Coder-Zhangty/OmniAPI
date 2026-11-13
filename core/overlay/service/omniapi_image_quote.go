package service

import (
	"encoding/json"
	"errors"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"slices"
	"strings"
)

// This quote enables only the accepted single-image protocol, not arbitrary
// upstream media options or hosted image tools on the conversation model.
func omniValidateImageQuote(info *relaycommon.RelayInfo) (bool, error) {
	image, ok := info.Request.(*dto.ImageRequest)
	if !ok {
		var quote struct {
			Specs json.RawMessage `json:"image_specs"`
		}
		data := info.OmniPriceQuote
		if len(data) == 0 {
			data = model.GetOmniPrices().Models[info.OriginModelName]
		}
		if json.Unmarshal(data, &quote) == nil && len(quote.Specs) > 0 && string(quote.Specs) != "null" {
			return true, errors.New("此型号仅支持图片生成接口")
		}
		return false, nil
	}
	var quote struct {
		Specs struct {
			Size           string   `json:"size"`
			Sizes          []string `json:"sizes"`
			Qualities      []string `json:"qualities"`
			DefaultQuality string   `json:"default_quality"`
			Options        []struct {
				Size    string `json:"size"`
				Quality string `json:"quality"`
			} `json:"options"`
		} `json:"image_specs"`
	}
	data := info.OmniPriceQuote
	if len(data) == 0 {
		data = model.GetOmniPrices().Models[info.OriginModelName]
	}
	_ = json.Unmarshal(data, &quote)
	sizes := quote.Specs.Sizes
	if len(sizes) == 0 && quote.Specs.Size != "" {
		sizes = []string{quote.Specs.Size}
	}
	qualities := map[string]bool{}
	for _, quality := range []string{"low", "medium", "high"} {
		if omniQuoteRestriction(info, "image_generation_1024_"+quality) {
			qualities[quality] = true
		}
	}
	if len(sizes) > 0 {
		for _, quality := range quote.Specs.Qualities {
			qualities[quality] = true
		}
	}
	if len(qualities) == 0 {
		if len(data) > 0 {
			return true, errors.New("此型号尚未核验生图报价")
		}
		return false, nil
	}
	if len(image.Extra) > 0 {
		return true, errors.New("此图片请求包含尚未开放的参数")
	}
	editing := strings.HasSuffix(strings.Split(info.RequestURLPath, "?")[0], "/images/edits")
	if !editing && !strings.HasSuffix(strings.Split(info.RequestURLPath, "?")[0], "/images/generations") {
		return true, errors.New("图片请求接口不正确")
	}
	if len(sizes) == 0 {
		sizes = []string{"1024x1024"}
	}
	size := image.Size
	if size == "" {
		size = quote.Specs.Size
		if size == "" {
			size = sizes[0]
		}
	}
	if !editing && !slices.Contains(sizes, size) {
		return true, errors.New("此图片尺寸尚未开放，请选择模型广场列出的尺寸")
	}
	quality := image.Quality
	if quality == "" {
		quality = quote.Specs.DefaultQuality
		if quality == "" {
			quality = "low"
		}
	}
	if !qualities[quality] {
		return true, errors.New("此图片品质尚未开放，请选择模型广场列出的品质")
	}
	if !editing && len(quote.Specs.Options) > 0 {
		allowed := false
		for _, option := range quote.Specs.Options {
			if option.Size == size && option.Quality == quality {
				allowed = true
				break
			}
		}
		if !allowed {
			return true, errors.New("此尺寸与品质组合尚未验收，请选择已开放的组合")
		}
	}
	if editing {
		registry := model.OmniRegistryAt(info.OmniRegistryVersion)
		accepted := false
		for _, b := range registry.Models[registry.Canonical(info.OriginModelName)].Bindings {
			if b.Status == "verified" && slices.Contains(b.Supports, "image_edit_spec:"+size+":"+quality) {
				accepted = true
				break
			}
		}
		if !accepted {
			return true, errors.New("当前模型的此尺寸与品质尚未通过图片编辑验收，请选择支持修改的规格")
		}
		if _, _, err := dto.OmniDecodeInlineImage(image.Image); err != nil {
			return true, err
		}
	}
	if image.N != nil && *image.N != 1 {
		return true, errors.New("图片生成每次支持一张，请设置 n=1")
	}
	if image.Stream != nil && *image.Stream {
		return true, errors.New("此图片模型目前使用非流式图片接口")
	}
	allowed := map[string]bool{"model": true, "prompt": true, "n": true, "size": true, "quality": true, "output_format": true, "response_format": true, "stream": true}
	if editing {
		allowed["image"] = true
	}
	encoded, err := json.Marshal(image)
	if err != nil {
		return true, err
	}
	var fields map[string]any
	if json.Unmarshal(encoded, &fields) != nil {
		return true, errors.New("invalid image request")
	}
	for key := range fields {
		if !allowed[key] {
			return true, errors.New("此图片请求包含尚未开放的参数")
		}
	}
	if format, ok := fields["output_format"]; ok && format != "png" {
		return true, errors.New("图片输出目前支持 PNG 格式")
	}
	if image.ResponseFormat != "" && image.ResponseFormat != "b64_json" {
		return true, errors.New("图片返回目前支持 b64_json 格式")
	}
	if strings.TrimSpace(image.Prompt) == "" || len([]rune(image.Prompt)) > 32000 {
		return true, errors.New("图片描述需要 1–32000 字符")
	}
	one := uint(1)
	image.N = &one
	image.Size = size
	image.Quality = quality
	image.OutputFormat = json.RawMessage(`"png"`)
	return true, nil
}
