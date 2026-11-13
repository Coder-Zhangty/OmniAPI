package openai

import (
	"encoding/json"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

// Flush the held content once and emit a protocol error without a success marker.
func omniStreamInterrupted(c *gin.Context, info *relaycommon.RelayInfo, last string) {
	if c.Request.Context().Err() != nil {
		return
	}
	if info.RelayFormat != types.RelayFormatOpenAI && last != "" {
		_ = HandleStreamFormat(c, info, last, info.ChannelSetting.ForceFormat, info.ChannelSetting.ThinkingToContent)
	}
	switch info.RelayFormat {
	case types.RelayFormatClaude:
		data, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]any{"type": "api_error", "message": "Upstream stream interrupted; retry the request"}})
		_, _ = c.Writer.Write([]byte("event: error\ndata: " + string(data) + "\n\n"))
		_ = helper.FlushWriter(c)
	case types.RelayFormatGemini:
		_ = helper.ObjectData(c, map[string]any{"error": map[string]any{"code": 502, "status": "UNAVAILABLE", "message": "Upstream stream interrupted; retry the request"}})
	default:
		_ = helper.ObjectData(c, map[string]any{"error": map[string]any{"message": "Upstream stream interrupted; retry the request", "type": "upstream_error", "code": "stream_interrupted"}})
	}
}
