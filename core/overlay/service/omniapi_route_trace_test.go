package service

import (
	"net/http/httptest"
	"sync"
	"testing"
	"github.com/gin-gonic/gin"
)

func TestOmniRouteTraceCopiesShareBoundedAdminEvidence(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	InitOmniRouteTrace(c)
	child := c.Copy()
	var group sync.WaitGroup
	for i := 0; i < 80; i++ { group.Add(1); go func(){ defer group.Done(); RecordOmniRouteDecision(child, 6, "skipped", "circuit_open") }() }
	group.Wait()
	rows := OmniRouteDecisions(c)
	if len(rows) != 64 || rows[0].ChannelID != 6 { t.Fatalf("trace not shared or bounded: %v", rows) }
	rows[0].Reason = "changed"
	if OmniRouteDecisions(c)[0].Reason != "circuit_open" { t.Fatal("snapshot modified retained evidence") }
}
