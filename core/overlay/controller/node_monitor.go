package controller

import (
 "bytes"
 "crypto/subtle"
 "io"
 "net/http"
 "net/url"
 "os"
 "strconv"
 "strings"
 "time"

 nodemonitor "github.com/QuantumNous/new-api/pkg/node_monitor"
 "github.com/gin-gonic/gin"
)

func NodeMonitorInternal(c *gin.Context) {
 token:=os.Getenv("OMNI_NODE_MONITOR_TOKEN")
 if len(token)<32 || subtle.ConstantTimeCompare([]byte(c.GetHeader("Authorization")),[]byte("Bearer "+token))!=1 { c.AbortWithStatus(http.StatusForbidden); return }
 c.Header("Cache-Control","no-store")
 if c.Request.Method==http.MethodPost {
  var body struct { Sequence int64 `json:"sequence"` }
  if c.ShouldBindJSON(&body)!=nil || body.Sequence<0 { c.AbortWithStatus(http.StatusBadRequest); return }
  nodemonitor.Ack(body.Sequence)
 }
 c.JSON(http.StatusOK,gin.H{"success":true,"data":nodemonitor.Snapshot()})
}

// RootAuth executes before this proxy. Caller headers and credentials never reach the daemon.
func NodeMonitorProxy(c *gin.Context) {
 base:=os.Getenv("OMNI_NODE_MONITOR_URL")
 token:=os.Getenv("OMNI_NODE_MONITOR_ADMIN_TOKEN")
 if base=="" || len(token)<32 { c.JSON(503,gin.H{"success":false,"message":"Node monitor unavailable"}); return }
 suffix:=strings.Trim(c.Param("path"),"/")
 allowed:=suffix=="snapshot" || suffix=="series" || suffix=="requests" || suffix=="events" || suffix=="operations" || strings.HasPrefix(suffix,"operations/") && !strings.Contains(strings.TrimPrefix(suffix,"operations/"),"/")
 if !allowed || (c.Request.Method!=http.MethodGet && !(c.Request.Method==http.MethodPost && suffix=="operations")) { c.AbortWithStatus(404); return }
 if c.Request.Method==http.MethodPost {
  origin:=c.GetHeader("Origin")
  if origin!="" && origin!="https://omniapi.biz" && origin!="https://www.omniapi.biz" { c.AbortWithStatus(403); return }
  if c.GetHeader("X-Omni-Operation")!="1" { c.AbortWithStatus(403); return }
 }
 body,err:=io.ReadAll(http.MaxBytesReader(c.Writer,c.Request.Body,8192))
 if err!=nil { c.AbortWithStatus(413);return }
 request,err:=http.NewRequestWithContext(c.Request.Context(),c.Request.Method,strings.TrimRight(base,"/")+"/admin/"+suffix+"?"+url.Values(c.Request.URL.Query()).Encode(),bytes.NewReader(body))
 if err!=nil { c.AbortWithStatus(503); return }
 request.Header.Set("Authorization","Bearer "+token)
 request.Header.Set("Content-Type","application/json")
 request.Header.Set("X-Omni-Actor-ID",strconv.Itoa(c.GetInt("id")))
 client:=http.Client{Timeout:8*time.Second,CheckRedirect:func(_ *http.Request,_ []*http.Request)error{return http.ErrUseLastResponse}}
 response,err:=client.Do(request)
 if err!=nil { c.JSON(503,gin.H{"success":false,"message":"Node monitor unavailable"}); return }
 defer response.Body.Close()
 data,err:=io.ReadAll(io.LimitReader(response.Body,4<<20))
 if err!=nil { c.AbortWithStatus(503); return }
 c.Header("Cache-Control","no-store")
 c.Data(response.StatusCode,"application/json",data)
}
