package service

import (
	"encoding/json"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"testing"
)

func TestOmniCoworkPDFQuote(t *testing.T) {
	old := omniPDFRender
	defer func() { omniPDFRender = old }()
	omniPDFRender = func(string) ([]any, error) {
		return []any{map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": omniSyntheticInputPNG(t)}}}, nil
	}
	for _, enabled := range []bool{false, true} {
		var r dto.ClaudeRequest
		raw := `{"model":"claude-opus-5-5","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"original_tool","content":[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0xLjQK"}}]}]}]}`
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			t.Fatal(err)
		}
		quote := `{"restrictions":["input_images_token_priced"]}`
		if enabled {
			quote = `{"restrictions":["input_images_token_priced","input_pdfs_via_images"]}`
		}
		err := OmniValidatePricedRequest(&relaycommon.RelayInfo{Request: &r, OriginModelName: r.Model, OmniPriceQuote: []byte(quote)})
		if (err == nil) != enabled {
			t.Fatalf("enabled=%v err=%v", enabled, err)
		}
		if enabled {
			b, _ := json.Marshal(r)
			var v map[string]any
			json.Unmarshal(b, &v)
			msg := v["messages"].([]any)[0].(map[string]any)["content"].([]any)
			if len(msg) != 1 || msg[0].(map[string]any)["tool_use_id"] != "original_tool" || msg[0].(map[string]any)["content"].([]any)[0].(map[string]any)["type"] != "image" {
				t.Fatal("tool relation or attachment lost")
			}
			meta := r.GetTokenCountMeta()
			if len(meta.Files) != 1 {
				t.Fatal("PDF must enter file metadata, never base64 text")
			}
		}
	}
}
