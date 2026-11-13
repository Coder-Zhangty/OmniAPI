package openai

import (
	"bytes"
	"encoding/json"
	"github.com/QuantumNous/new-api/relay/channel"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
)

// Browser input remains bounded JSON; the supplier receives its real multipart
// protocol. Validation/selection and ordinary fixed-image settlement run first.
func (a *Adaptor) omniInlineImageEdit(c *gin.Context, info *relaycommon.RelayInfo, body io.Reader) (*http.Response, bool, error) {
	request, ok := info.Request.(*dto.ImageRequest)
	if !ok || !strings.HasSuffix(strings.Split(info.RequestURLPath, "?")[0], "/images/edits") || len(request.Image) == 0 {
		return nil, false, nil
	}
	var outbound dto.ImageRequest
	if err := json.NewDecoder(io.LimitReader(body, 25<<20)).Decode(&outbound); err != nil {
		return nil, true, err
	}
	wire, contentType, err := omniInlineImageMultipart(&outbound)
	if err != nil {
		return nil, true, err
	}
	c.Request.Header.Set("Content-Type", contentType)
	response, err := channel.DoFormRequest(a, c, info, bytes.NewReader(wire))
	return response, true, err
}

func omniInlineImageMultipart(request *dto.ImageRequest) ([]byte, string, error) {
	image, mime, err := dto.OmniDecodeInlineImage(request.Image)
	if err != nil {
		return nil, "", err
	}
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	fields := map[string]string{"model": request.Model, "prompt": request.Prompt, "size": request.Size, "quality": request.Quality, "n": "1", "output_format": "png", "response_format": "b64_json"}
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			return nil, "", err
		}
	}
	extension := strings.TrimPrefix(mime, "image/")
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="image[]"; filename="reference.`+extension+`"`)
	header.Set("Content-Type", mime)
	part, err := writer.CreatePart(header)
	if err != nil {
		return nil, "", err
	}
	if _, err = part.Write(image); err != nil {
		return nil, "", err
	}
	if err = writer.Close(); err != nil {
		return nil, "", err
	}
	return buffer.Bytes(), writer.FormDataContentType(), nil
}
