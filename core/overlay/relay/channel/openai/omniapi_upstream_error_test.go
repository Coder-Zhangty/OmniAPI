package openai

import (
 "fmt"
 "io"
 "net/http"
 "strings"
 "testing"
)

func TestOmniBilling422FailsOverWithoutLeakingBody(t *testing.T) {
 for _, stream := range []bool{false, true} {
  backupCalls:=0
  c,info:=omniHTTPFixture(t,func(w http.ResponseWriter,r *http.Request){
   w.WriteHeader(422)
   fmt.Fprint(w,`{"error":{"code":"BILLING_INSUFFICIENT_CREDITS","message":"private supplier secret"}}`)
  },func(w http.ResponseWriter,r *http.Request){
   backupCalls++
   if stream { omniSend(w,"backup","recovered",true) } else {
    fmt.Fprint(w,`{"choices":[{"message":{"role":"assistant","content":"recovered"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":2}}`)
   }
  })
  info.IsStream=stream
  resp,err:=(&Adaptor{}).omniRequest(c,info,strings.NewReader(fmt.Sprintf(`{"stream":%t,"messages":[{"role":"user","content":"hello"}]}`,stream)))
  if err!=nil { t.Fatalf("supplier billing 422 stranded request stream=%t: %v",stream,err) }
  data,e:=io.ReadAll(resp.Body);resp.Body.Close()
  if e!=nil||backupCalls!=1||!strings.Contains(string(data),"recovered")||strings.Contains(string(data),"private") {t.Fatalf("invalid failover stream=%t calls=%d err=%v",stream,backupCalls,e)}
 }
}

func TestOmniParameterAndUnknown422NeverRetry(t *testing.T) {
 for _, body:=range []string{`{"error":{"code":"INVALID_IMAGE","message":"bad base64"}}`,`{"error":{"message":"unknown private failure"}}`} {
  calls:=0
  c,info:=omniHTTPFixture(t,func(w http.ResponseWriter,r *http.Request){w.WriteHeader(422);fmt.Fprint(w,body)},func(w http.ResponseWriter,r *http.Request){calls++;omniSend(w,"backup","wrong retry",true)})
  _,err:=(&Adaptor{}).omniRequest(c,info,strings.NewReader(`{"stream":true,"messages":[{"role":"user","content":"hello"}]}`))
  if err==nil||calls!=0 {t.Fatal("invalid or unknown 422 retried",calls,err)}
 }
}
