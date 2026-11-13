package model

import (
    "sync/atomic"
    "testing"
    "time"

    "github.com/QuantumNous/new-api/common"
    "gorm.io/gorm"
)

// A slave must not launch either custom maintenance loop against the shared DB.
func TestOmniSlaveDoesNotStartMaintenance(t *testing.T) {
    omniTestDB(t)
    previous := common.IsMasterNode
    common.IsMasterNode = false
    t.Cleanup(func() { common.IsMasterNode = previous })
    var queries atomic.Int64
    if err := DB.Callback().Query().Before("gorm:query").Register("migration:observe-maintenance", func(_ *gorm.DB) {
        queries.Add(1)
    }); err != nil { t.Fatal(err) }
    StartOmniMaintenance()
    deadline := time.After(time.Second)
    tick := time.NewTicker(10*time.Millisecond)
    defer tick.Stop()
    for {
        select {
        case <-tick.C:
            if count := queries.Load(); count != 0 { t.Fatalf("slave launched maintenance: %d shared database queries", count) }
        case <-deadline:
            return
        }
    }
}
