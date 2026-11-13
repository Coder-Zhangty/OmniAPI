package openai

import (
 "bytes"
 "encoding/base64"
 "encoding/json"
 "image"
 "image/jpeg"
 "image/png"
 "strings"
 "testing"
)

func TestOmniGeminiImageNormalizationKeepsRealDimensionsAndReturnsPNG(t *testing.T) {
 var buffer bytes.Buffer
 if err:=jpeg.Encode(&buffer,image.NewRGBA(image.Rect(0,0,64,32)),nil);err!=nil {t.Fatal(err)}
 raw,_:=json.Marshal(map[string]any{"candidates":[]any{map[string]any{"content":map[string]any{"parts":[]any{map[string]any{"inlineData":map[string]string{"mimeType":"image/jpeg","data":base64.StdEncoding.EncodeToString(buffer.Bytes())}}}}}}})
 body,err:=omniNormalizeGeminiImage(raw);if err!=nil {t.Fatal(err)}
 var output struct {Data []struct {Base64 string `json:"b64_json"`} `json:"data"`}
 if json.Unmarshal(body,&output)!=nil||len(output.Data)!=1 {t.Fatal("invalid image response")}
 data,err:=base64.StdEncoding.DecodeString(output.Data[0].Base64);if err!=nil {t.Fatal(err)}
 config,err:=png.DecodeConfig(bytes.NewReader(data));if err!=nil||config.Width!=64||config.Height!=32 {t.Fatal("dimensions or PNG format changed",err)}
 snake:=[]byte(strings.ReplaceAll(string(raw),"inlineData","inline_data"))
 normalized,err:=omniNormalizeGeminiImage(snake)
 if err!=nil||!bytes.Equal(normalized,body) {t.Fatal("supplier snake_case JPEG envelope changed pixels or failed",err)}
 if _,err:=omniNormalizeGeminiImage([]byte(`{"candidates":[]}`));err==nil {t.Fatal("empty native response accepted")}
 enriched:=bytes.Replace(raw,[]byte(`"candidates":`),[]byte(`"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":2,"promptTokensDetails":[{"modality":"TEXT","tokenCount":4}]},"candidates":`),1)
 if _,err:=omniNormalizeGeminiImage(enriched);err!=nil {t.Fatal("valid rich Gemini usage metadata rejected",err)}
}
