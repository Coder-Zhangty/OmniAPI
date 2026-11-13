package service

import (
 "bytes"
 "encoding/base64"
 "encoding/json"
 "image"
 "image/png"
 "math/rand"
 "strings"
 "testing"
 "github.com/QuantumNous/new-api/constant"
 relaycommon "github.com/QuantumNous/new-api/relay/common"
 "github.com/QuantumNous/new-api/relaykit/dto"
 "github.com/QuantumNous/new-api/relaykit/types"
 "github.com/gin-gonic/gin"
 "net/http/httptest"
)

func TestOmniClaudeToolResultImageEstimateMatchesDirectImage(t *testing.T){
 img:=image.NewRGBA(image.Rect(0,0,1024,1024));rng:=rand.New(rand.NewSource(42));for i:=range img.Pix{img.Pix[i]=byte(rng.Intn(256))}
 var buffer bytes.Buffer;if err:=png.Encode(&buffer,img);err!=nil{t.Fatal(err)}
 data:=base64.StdEncoding.EncodeToString(buffer.Bytes())
 media:=map[string]any{"type":"image","source":map[string]any{"type":"base64","media_type":"image/png","data":data}}
 var estimates []int
 old:=constant.GetMediaToken;constant.GetMediaToken=false;t.Cleanup(func(){constant.GetMediaToken=old})
 for _,content:=range []any{[]any{media},[]any{map[string]any{"type":"tool_result","tool_use_id":"synthetic","content":[]any{media}}}}{
  body,_:=json.Marshal(map[string]any{"model":"claude-opus-5-5","max_tokens":32,"messages":[]any{map[string]any{"role":"user","content":content}}})
  var request dto.ClaudeRequest;if err:=json.Unmarshal(body,&request);err!=nil{t.Fatal(err)}
  meta:=request.GetTokenCountMeta()
  c,_:=gin.CreateTestContext(httptest.NewRecorder());c.Set(string(constant.ContextKeyOriginalModel),request.Model)
  estimate,err:=CountRequestToken(c,meta,&relaycommon.RelayInfo{RelayFormat:types.RelayFormatClaude,IsStream:true});if err!=nil{t.Fatal(err)}
  t.Logf("files=%d combine_chars=%d estimate=%d",len(meta.Files),len(meta.CombineText),estimate)
  estimates=append(estimates,estimate)
  if strings.Contains(meta.CombineText,data[:128]){t.Errorf("image base64 counted as text; estimated=%d",estimate)}
 }
 if estimates[1]>estimates[0]+100{t.Fatalf("same image inflated inside tool_result: direct=%d nested=%d",estimates[0],estimates[1])}
}
