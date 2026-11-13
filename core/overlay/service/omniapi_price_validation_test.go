package service

import (
 "encoding/json"
 "testing"
 relaycommon "github.com/QuantumNous/new-api/relay/common"
 "github.com/QuantumNous/new-api/relaykit/dto"
)

func TestOmniPricedTextToolSchemaDoesNotBecomeMedia(t *testing.T) {
 var request dto.GeneralOpenAIRequest
 err:=json.Unmarshal([]byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"describe","parameters":{"type":"object","properties":{"image_url":{"type":"string"},"kind":{"type":"string","enum":["image","audio"]}}}}}]}`),&request)
 if err!=nil{t.Fatal(err)}
 if err:=OmniValidatePricedRequest(&relaycommon.RelayInfo{Request:&request,OriginModelName:request.Model});err!=nil{t.Fatal("text tool schema falsely classified as media:",err)}
}
func TestOmniPricedImageInputUsesVerifiedTokenQuote(t *testing.T) {
 for _,tc:=range []struct{kind,quote string;accepted bool}{
  {"image_url",`{"restrictions":["input_images_token_priced"]}`,true},
  {"image_url",`{"restrictions":["no_media"]}`,false},
  {"input_audio",`{"restrictions":["input_images_token_priced"]}`,false},
 } {
  var request dto.GeneralOpenAIRequest
  body:=`{"model":"claude-opus-5-5","messages":[{"role":"user","content":[{"type":"`+tc.kind+`","`+tc.kind+`":{"url":"data:image/png;base64,`+omniSyntheticInputPNG(t)+`","data":"synthetic","format":"wav"}}]}]}`
  if err:=json.Unmarshal([]byte(body),&request);err!=nil{t.Fatal(err)}
  err:=OmniValidatePricedRequest(&relaycommon.RelayInfo{Request:&request,OriginModelName:request.Model,OmniPriceQuote:[]byte(tc.quote)})
  if (err==nil)!=tc.accepted{t.Fatalf("%s accepted=%t err=%v",tc.kind,tc.accepted,err)}
 }
}
