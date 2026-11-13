package openai

import (
 "bytes"
 "encoding/base64"
 "encoding/json"
 "image"
 "image/png"
 "testing"
 relaycommon "github.com/QuantumNous/new-api/relay/common"
 "github.com/QuantumNous/new-api/relaykit/dto"
)

func TestOmniImageResponseRejectsInvalidOrWrongDimensionsBeforeSettlement(t *testing.T) {
 var buffer bytes.Buffer
 if err:=png.Encode(&buffer,image.NewRGBA(image.Rect(0,0,16,16)));err!=nil {t.Fatal(err)}
 info:=&relaycommon.RelayInfo{Request:&dto.ImageRequest{Size:"16x16"},OmniPriceQuote:[]byte(`{"image_specs":{"size":"16x16"}}`)}
 encode:=func(data []byte)[]byte {b,_:=json.Marshal(map[string]any{"data":[]any{map[string]string{"b64_json":base64.StdEncoding.EncodeToString(data)}}});return b}
 body:=encode(buffer.Bytes())
 if err:=omniValidateImageResponse(info,body);err!=nil {t.Fatal(err)}
 info.Request.(*dto.ImageRequest).Size="4096x4096"
 if omniValidateImageResponse(info,body)==nil {t.Fatal("small image accepted as 4K")}
 info.Request.(*dto.ImageRequest).Size="16x16"
 if omniValidateImageResponse(info,encode(buffer.Bytes()[:buffer.Len()-3]))==nil {t.Fatal("truncated PNG accepted")}
 if omniValidateImageResponse(info,[]byte(`{"data":[]}`))==nil {t.Fatal("empty response accepted")}
}
