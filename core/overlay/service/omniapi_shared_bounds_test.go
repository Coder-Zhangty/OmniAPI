package service

import (
	"encoding/json"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestOmniSharedUnboundedRequestCannotFallback(t *testing.T) {
	for _, raw := range []string{`{"model":"test","previous_response_id":"opaque-server-context"}`, `{"model":"test","conversation":"hidden-history"}`, `{"model":"test","tools":[{"type":"web_search"}]}`} {
		var request dto.OpenAIResponsesRequest
		require.NoError(t, json.Unmarshal([]byte(raw), &request))
		require.Error(t, OmniValidateSharedReservation(&relaycommon.RelayInfo{OmniPoolID: "pool", Request: &request}))
	}
	var request dto.OpenAIResponsesRequest
	require.NoError(t, json.Unmarshal([]byte(`{"model":"test","input":"hello","max_output_tokens":100}`), &request))
	require.NoError(t, OmniValidateSharedReservation(&relaycommon.RelayInfo{OmniPoolID: "pool", Request: &request}))
}
