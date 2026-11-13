package service

import (
 "bytes"
 "encoding/base64"
 "image"
 "image/png"
 "testing"
)

func omniSyntheticInputPNG(t *testing.T) string {
 t.Helper();var b bytes.Buffer
 if err:=png.Encode(&b,image.NewRGBA(image.Rect(0,0,16,16)));err!=nil {t.Fatal(err)}
 return base64.StdEncoding.EncodeToString(b.Bytes())
}

func TestOmniImageInputRejectsBadEncodingAndMismatchedMIME(t *testing.T) {
 data:=omniSyntheticInputPNG(t)
 for _,tc:=range []struct{mime,data string;pass bool}{{"image/png",data,true},{"image/jpeg",data,false},{"image/png","not-base64",false},{"image/png","",false}} {
  err:=omniValidateInputImages(map[string]any{"messages":[]any{map[string]any{"role":"user","content":[]any{map[string]any{"type":"image","source":map[string]any{"type":"base64","media_type":tc.mime,"data":tc.data}}}}}})
  if (err==nil)!=tc.pass {t.Fatal("invalid embedded image decision",tc.mime,tc.pass,err)}
 }
 // The same field names in a function schema are declarations, not image bytes.
 if err:=omniValidateInputImages(map[string]any{"tools":[]any{map[string]any{"image_url":"data:image/png;base64,invalid"}}});err!=nil {t.Fatal("tool schema treated as an image")}
}
