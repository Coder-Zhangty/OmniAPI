package service

import (
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"sync"
)

// Shared by request-local context copies; never retains prompts or provider bodies.
type OmniRouteDecision struct {
	ChannelID    int      `json:"channel_id"`
	Decision     string   `json:"decision"`
	Reason       string   `json:"reason"`
	Field        string   `json:"field,omitempty"`
	BlockType    string   `json:"block_type,omitempty"`
	OutputTokens int64    `json:"declared_output_tokens,omitempty"`
	Limit        int      `json:"limit,omitempty"`
	Missing      []string `json:"missing_capabilities,omitempty"`
}
type omniRouteTrace struct {
	sync.Mutex
	Rows             []OmniRouteDecision
	ReturnedToolCall bool
}

const omniRouteTraceKey = "omni_route_trace"

func InitOmniRouteTrace(c *gin.Context) {
	if c == nil {
		return
	}
	if _, exists := c.Get(omniRouteTraceKey); !exists {
		c.Set(omniRouteTraceKey, &omniRouteTrace{})
	}
}
func RecordOmniRouteDecision(c *gin.Context, id int, decision, reason string) {
	recordOmniRouteRow(c, OmniRouteDecision{ChannelID: id, Decision: decision, Reason: reason})
}

func RecordOmniRouteRejection(c *gin.Context, id int, rejection *model.OmniBindingRejection) {
	if rejection == nil {
		return
	}
	recordOmniRouteRow(c, OmniRouteDecision{ChannelID: id, Decision: "skipped", Reason: rejection.Reason, Field: rejection.Field, BlockType: rejection.BlockType, OutputTokens: rejection.OutputTokens, Limit: rejection.Limit, Missing: rejection.Missing})
}

func recordOmniRouteRow(c *gin.Context, row OmniRouteDecision) {
	if c == nil {
		return
	}
	value, _ := c.Get(omniRouteTraceKey)
	trace, ok := value.(*omniRouteTrace)
	if !ok {
		return
	}
	trace.Lock()
	defer trace.Unlock()
	if len(trace.Rows) < 64 {
		trace.Rows = append(trace.Rows, row)
	}
}
func OmniRouteDecisions(c *gin.Context) []OmniRouteDecision {
	if c == nil {
		return nil
	}
	value, _ := c.Get(omniRouteTraceKey)
	trace, ok := value.(*omniRouteTrace)
	if !ok {
		return nil
	}
	trace.Lock()
	defer trace.Unlock()
	return append([]OmniRouteDecision(nil), trace.Rows...)
}

func RecordOmniReturnedToolCall(c *gin.Context) {
	value, _ := c.Get(omniRouteTraceKey)
	if trace, ok := value.(*omniRouteTrace); ok {
		trace.Lock()
		trace.ReturnedToolCall = true
		trace.Unlock()
	}
}

func omniReturnedToolCall(c *gin.Context) bool {
	value, _ := c.Get(omniRouteTraceKey)
	if trace, ok := value.(*omniRouteTrace); ok {
		trace.Lock()
		defer trace.Unlock()
		return trace.ReturnedToolCall
	}
	return false
}
