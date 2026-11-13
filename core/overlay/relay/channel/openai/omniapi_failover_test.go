package openai

import (
 "encoding/json"
 "fmt"
 "io"
 "net/http"
 "strings"
 "testing"
 "github.com/QuantumNous/new-api/model"
 "github.com/QuantumNous/new-api/service"
)
func TestOmniContinuationPreservesOriginalCacheUsage(t *testing.T) {
 c,info:=omniHTTPFixture(t,func(w http.ResponseWriter,r *http.Request){
  fmt.Fprint(w,"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":0,\"prompt_tokens_details\":{\"cached_tokens\":40}}}\n\n")
  omniSend(w,"main","already written",false)
 },func(w http.ResponseWriter,r *http.Request){
  fmt.Fprint(w,"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1000,\"completion_tokens\":500,\"prompt_tokens_details\":{\"cached_tokens\":500}}}\n\n")
  omniSend(w,"backup","already written recovered",true)
 })
 resp,err:=(&Adaptor{}).omniRequest(c,info,strings.NewReader(`{"stream":true,"messages":[{"role":"user","content":"hello"}]}`))
 if err!=nil{t.Fatal(err)};defer resp.Body.Close();data,err:=io.ReadAll(resp.Body);if err!=nil{t.Fatal(err)}
 usages:=0
 for _,line:=range strings.Split(string(data),"\n"){if !strings.HasPrefix(line,"data: "){continue};var frame map[string]any;if json.Unmarshal([]byte(strings.TrimPrefix(line,"data: ")),&frame)!=nil{continue};if usage,ok:=frame["usage"].(map[string]any);ok{usages++;detail,_:=usage["prompt_tokens_details"].(map[string]any);if usage["prompt_tokens"]!=float64(100)||detail["cached_tokens"]!=float64(40)||usage["completion_tokens"]==float64(500){t.Fatalf("backup context or original cache billed incorrectly: %v",usage)}}}
 if usages!=1{t.Fatalf("usage frames %d",usages)}
}

