package service

import (
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

// The controller calls the guard before getChannel; ChannelMeta is intentionally
// nil until the selected relay helper initializes it. Dispatch guards run again
// with the actual selected channel immediately before contacting the provider.
func TestOmniRiskRequestGuardBeforeChannelSelection(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	for _, pool := range []string{"", "not-dispatched-yet"} {
		info := &relaycommon.RelayInfo{OriginModelName: "gpt-6.1-sol", OmniPoolID: pool}
		require.NotPanics(t, func() { require.NoError(t, OmniRiskRequestGuard(c, info)) })
	}
}
