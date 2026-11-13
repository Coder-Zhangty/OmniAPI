package middleware

import (
 "net/http"
 "os"
 "strings"
 "time"

 "github.com/QuantumNous/new-api/common"
 nodemonitor "github.com/QuantumNous/new-api/pkg/node_monitor"
 "github.com/gin-gonic/gin"
)

type nodeMonitorWriter struct {
 gin.ResponseWriter
 flag nodemonitor.JSONFlag
}
func (w *nodeMonitorWriter) Write(data []byte) (int,error) {
 if strings.HasPrefix(w.Header().Get("Content-Type"),"application/json") {w.flag.Feed(data)}
 return w.ResponseWriter.Write(data)
}
func (w *nodeMonitorWriter) WriteString(data string) (int,error) {return w.Write([]byte(data))}

func NodeMonitor() gin.HandlerFunc {
 return func(c *gin.Context) {
  path:=c.Request.URL.Path
  if os.Getenv("OMNI_NODE_MONITOR_TOKEN")=="" || strings.HasPrefix(path,"/internal/") || strings.HasPrefix(path,"/api/omni/admin/node-monitor") || path=="/api/status" || path=="/healthz" { c.Next(); return }
  isModel := (c.Request.Method==http.MethodPost && (strings.HasPrefix(path,"/v1/") || strings.HasPrefix(path,"/v1beta/") || strings.HasPrefix(path,"/pg/") || strings.HasPrefix(path,"/mj/") || strings.HasPrefix(path,"/suno/"))) || strings.Contains(path,"/realtime")
  if !isModel && !strings.HasPrefix(path,"/api/") && !strings.HasPrefix(path,"/oauth/") { c.Next();return }
  ctx,state:=nodemonitor.Begin(c.Request.Context(),isModel)
  c.Request=c.Request.WithContext(ctx)
  var writer *nodeMonitorWriter
  if !isModel {writer=&nodeMonitorWriter{ResponseWriter:c.Writer};c.Writer=writer}
  start:=time.Now()
  defer func() {
   category:="management"
   if isModel { category="model" } else if strings.Contains(path,"/user/login") || strings.HasPrefix(path,"/oauth/") || strings.HasPrefix(path,"/api/app/login") { category="login" } else if strings.Contains(path,"/orders") || strings.Contains(path,"/topup") || strings.Contains(path,"/purchase") || strings.Contains(path,"/credit") { category="recharge" } else if strings.Contains(path,"/support") { category="support" } else if strings.Contains(path,"/sync") { category="sync" }
   outcome:="success"
   status:=c.Writer.Status()
   if status>=500 { outcome="failure" } else if status>=400 { outcome="rejected" }
   if writer!=nil && writer.flag.Found && !writer.flag.Success && status<500 {outcome="rejected"}
   if ctx.Err()!=nil { outcome="cancelled" }
   modelName:=c.GetString("original_model")
   var first *int64
   if state.Result!=nil { outcome=state.Result.Outcome; modelName=state.Result.Model; first=state.Result.FirstMS }
   chain:=c.GetStringSlice("omni_node_monitor_chain")
   if len(chain)==0 { chain=c.GetStringSlice("use_channel") }
   nodemonitor.End(isModel,nodemonitor.Event{RequestID:c.GetString(common.RequestIdKey),EdgeID:c.GetHeader("X-Omni-Monitor-ID"),Timestamp:start.UnixMilli(),Node:common.NodeName,Category:category,Model:modelName,Outcome:outcome,Status:status,DurationMS:time.Since(start).Milliseconds(),FirstMS:first,Attempts:len(chain),Chain:chain})
  }()
  c.Next()
 }
}
