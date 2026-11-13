package dto

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	_ "golang.org/x/image/webp"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"strings"
)

const OmniImageInputMaxBytes = 16 << 20
const OmniImageInputMaxPixels = 20_000_000

// Only caller-owned inline bytes are accepted: never fetch a caller's URL.
func OmniDecodeInlineImage(raw json.RawMessage) ([]byte, string, error) {
	invalid := errors.New("参考图须为完整 PNG、JPEG 或 WebP，文件不超过16MiB，像素不超过2000万")
	var value string
	if len(raw) > 24<<20 || json.Unmarshal(raw, &value) != nil {
		return nil, "", invalid
	}
	header, encoded, ok := strings.Cut(value, ",")
	mime := strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
	if !ok || header != "data:"+mime+";base64" || (mime != "image/png" && mime != "image/jpeg" && mime != "image/webp") {
		return nil, "", invalid
	}
	if base64.StdEncoding.DecodedLen(len(encoded)) > OmniImageInputMaxBytes {
		return nil, "", invalid
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data) == 0 {
		return nil, "", invalid
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width)*int64(cfg.Height) > OmniImageInputMaxPixels || "image/"+format != mime {
		return nil, "", invalid
	}
	if _, _, err = image.Decode(bytes.NewReader(data)); err != nil {
		return nil, "", invalid
	}
	return data, mime, nil
}
