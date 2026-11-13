package claude

import (
 "net/http/httptest"
 "strings"
 "testing"
 relaycommon "github.com/QuantumNous/new-api/relay/common"
 "github.com/QuantumNous/new-api/relaykit/dto"
 "github.com/QuantumNous/new-api/relaykit/types"
 "github.com/gin-gonic/gin"
 "github.com/stretchr/testify/require"
 "github.com/tidwall/gjson"
)
func TestOmniNativeClaudeKeepsHostedStateAndUsage(t *testing.T) {
 recorder:=httptest.NewRecorder();c,_:=gin.CreateTestContext(recorder);c.Request=httptest.NewRequest("POST","/v1/messages",nil)
 info:=&relaycommon.RelayInfo{ChannelMeta:&relaycommon.ChannelMeta{},RelayFormat:types.RelayFormatClaude,OriginModelName:"test",StreamStatus:relaycommon.NewStreamStatus(),OmniHostedToolUsage:&relaycommon.OmniHostedToolUsage{}}
 info.OmniNativeToolAttempt=info.OmniHostedToolUsage.Begin()
 usage:=&ClaudeResponseInfo{Usage:&dto.Usage{}}
 frames:=[]string{
  `{"type":"message_start","message":{"id":"msg_test","model":"test","role":"assistant","content":[],"usage":{"input_tokens":10,"output_tokens":0}}}`,
  `{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtool_test","name":"web_search","input":{"query":"synthetic"}}}`,
  `{"type":"content_block_stop","index":0}`,
  `{"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtool_test","content":[{"type":"web_search_result","url":"https://example.invalid","title":"Synthetic","encrypted_content":"OPAQUE_SYNTHETIC_STATE"}]}}`,
  `{"type":"content_block_stop","index":1}`,
  `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4,"server_tool_use":{"web_search_requests":1,"web_fetch_requests":0},"future_provider_dimension":{"preserved":true}}}`,
  `{"type":"message_stop"}`,
 }
 for _,frame:=range frames {require.Nil(t,HandleStreamResponseData(c,info,usage,frame))}
 raw:=recorder.Body.String();require.Contains(t,raw,`"type":"server_tool_use"`);require.Contains(t,raw,`"type":"web_search_tool_result"`);require.Contains(t,raw,"OPAQUE_SYNTHETIC_STATE")
 found:=false
 for _,line:=range strings.Split(raw,"\n") {if !strings.HasPrefix(line,"data: "){continue};value:=strings.TrimPrefix(line,"data: ");if gjson.Get(value,"type").String()=="message_delta" {found=true;require.EqualValues(t,1,gjson.Get(value,"usage.server_tool_use.web_search_requests").Int());require.True(t,gjson.Get(value,"usage.future_provider_dimension.preserved").Bool())}}
 require.True(t,found);search,_:=info.OmniHostedToolUsage.Counts();require.Equal(t,1,search)
}
