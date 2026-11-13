package service

import (
 "encoding/json"
 "net/http"
 "strings"
 "sync"
 "time"
 "github.com/QuantumNous/new-api/relaykit/types"
 "github.com/gin-gonic/gin"
)

const omniPrecommitHeartbeatKey="omni_precommit_heartbeat"

// StartOmniPrecommitHeartbeat owns the writer only while the route opens.
// Its caller must stop and join it before the response handler writes content.
func StartOmniPrecommitHeartbeat(c *gin.Context,format types.RelayFormat,interval time.Duration) func(){
 if interval<=0{return func(){}}
 c.Set(omniPrecommitHeartbeatKey,true)
 writer:=c.Writer;ctx:=c.Request.Context();stop:=make(chan struct{});done:=make(chan struct{});var once sync.Once
 go func(){
  defer close(done)
  ticker:=time.NewTicker(interval);defer ticker.Stop()
  for{
   select{
   case <-stop:return
   case <-ctx.Done():return
   case <-ticker.C:
    if ctx.Err()!=nil{return}
    writer.Header().Set("Content-Type","text/event-stream")
    writer.Header().Set("Cache-Control","no-cache")
    writer.Header().Set("X-Accel-Buffering","no")
    writer.WriteHeader(http.StatusOK)
    data:=": ping\n\n"
    if format==types.RelayFormatClaude{data="event: ping\ndata: {\"type\":\"ping\"}\n\n"}
    if _,err:=writer.Write([]byte(data));err!=nil{return}
    writer.Flush()
   }
  }
 }()
 return func(){once.Do(func(){close(stop)});<-done}
}

// A heartbeat commits HTTP 200 but never commits a model result. Final failures
// after it must stay in SSE, without a completion event or a second JSON body.
func OmniRespondPrecommitStreamError(c *gin.Context,format types.RelayFormat,apiErr *types.NewAPIError) bool{
 if !c.GetBool(omniPrecommitHeartbeatKey)||!c.Writer.Written(){return false}
 if c.Request.Context().Err()!=nil{return true}
 var body any=map[string]any{"error":apiErr.ToOpenAIError()}
 event:=""
 if format==types.RelayFormatClaude{
  event="event: error\n";body=map[string]any{"type":"error","error":apiErr.ToClaudeError()}
 }else if strings.Split(c.Request.URL.Path,"?")[0]=="/v1/responses"{
  event="event: error\n";body=map[string]any{"type":"error","error":apiErr.ToOpenAIError()}
 }
 data,_:=json.Marshal(body)
 _,_=c.Writer.Write(append(append([]byte(event+"data: "),data...),[]byte("\n\n")...));c.Writer.Flush()
 return true
}
