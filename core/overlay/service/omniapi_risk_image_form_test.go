package service

import (
	"bytes"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"mime/multipart"
	"testing"
)

func TestOmniImageRiskMultipartPreservesFixedPriceProtection(t *testing.T) {
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{ChannelId: 20}, OmniPriceQuote: []byte(`{"costs":{"20":{"per_image_usd":"0.01","complete_prices_verified":true,"risk_fixed_image_upper_verified":true}}}`)}
	for _, n := range []string{"1", "3", "0", "-1", "1000", "x"} {
		var buffer bytes.Buffer
		w := multipart.NewWriter(&buffer)
		w.WriteField("model", "test-image")
		w.WriteField("n", n)
		part, _ := w.CreateFormFile("image[]", "synthetic.png")
		part.Write([]byte("synthetic"))
		w.Close()
		upper, _, err := OmniRiskUpperBound(info, buffer.Bytes(), "7")
		if n == "1" || n == "3" {
			want := int64(70000)
			if n == "3" {
				want *= 3
			}
			if err != nil || upper != want {
				t.Fatal(n, upper, err)
			}
		} else if err == nil {
			t.Fatal("unsafe count accepted", n)
		}
	}
	if _, _, err := OmniRiskUpperBound(info, []byte("--broken\r\n"), "7"); err == nil {
		t.Fatal("truncated form accepted")
	}
	info.OmniPriceQuote = []byte(`{"costs":{"20":{"per_image_usd":"0.01"}}}`)
	if _, _, err := OmniRiskUpperBound(info, []byte(`{"n":1}`), "7"); err == nil {
		t.Fatal("unverified fixed price accepted")
	}
}
