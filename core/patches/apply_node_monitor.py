"""Reproducible observer additions to the attested production gateway."""
import argparse
import pathlib
import shutil

ROOT=pathlib.Path(__file__).resolve().parents[2]

def apply(source):
 for relative in ['pkg/node_monitor/monitor.go','pkg/node_monitor/json_flag.go','middleware/node_monitor.go','controller/node_monitor.go']:
  dest=source/relative;dest.parent.mkdir(parents=True,exist_ok=True)
  shutil.copyfile(ROOT/'core/overlay'/relative,dest)
 for relative,anchor,replacement in [
  ('router/api-router.go','apiRouter := router.Group("/api")','apiRouter := router.Group("/api")\n\tapiRouter.Use(middleware.NodeMonitor())\n\tmonitor := apiRouter.Group("/omni/admin/node-monitor")\n\tmonitor.Use(middleware.RootAuth())\n\tmonitor.GET("/*path", controller.NodeMonitorProxy)\n\tmonitor.POST("/*path", controller.NodeMonitorProxy)\n\trouter.GET("/internal/node-monitor", controller.NodeMonitorInternal)\n\trouter.POST("/internal/node-monitor", controller.NodeMonitorInternal)'),
  ('router/relay-router.go','router.Use(middleware.CORS())','router.Use(middleware.NodeMonitor())\n\trouter.Use(middleware.CORS())'),
  ('service/omniapi_route_log.go','other.SetAdmin("use_channel", chain)','ctx.Set("omni_node_monitor_chain", chain)\n\tother.SetAdmin("use_channel", chain)')]:
  file=source/relative;text=file.read_text(encoding='utf-8')
  marker = 'middleware.NodeMonitor()' if relative.startswith('router/') else 'ctx.Set("omni_node_monitor_chain"'
  if marker not in text:
   assert anchor in text,relative
   file.write_text(text.replace(anchor,replacement,1),encoding='utf-8')
 file=source/'pkg/perf_metrics/metrics.go';text=file.read_text(encoding='utf-8')
 if 'nodemonitor.MarkResult' not in text:
  text=text.replace('"github.com/QuantumNous/new-api/model"','"github.com/QuantumNous/new-api/model"\n nodemonitor "github.com/QuantumNous/new-api/pkg/node_monitor"',1)
  anchor='outcome := ClassifyRelayOutcome(ctx, info, apiErr)'
  replacement=anchor+'''
 monitorOutcome:=string(outcome)
 if outcome==OutcomeIgnored { monitorOutcome="rejected"; if ctx.Err()!=nil { monitorOutcome="cancelled" } }
 var monitorFirst *int64
 if info.IsStream && info.HasSendResponse() { value:=info.FirstResponseTime.Sub(info.StartTime).Milliseconds();monitorFirst=&value }
 nodemonitor.MarkResult(ctx,nodemonitor.Result{Model:info.OriginModelName,Outcome:monitorOutcome,FirstMS:monitorFirst})'''
  assert anchor in text
  file.write_text(text.replace(anchor,replacement,1),encoding='utf-8')
 elif 'nodemonitor.MarkResult(ctx.Request.Context(),' in text:
  file.write_text(text.replace('nodemonitor.MarkResult(ctx.Request.Context(),','nodemonitor.MarkResult(ctx,',1),encoding='utf-8')
 text=file.read_text(encoding='utf-8')
 text=text.replace('if ctx.Err() != nil {\n\t\t\tmonitorOutcome = "cancelled"', '''streamOutcome := info.StreamStatus.OutcomeSnapshot()
        if ctx.Err() == context.Canceled || streamOutcome.Response == relaycommon.ResponseOutcomeCancelled || streamOutcome.EndReason == relaycommon.StreamEndReasonPingFail || streamOutcome.EndReason == relaycommon.StreamEndReasonClientGone {
            monitorOutcome = "cancelled"''',1)
 file.write_text(text,encoding='utf-8')

if __name__=='__main__':
 parser=argparse.ArgumentParser();parser.add_argument('source',type=pathlib.Path);apply(parser.parse_args().source)
