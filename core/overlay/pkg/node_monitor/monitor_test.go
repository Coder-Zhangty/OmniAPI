package nodemonitor

import (
 "context"
 "testing"
)

func TestFinishedStreamingFailureAndQueueAck(t *testing.T) {
 queue.Lock();queue.Events=nil;queue.Unlock()
 ctx,state:=Begin(context.Background(),true)
 MarkResult(ctx,Result{Model:"test",Outcome:"failure"})
 if state.Result==nil || state.Result.Outcome!="failure" { t.Fatal("stream result lost") }
 End(true,Event{Outcome:state.Result.Outcome})
 snapshot:=Snapshot()
 if snapshot["model_active"].(int64)!=0 { t.Fatal("active request leaked") }
 events:=snapshot["events"].([]Event)
 if len(events)!=1 { t.Fatal("expected one client request") }
 Ack(events[0].Sequence)
 if len(Snapshot()["events"].([]Event))!=0 { t.Fatal("ack did not remove persisted request") }
}
