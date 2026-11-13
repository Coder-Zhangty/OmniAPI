package dto

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func TestOmniInlineImageRejectsRemoteSpoofAndTruncated(t *testing.T) {
	var b bytes.Buffer
	im := image.NewRGBA(image.Rect(0, 0, 16, 16))
	im.Set(1, 1, color.RGBA{255, 0, 0, 255})
	png.Encode(&b, im)
	uri := "data:image/png;base64," + base64.StdEncoding.EncodeToString(b.Bytes())
	raw, _ := json.Marshal(uri)
	got, mime, err := OmniDecodeInlineImage(raw)
	if err != nil || mime != "image/png" || !bytes.Equal(got, b.Bytes()) {
		t.Fatal(err)
	}
	for _, v := range []string{"https://example.com/private.png", "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(b.Bytes()), "data:image/png;base64," + base64.StdEncoding.EncodeToString(b.Bytes()[:30]), "data:image/png;base64,invalid"} {
		r, _ := json.Marshal(v)
		if _, _, e := OmniDecodeInlineImage(r); e == nil {
			t.Fatal("unsafe image accepted", v)
		}
	}
}
