package service

import (
 "bufio"
 "context"
 "errors"
 "net/http"
 "net/http/httptest"
 "strings"
 "testing"
 "time"

 "github.com/QuantumNous/new-api/relaykit/types"
 "github.com/gin-gonic/gin"
)

func TestOmniPrecommitHeartbeatKeepsClaudeStreamAndEndsWithProtocolError(t *testing.T) {
 release:=make(chan struct{});defer close(release)
 server:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  c,_:=gin.CreateTestContext(w);c.Request=r
  stop:=StartOmniPrecommitHeartbeat(c,types.RelayFormatClaude,20*time.Millisecond)
  select{case <-release:case <-r.Context().Done():stop();return}
  stop()
  apiErr:=types.NewErrorWithStatusCode(errors.New("synthetic supplier failure"),types.ErrorCodeDoRequestFailed,503)
  if !OmniRespondPrecommitStreamError(c,types.RelayFormatClaude,apiErr){c.JSON(503,gin.H{"error":"incorrect JSON response"})}
 }));defer server.Close()
 client:=&http.Client{Timeout:time.Second};resp,err:=client.Get(server.URL)
 if err!=nil{t.Fatal("no heartbeat before model response:",err)}
 defer resp.Body.Close()
 scanner:=bufio.NewScanner(resp.Body);if !scanner.Scan()||scanner.Text()!="event: ping"{t.Fatal("missing Claude ping")}
 // Keep the channel release one-shot, allowing the defer above to own close.
 release<-struct{}{}
 var out strings.Builder
 for scanner.Scan(){out.WriteString(scanner.Text());out.WriteByte('\n')}
 if err=scanner.Err();err!=nil{t.Fatal(err)}
 body:=out.String()
 if resp.StatusCode!=200||!strings.Contains(body,"event: error")||strings.Contains(body,"message_stop")||strings.Contains(body,"incorrect JSON"){t.Fatal(resp.StatusCode,body)}
}

func TestOmniPrecommitHeartbeatStopJoinsBeforeNormalResponse(t *testing.T){
 c,_:=gin.CreateTestContext(httptest.NewRecorder());c.Request=httptest.NewRequest("POST","/v1/messages",nil)
 stop:=StartOmniPrecommitHeartbeat(c,types.RelayFormatClaude,time.Hour);stop();stop()
 if c.Writer.Written(){t.Fatal("fast normal response must retain its HTTP status")}
 if OmniRespondPrecommitStreamError(c,types.RelayFormatClaude,types.NewError(errors.New("bad request"),types.ErrorCodeInvalidRequest)){t.Fatal("unsent response must retain normal JSON error path")}
}

func TestOmniPrecommitStreamErrorOpenAIAndResponses(t *testing.T){
 for _,path:=range []string{"/v1/chat/completions","/v1/responses"}{
  t.Run(path,func(t *testing.T){
   rec:=httptest.NewRecorder();c,_:=gin.CreateTestContext(rec);c.Request=httptest.NewRequest("POST",path,nil)
   c.Set(omniPrecommitHeartbeatKey,true);c.Writer.Header().Set("Content-Type","text/event-stream");c.Writer.Write([]byte(": ping\n\n"))
   err:=types.NewError(errors.New("synthetic timeout"),types.ErrorCodeDoRequestFailed)
   if !OmniRespondPrecommitStreamError(c,types.RelayFormatOpenAI,err){t.Fatal("stream error was not handled")}
   body:=rec.Body.String();if !strings.Contains(body,`"error"`)||strings.Contains(body,"[DONE]")||strings.Contains(body,"response.completed"){t.Fatal(body)}
   if path=="/v1/responses"&&!strings.Contains(body,"event: error"){t.Fatal(body)}
  })
 }
}

func TestOmniPrecommitHeartbeatCancellationHasNoSuccessMarker(t *testing.T){
 rec:=httptest.NewRecorder();c,_:=gin.CreateTestContext(rec);ctx,cancel:=context.WithCancel(context.Background());c.Request=httptest.NewRequest("POST","/v1/messages",nil).WithContext(ctx)
 stop:=StartOmniPrecommitHeartbeat(c,types.RelayFormatClaude,time.Hour);cancel();stop()
 if strings.Contains(rec.Body.String(),"message_stop"){t.Fatal(rec.Body.String())}
}
