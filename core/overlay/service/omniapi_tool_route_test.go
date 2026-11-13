package service

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOmniToolRouteLogRetainsOnlyRoutingMetadata(t *testing.T) {
	var req dto.OpenAIResponsesRequest
	require.NoError(t, json.Unmarshal([]byte(`{
		"tools":[{"type":"namespace","name":"PRIVATE_NAMESPACE","tools":[{"type":"custom","name":"PRIVATE_NAME"}]},
		{"type":"tool_search","execution":"client"}],
		"tool_choice":{"type":"custom","name":"PRIVATE_NAME"},"parallel_tool_calls":false,
		"input":[{"type":"custom_tool_call","call_id":"PRIVATE_ID","input":"PRIVATE_ARGS"},
		{"type":"custom_tool_call_output","call_id":"PRIVATE_ID","output":"PRIVATE_OUTPUT"}]
	}`), &req))
	info := &relaycommon.RelayInfo{Request: &req}
	shape := omniRequestToolRoute(info)
	require.Equal(t, &omniToolRoute{Kinds: []string{"namespace", "custom", "client_tool_search"}, Choice: "named",
		Parallel: "disabled", HasDefinitions: true, HasCallHistory: true, HasResult: true}, shape)
	c := &gin.Context{}
	InitOmniRouteTrace(c)
	RecordOmniReturnedToolCall(c)
	other := model.NewLogOther()
	OmniAppendRouteLog(c, info, other)
	encoded, err := json.Marshal(other)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"tool_route"`)
	require.Contains(t, string(encoded), `"returned_tool_call":true`)
	require.NotContains(t, string(encoded), "PRIVATE_")
}