func TestOmniAllVerifiedRoutesAreTried(t *testing.T) {
 bad:=func(w http.ResponseWriter,r *http.Request){http.Error(w,"unavailable",503)}
 c,info:=omniHTTPFixture(t,bad,bad,bad,func(w http.ResponseWriter,r *http.Request){omniSend(w,"fourth","recovered",true)})
 resp,err:=(&Adaptor{}).omniRequest(c,info,strings.NewReader(`{"stream":true,"messages":[{"role":"user","content":"hello"}]}`))
 if err!=nil{t.Fatal(err)};defer resp.Body.Close()
 data,err:=io.ReadAll(resp.Body)
 if err!=nil||!strings.Contains(string(data),"recovered")||len(model.OmniRequestAttempts(info.RequestId))!=4{t.Fatal(string(data),err)}
}
func TestOmniErrorEventBeforeOutputRetries(t *testing.T) {
 c,info:=omniHTTPFixture(t,func(w http.ResponseWriter,r *http.Request){fmt.Fprint(w,"data: {\"error\":{\"message\":\"private upstream failure\"}}\n\n")},func(w http.ResponseWriter,r *http.Request){omniSend(w,"backup","answer",true)})
 resp,err:=(&Adaptor{}).omniRequest(c,info,strings.NewReader(`{"stream":true,"messages":[{"role":"user","content":"hello"}]}`))
 if err!=nil{t.Fatal(err)};defer resp.Body.Close();data,err:=io.ReadAll(resp.Body)
 if err!=nil||strings.Contains(string(data),"private")||!strings.Contains(string(data),"answer"){t.Fatal(string(data),err)}
}
func TestOmniContinuationTriesRemainingBackups(t *testing.T) {
 c,info:=omniHTTPFixture(t,func(w http.ResponseWriter,r *http.Request){omniSend(w,"main","already written",false)},func(w http.ResponseWriter,r *http.Request){http.Error(w,"bad",503)},func(w http.ResponseWriter,r *http.Request){omniSend(w,"last","already written recovered",true)})
 resp,err:=(&Adaptor{}).omniRequest(c,info,strings.NewReader(`{"stream":true,"messages":[{"role":"user","content":"hello"}]}`))
 if err!=nil{t.Fatal(err)};defer resp.Body.Close();data,err:=io.ReadAll(resp.Body)
 if err!=nil||strings.Count(string(data),"already written")!=1||!strings.Contains(string(data),"recovered"){t.Fatal(string(data),err)}
}
func TestOmniEmptyContinuationDoesNotFinishEarly(t *testing.T) {
 c,info:=omniHTTPFixture(t,func(w http.ResponseWriter,r *http.Request){omniSend(w,"main","already written",false)},func(w http.ResponseWriter,r *http.Request){omniSend(w,"empty","already written",true)},func(w http.ResponseWriter,r *http.Request){omniSend(w,"last","already written recovered",true)})
 resp,err:=(&Adaptor{}).omniRequest(c,info,strings.NewReader(`{"stream":true,"messages":[{"role":"user","content":"hello"}]}`))
 if err!=nil{t.Fatal(err)};defer resp.Body.Close();data,err:=io.ReadAll(resp.Body)
 if err!=nil||!strings.Contains(string(data),"recovered")||strings.Count(string(data),"[DONE]")!=1||len(model.OmniRequestAttempts(info.RequestId))!=3{t.Fatal(string(data),err)}
}
func TestOmniClaudeNativeBackup(t *testing.T) {
 c,info:=omniHTTPFixture(t,func(w http.ResponseWriter,r *http.Request){http.Error(w,"bad",503)},func(w http.ResponseWriter,r *http.Request){
  if r.URL.Path!="/v1/messages"||r.Header.Get("x-api-key")!="test-key"{t.Error("native Claude transport missing")}
  w.Header().Set("Content-Type","text/event-stream")
  fmt.Fprint(w,`data: {"type":"message_start","message":{"id":"claude-backup","model":"test-model","role":"assistant","content":[],"usage":{"input_tokens":2,"output_tokens":0}}}`+"\n\n")
  fmt.Fprint(w,`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n")
  fmt.Fprint(w,`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"native answer"}}`+"\n\n")
  fmt.Fprint(w,`data: {"type":"content_block_stop","index":0}`+"\n\n")
  fmt.Fprint(w,`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`+"\n\n")
  fmt.Fprint(w,`data: {"type":"message_stop"}`+"\n\n")
 })
 id:=service.GetOmniRouting().Models[info.OriginModelName][1]
 model.DB.Model(&model.Channel{}).Where("id = ?",id).Update("type",14)
 resp,err:=(&Adaptor{}).omniRequest(c,info,strings.NewReader(`{"stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`))
 if err!=nil{t.Fatal(err)};defer resp.Body.Close();data,err:=io.ReadAll(resp.Body)
 if err!=nil||!strings.Contains(string(data),"native answer")||!strings.Contains(string(data),"[DONE]"){t.Fatal(string(data),err)}
}
func TestOmniClaudeNativeResponsesBackup(t *testing.T) {
 c,info:=omniHTTPFixture(t,func(w http.ResponseWriter,r *http.Request){http.Error(w,"bad",503)},func(w http.ResponseWriter,r *http.Request){
  if r.URL.Path!="/v1/messages"{t.Error("Responses was not converted to Claude")}
  fmt.Fprint(w,`{"id":"msg_test","type":"message","role":"assistant","model":"test-model","content":[{"type":"text","text":"native answer"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":2}}`)
 })
 id:=service.GetOmniRouting().Models[info.OriginModelName][1]
 model.DB.Model(&model.Channel{}).Where("id = ?",id).Update("type",14)
 info.IsStream=false;info.RequestURLPath="/v1/responses";c.Request.URL.Path="/v1/responses"
 resp,err:=(&Adaptor{}).omniRequest(c,info,strings.NewReader(`{"model":"test-model","input":"hello","max_output_tokens":100}`))
 if err!=nil{t.Fatal(err)};defer resp.Body.Close();data,err:=io.ReadAll(resp.Body)
 if err!=nil||!strings.Contains(string(data),"native answer")||!strings.Contains(string(data),"output"){t.Fatal(string(data),err)}
}
