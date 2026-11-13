package openai

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"image"
	"image/png"
	"io"
	"mime"
	"mime/multipart"
	"testing"
)

func TestOmniInlineEditUsesExactMultipartImageAndParameters(t *testing.T) {
	var p bytes.Buffer
	png.Encode(&p, image.NewRGBA(image.Rect(0, 0, 8, 8)))
	raw, _ := json.Marshal("data:image/png;base64," + base64.StdEncoding.EncodeToString(p.Bytes()))
	request := dto.ImageRequest{Model: "exact-image", Prompt: "make green", Size: "1254x1254", Quality: "low", Image: raw}
	wire, contentType, e := omniInlineImageMultipart(&request)
	if e != nil {
		t.Fatal(e)
	}
	_, params, _ := mime.ParseMediaType(contentType)
	reader := multipart.NewReader(bytes.NewReader(wire), params["boundary"])
	form, e := reader.ReadForm(1 << 20)
	if e != nil {
		t.Fatal(e)
	}
	defer form.RemoveAll()
	for k, v := range map[string]string{"model": "exact-image", "prompt": "make green", "size": "1254x1254", "quality": "low", "n": "1", "response_format": "b64_json", "output_format": "png"} {
		if form.Value[k][0] != v {
			t.Fatal(k)
		}
	}
	if len(form.File["image[]"]) != 1 {
		t.Fatal("missing image")
	}
	f, _ := form.File["image[]"][0].Open()
	defer f.Close()
	data, _ := io.ReadAll(f)
	if !bytes.Equal(data, p.Bytes()) {
		t.Fatal("image changed")
	}
}
