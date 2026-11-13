package service

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"image"
	"image/png"
	"testing"
)

func TestOmniImageEditRequiresExactAcceptedSpecAndInlineInput(t *testing.T) {
	prior := common.OptionMap["OmniAPIModelRegistry"]
	defer func() { common.OptionMap["OmniAPIModelRegistry"] = prior }()
	common.OptionMap["OmniAPIModelRegistry"] = `{"enabled":true,"version":"studio-edit-unit-v1","models":{"studio-image":{"version":"studio-image","capability":"image","bindings":[{"channel_id":20,"upstream_model":"studio-image","protocols":["openai"],"status":"verified","supports":["image_spec:16x16:low","image_edit_spec:16x16:low"]}]}}}`
	var b bytes.Buffer
	png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 8, 8)))
	uri := "data:image/png;base64," + base64.StdEncoding.EncodeToString(b.Bytes())
	for _, tc := range []struct {
		size, quality, input string
		pass                 bool
	}{{"16x16", "low", uri, true}, {"16x16", "high", uri, false}, {"32x32", "low", uri, false}, {"16x16", "low", "https://example.invalid/x.png", false}, {"16x16", "low", "", false}} {
		raw, _ := json.Marshal(tc.input)
		r := dto.ImageRequest{Model: "studio-image", Prompt: "green", Size: tc.size, Quality: tc.quality, Image: raw}
		info := &relaycommon.RelayInfo{OriginModelName: "studio-image", Request: &r, RequestURLPath: "/v1/images/edits", OmniPriceQuote: []byte(`{"image_specs":{"sizes":["32x32"],"qualities":["low","high"]}}`)}
		handled, err := omniValidateImageQuote(info)
		if !handled || (err == nil) != tc.pass {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
}
