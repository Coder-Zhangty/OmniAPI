package service

import (
	"encoding/json"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"testing"
)

func TestOmniImageQuotedOptionsBoundActualSupplierCall(t *testing.T) {
	quote := []byte(`{"restrictions":["image_generation_1024_low"]}`)
	for _, tc := range []struct {
		body, path string
		pass       bool
	}{
		{`{"model":"gpt-image-2","prompt":"a circle"}`, "/v1/images/generations", true},
		{`{"model":"gpt-image-2","prompt":"a circle","n":1,"quality":"low","size":"1024x1024","output_format":"png"}`, "/v1/images/generations", true},
		{`{"model":"gpt-image-2","prompt":"a circle","n":2}`, "/v1/images/generations", false},
		{`{"model":"gpt-image-2","prompt":"a circle","quality":"high"}`, "/v1/images/generations", false},
		{`{"model":"gpt-image-2","prompt":"a circle","size":"2048x2048"}`, "/v1/images/generations", false},
		{`{"model":"gpt-image-2","prompt":"a circle","stream":true}`, "/v1/images/generations", false},
		{`{"model":"gpt-image-2","prompt":"a circle","background":"transparent"}`, "/v1/images/generations", false},
		{`{"model":"gpt-image-2","prompt":"a circle","image":"https://example.invalid/a.png"}`, "/v1/images/edits", false},
		{`{"model":"gpt-image-2","prompt":"a circle","other_paid_option":true}`, "/v1/images/generations", false},
	} {
		var image dto.ImageRequest
		if err := json.Unmarshal([]byte(tc.body), &image); err != nil {
			t.Fatal(err)
		}
		handled, err := omniValidateImageQuote(&relaycommon.RelayInfo{Request: &image, OriginModelName: "gpt-image-2", RequestURLPath: tc.path, OmniPriceQuote: quote})
		if !handled || (err == nil) != tc.pass {
			t.Fatalf("body=%s pass=%t err=%v", tc.body, tc.pass, err)
		}
		if tc.pass && (image.Size != "1024x1024" || image.Quality != "low" || image.N == nil || *image.N != 1) {
			t.Fatal("defaults failed")
		}
	}
}

func TestOmniImageSpecsRequireAcceptedCombination(t *testing.T) {
 quote:=[]byte(`{"image_specs":{"size":"1024x1024","sizes":["1024x1024","4096x4096"],"qualities":["low","high"],"default_quality":"low","options":[{"size":"1024x1024","quality":"low"},{"size":"4096x4096","quality":"high"}]}}`)
 for _,tc:=range []struct{size,quality string;pass bool}{{"4096x4096","high",true},{"4096x4096","low",false},{"2048x2048","high",false},{"1024x1024","high",false},{"","",true}} {
  image:=dto.ImageRequest{Model:"image-test",Prompt:"a circle",Size:tc.size,Quality:tc.quality}
  handled,err:=omniValidateImageQuote(&relaycommon.RelayInfo{Request:&image,RequestURLPath:"/v1/images/generations",OmniPriceQuote:quote})
  if !handled||(err==nil)!=tc.pass {t.Fatalf("size=%s quality=%s err=%v",tc.size,tc.quality,err)}
  if tc.pass&&tc.size!=""&&image.Size!=tc.size {t.Fatal("silently changed dimensions")}
 }
}

func TestOmniImageQuoteCannotBeUsedAsTextOrUnpricedImages(t *testing.T) {
 text:=&dto.GeneralOpenAIRequest{Model:"image-model"}
 handled,err:=omniValidateImageQuote(&relaycommon.RelayInfo{Request:text,OmniPriceQuote:[]byte(`{"image_specs":{"size":"1024x1024"}}`)})
 if !handled||err==nil {t.Fatal("image quote accepted on text endpoint")}
 image:=&dto.ImageRequest{Model:"text-model",Prompt:"a circle"}
 handled,err=omniValidateImageQuote(&relaycommon.RelayInfo{Request:image,OmniPriceQuote:[]byte(`{"retail":{"input":"1","output":"2"}}`)})
 if !handled||err==nil {t.Fatal("unpriced image generation admitted with token quote")}
}

func TestOmniImageQualityRequiresQuoteAndPreservesSupplierParameter(t *testing.T) {
	for _, quality := range []string{"low", "medium", "high"} {
		for _, enabled := range []bool{false, true} {
			quote := `{"restrictions":["image_generation_1024_low"`
			if enabled {
				quote += `,"image_generation_1024_medium","image_generation_1024_high"`
			}
			quote += `]}`
			image := dto.ImageRequest{Model: "gpt-image-2", Prompt: "a teapot", Quality: quality}
			handled, err := omniValidateImageQuote(&relaycommon.RelayInfo{Request: &image, RequestURLPath: "/v1/images/generations", OmniPriceQuote: []byte(quote)})
			want := quality == "low" || enabled
			if !handled || (err == nil) != want {
				t.Fatalf("quality=%s enabled=%t err=%v", quality, enabled, err)
			}
			if want && image.Quality != quality {
				t.Fatalf("requested quality %s silently changed to %s", quality, image.Quality)
			}
		}
	}
	for _, quality := range []string{"auto", "ultra", "HIGH"} {
		image := dto.ImageRequest{Model: "gpt-image-2", Prompt: "a teapot", Quality: quality}
		_, err := omniValidateImageQuote(&relaycommon.RelayInfo{Request: &image, RequestURLPath: "/v1/images/generations", OmniPriceQuote: []byte(`{"restrictions":["image_generation_1024_low","image_generation_1024_medium","image_generation_1024_high"]}`)})
		if err == nil {
			t.Fatalf("unquoted quality %s admitted", quality)
		}
	}
}
