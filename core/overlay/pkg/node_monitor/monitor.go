// Package nodemonitor is a bounded, best-effort observer. It never writes billing data.
package nodemonitor

import (
 "context"
 "crypto/rand"
 "encoding/hex"
 "sync"
 "sync/atomic"
 "strconv"
)

type contextKey struct{}
type Result struct {
 Model string `json:"model"`
 Outcome string `json:"outcome"`
 FirstMS *int64 `json:"first_ms"`
}
type State struct { Result *Result }
type Event struct {
 Sequence int64 `json:"sequence"`
 EventID string `json:"event_id"`
 RequestID string `json:"request_id"`
 EdgeID string `json:"edge_id"`
 Timestamp int64 `json:"timestamp"`
 Node string `json:"node"`
 Category string `json:"category"`
 Model string `json:"model"`
 Outcome string `json:"outcome"`
 Status int `json:"status"`
 DurationMS int64 `json:"duration_ms"`
 FirstMS *int64 `json:"first_ms"`
 Attempts int `json:"attempts"`
 Chain []string `json:"chain"`
}
var active atomic.Int64
var models atomic.Int64
var queue = struct { sync.Mutex; Events []Event; Sequence int64; Dropped int64 }{}
var boot = func() string { var id [16]byte; _, _ = rand.Read(id[:]); return hex.EncodeToString(id[:]) }()

func Begin(ctx context.Context, model bool) (context.Context, *State) {
 active.Add(1)
 if model { models.Add(1) }
 state := &State{}
 return context.WithValue(ctx, contextKey{}, state), state
}
func MarkResult(ctx context.Context, result Result) {
 if state, ok := ctx.Value(contextKey{}).(*State); ok { state.Result = &result }
}
func End(model bool, event Event) {
 active.Add(-1)
 if model { models.Add(-1) }
 queue.Lock()
 defer queue.Unlock()
 if len(queue.Events) >= 10000 { queue.Dropped++; return }
 queue.Sequence++
 event.Sequence = queue.Sequence
 event.EventID = boot + ":" + strconv.FormatInt(queue.Sequence,10)
 queue.Events = append(queue.Events, event)
}
func Snapshot() map[string]any {
 queue.Lock()
 defer queue.Unlock()
 count := min(len(queue.Events), 500)
 events := append([]Event{}, queue.Events[:count]...)
 return map[string]any{"boot_id":boot,"active":active.Load(),"model_active":models.Load(),"events":events,"dropped":queue.Dropped}
}
func Ack(sequence int64) {
 queue.Lock()
 defer queue.Unlock()
 n:=0
 for n<len(queue.Events) && queue.Events[n].Sequence<=sequence { n++ }
 queue.Events=append([]Event{},queue.Events[n:]...)
}
