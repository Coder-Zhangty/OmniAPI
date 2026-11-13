package openai

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"image"
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type omniGeminiImageAdaptor struct{ *Adaptor }

func (a *omniGeminiImageAdaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	return strings.TrimRight(info.ChannelBaseUrl, "/") + "/v1beta/models/" + url.PathEscape(info.UpstreamModelName) + ":generateContent", nil
}

func (a *Adaptor) omniGeminiImageRequest(c *gin.Context, info *relaycommon.RelayInfo, body io.Reader) (*http.Response, bool, error) {
	imageRequest, ok := info.Request.(*dto.ImageRequest)
	if !ok {
		return nil, false, nil
	}
	native := false
	registry := model.OmniRegistryAt(info.OmniRegistryVersion)
	for _, b := range registry.Models[registry.Canonical(info.OriginModelName)].Bindings {
		if b.ChannelID == info.ChannelId && b.Status == "verified" {
			for _, p := range b.Protocols {
				if p == "gemini-image" {
					native = true
				}
			}
		}
	}
	if !native {
		return nil, false, nil
	}
	scale := map[string]string{"1024x1024": "1K", "2048x2048": "2K", "4096x4096": "4K", "1264x848": "1K", "848x1264": "1K", "1376x768": "1K"}[imageRequest.Size]
	aspect := map[string]string{"1264x848": "3:2", "848x1264": "2:3", "1376x768": "16:9"}[imageRequest.Size]
	if aspect == "" {
		aspect = "1:1"
	}
	if scale == "" || imageRequest.Quality != "default" {
		return nil, true, errors.New("该原生图片规格尚未验收")
	}
	var outbound dto.ImageRequest
	if err := json.NewDecoder(io.LimitReader(body, 25<<20)).Decode(&outbound); err != nil {
		return nil, true, errors.New("无效图片请求")
	}
	parts := []any{}
	if len(outbound.Image) > 0 {
		image, mime, err := dto.OmniDecodeInlineImage(outbound.Image)
		if err != nil {
			return nil, true, err
		}
		parts = append(parts, map[string]any{"inlineData": map[string]string{"mimeType": mime, "data": base64.StdEncoding.EncodeToString(image)}})
	}
	parts = append(parts, map[string]any{"text": outbound.Prompt})
	request := map[string]any{"contents": []any{map[string]any{"role": "user", "parts": parts}}, "generationConfig": map[string]any{"responseModalities": []string{"TEXT", "IMAGE"}, "imageConfig": map[string]string{"aspectRatio": aspect, "imageSize": scale}}}
	encoded, err := json.Marshal(request)
	if err != nil {
		return nil, true, err
	}
	resp, err := channel.DoApiRequest(&omniGeminiImageAdaptor{a}, c, info, bytes.NewReader(encoded))
	if err != nil || resp.StatusCode != 200 {
		return resp, true, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (96<<20)+1))
	if err != nil || len(raw) > 96<<20 {
		return nil, true, errors.New("图片响应不完整或过大")
	}
	data, err := omniNormalizeGeminiImage(raw)
	if err != nil {
		return nil, true, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(data))
	resp.ContentLength = int64(len(data))
	resp.Header.Set("Content-Type", "application/json")
	return resp, true, nil
}

func omniNormalizeGeminiImage(raw []byte) ([]byte, error) {
	type inlineImage struct {
		Data string `json:"data"`
	}
	var body struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					InlineData  *inlineImage `json:"inlineData"`
					InlineSnake *inlineImage `json:"inline_data"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		Usage struct {
			Prompt int `json:"promptTokenCount"`
			Output int `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
	}
	invalid := errors.New("原生图片接口未返回单张完整图片")
	if json.Unmarshal(raw, &body) != nil {
		return nil, invalid
	}
	var images [][]byte
	for _, candidate := range body.Candidates {
		for _, part := range candidate.Content.Parts {
			inline := part.InlineData
			if inline == nil {
				inline = part.InlineSnake
			} else if part.InlineSnake != nil {
				return nil, invalid
			}
			if inline != nil {
				data, err := base64.StdEncoding.DecodeString(inline.Data)
				if err != nil {
					return nil, invalid
				}
				config, format, err := image.DecodeConfig(bytes.NewReader(data))
				if err != nil || config.Width < 1 || config.Height < 1 || config.Width > 4096 || config.Height > 4096 {
					return nil, invalid
				}
				if format != "png" {
					if format != "jpeg" {
						return nil, invalid
					}
					decoded, _, err := image.Decode(bytes.NewReader(data))
					if err != nil {
						return nil, invalid
					}
					var buffer bytes.Buffer
					encoder := png.Encoder{CompressionLevel: png.BestSpeed}
					if encoder.Encode(&buffer, decoded) != nil {
						return nil, invalid
					}
					data = buffer.Bytes()
				}
				images = append(images, data)
			}
		}
	}
	if len(images) != 1 {
		return nil, invalid
	}
	return json.Marshal(map[string]any{"created": time.Now().Unix(), "data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(images[0])}}, "usage": map[string]int{"input_tokens": body.Usage.Prompt, "output_tokens": body.Usage.Output}})
}
