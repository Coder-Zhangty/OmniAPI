package openai

import (
 "encoding/json"
 "fmt"
 "io"
 "net/http"
 "strings"
 "sync/atomic"
 "testing"
 "time"

 "github.com/QuantumNous/new-api/common"
 "github.com/QuantumNous/new-api/model"
 "github.com/QuantumNous/new-api/relaykit/dto"
)

func TestOmniNativeToolPauseCompletesWithoutReplay(t *testing.T) {
 var backup atomic.Int32
 c,info:=omniHTTPFixture(t,func(w http.ResponseWriter,r *http.Request){
  w.Header().Set("Content-Type","text/event-stream")
  fmt.Fprint(w,`data: {"type":"message_start","message":{"id":"msg_test","model":"test-model","role":"assistant","content":[],"usage":{"input_tokens":10,"output_tokens":0}}}`+"\n\n")
  fmt.Fprint(w,`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tool_test","name":"ping","input":{}}}`+"\n\n")
  w.(http.Flusher).Flush()
  select { case <-r.Context().Done():return; case <-time.After(1500*time.Millisecond): }
  fmt.Fprint(w,`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"message\":\"OK\"}"}}`+"\n\n")
  fmt.Fprint(w,"data: {\"type\":\"content_block_stop\",\"index\":0}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":10}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
 },func(w http.ResponseWriter,r *http.Request){backup.Add(1);http.Error(w,"must not replay tools",500)})
 p:=omniTestInstallWaitBudget(info.OriginModelName,2,3,4,5)
 ids:=p.Models[info.OriginModelName]
 bindings:=[]model.OmniModelBinding{}
 for _,id:=range ids {model.DB.Model(&model.Channel{}).Where("id = ?",id).Update("type",14);bindings=append(bindings,model.OmniModelBinding{ChannelID:id,Status:"verified",UpstreamModel:info.OriginModelName,Protocols:[]string{"claude"},Supports:[]string{"tools"}})}
 registry:=model.OmniModelRegistry{Enabled:true,Version:info.RequestId,Models:map[string]model.OmniModelIdentity{info.OriginModelName:{Version:"test",Capability:"text",Bindings:bindings}}}
 data,_:=json.Marshal(registry);previous:=common.OptionMap["OmniAPIModelRegistry"];common.OptionMap["OmniAPIModelRegistry"]=string(data);t.Cleanup(func(){common.OptionMap["OmniAPIModelRegistry"]=previous})
 info.OmniRegistryVersion=registry.Version;info.RequestURLPath="/v1/messages";c.Request.URL.Path=info.RequestURLPath;info.OmniNativeClaude=true
 var request dto.ClaudeRequest
 if err:=json.Unmarshal([]byte(`{"model":"test-model","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"Call ping"}],"tools":[{"name":"ping","input_schema":{"type":"object","properties":{"message":{"type":"string"}}}}]}`),&request);err!=nil{t.Fatal(err)}
 info.Request=&request
 resp,err:=OmniNativeClaudeRequest(c,info);if err!=nil{t.Fatal(err)};defer resp.Body.Close()
 body,err:=io.ReadAll(resp.Body)
 if err!=nil||!strings.Contains(string(body),`"message_stop"`)||strings.Contains(string(body),`"type":"error"`)||backup.Load()!=0 {t.Fatalf("valid tool pause interrupted or replayed: err=%v backup=%d body=%s",err,backup.Load(),body)}
}

func TestOmniNewRouteInheritsBoundedWait(t *testing.T){
 c,info:=omniHTTPFixture(t,func(w http.ResponseWriter,r *http.Request){},func(w http.ResponseWriter,r *http.Request){})
 p:=omniTestInstallWaitBudget(info.OriginModelName,15,180,180,240)
 id:=p.Models[info.OriginModelName][0]
 cfg:=p.WaitBudgets[info.OriginModelName];delete(cfg.FirstProgressSecondsByChannel,id);p.WaitBudgets[info.OriginModelName]=cfg
 p.FirstSecondsByModel=map[string]int{info.OriginModelName:180}
 start:=time.Now();budget:=omniAttemptBudgetFor(c,info,map[string]any{},id,p,start)
 if !budget.Enabled()||budget.firstDeadline.Sub(start)>60*time.Second {t.Fatal("new route bypasses bounded first progress and shared precommit limits")}
}
