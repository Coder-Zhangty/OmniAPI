package openai

import (
 "encoding/json"
 "fmt"
 "io"
 "net/http"
 "strings"
 "sync/atomic"
 "testing"
 "github.com/QuantumNous/new-api/common"
 "github.com/QuantumNous/new-api/model"
 "github.com/QuantumNous/new-api/service"
)

func TestOmniToolCompatibilitySkipsDoNotBreakPlainRoute(t *testing.T){
 for _,format:=range []string{"chat","responses"}{t.Run(format,func(t *testing.T){
  var primary,backup atomic.Int32
  send:=func(w http.ResponseWriter,id string){
   if format=="chat"{omniSend(w,id,id,true);return}
   w.Header().Set("Content-Type","text/event-stream")
   fmt.Fprintf(w,"data: {\"type\":\"response.output_text.delta\",\"delta\":%q}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":%q,\"status\":\"completed\",\"output\":[]}}\n\n",id,id)
  }
  c,info:=omniHTTPFixture(t,func(w http.ResponseWriter,r *http.Request){primary.Add(1);send(w,"primary")},func(w http.ResponseWriter,r *http.Request){backup.Add(1);send(w,"backup")})
  policy:=service.GetOmniRouting();policy.FailureThreshold=1;policy.CooldownSeconds=60;ids:=policy.Models[info.OriginModelName];data,_:=json.Marshal(policy);common.OptionMap["OmniAPIRouting"]=string(data)
  registry:=model.OmniModelRegistry{Enabled:true,Version:info.RequestId,Models:map[string]model.OmniModelIdentity{info.OriginModelName:{Version:"test",Capability:"text",Bindings:[]model.OmniModelBinding{
   {ChannelID:ids[0],UpstreamModel:"test-model",Status:"verified",Protocols:[]string{"openai"}},
   {ChannelID:ids[1],UpstreamModel:"test-model",Status:"verified",Protocols:[]string{"openai"},Supports:[]string{"tools"}},
  }}}}
  encoded,_:=json.Marshal(registry);old:=common.OptionMap["OmniAPIModelRegistry"];common.OptionMap["OmniAPIModelRegistry"]=string(encoded);info.OmniRegistryVersion=registry.Version;t.Cleanup(func(){common.OptionMap["OmniAPIModelRegistry"]=old})
  payload:=`{"model":"test-model","stream":true,"messages":[{"role":"user","content":"synthetic"}]}`
  if format=="responses"{c.Request.URL.Path="/v1/responses";info.RequestURLPath="/v1/responses";payload=`{"model":"test-model","stream":true,"input":"synthetic"}`}
  withTool:=strings.TrimSuffix(payload,"}")+`,"tools":[{"type":"function","name":"ping","function":{"name":"ping"}}]}`
  response,err:=(&Adaptor{}).omniRequest(c,info,strings.NewReader(withTool));if err!=nil{t.Fatal(err)};_,err=io.ReadAll(response.Body);response.Body.Close();if err!=nil{t.Fatal(err)}
  if primary.Load()!=0||backup.Load()!=1{t.Fatal("tool request used incompatible primary")}
  info.RequestId+="-plain"
  response,err=(&Adaptor{}).omniRequest(c,info,strings.NewReader(payload));if err!=nil{t.Fatal(err)};_,err=io.ReadAll(response.Body);response.Body.Close();if err!=nil{t.Fatal(err)}
  if primary.Load()!=1||backup.Load()!=1{t.Fatalf("capability skip incorrectly opened primary circuit: primary=%d backup=%d",primary.Load(),backup.Load())}
 })}
}
