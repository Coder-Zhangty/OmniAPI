package service

import (
 "encoding/json"
 "net/http/httptest"
 "strings"
 "testing"
 "github.com/QuantumNous/new-api/common"
 "github.com/QuantumNous/new-api/pkg/billingexpr"
 relaycommon "github.com/QuantumNous/new-api/relay/common"
 "github.com/QuantumNous/new-api/relaykit/dto"
 "github.com/gin-gonic/gin"
 "github.com/stretchr/testify/require"
)

func nativeToolFixture(t *testing.T) *relaycommon.RelayInfo {
 t.Helper()
 quote:=map[string]any{"native_client_tools":[]string{"bash"},"hosted_tool_prices":map[string]any{"web_search":map[string]any{"retail_usd_micros":10000,"estimated_cost_usd_micros":5000,"default_max_uses":5,"evidence_hash":strings.Repeat("a",64),"basis":"synthetic test only"}}}
 raw,err:=json.Marshal(quote);require.NoError(t,err)
 return &relaycommon.RelayInfo{OmniNativeToolQuote:raw,OriginModelName:"test",OmniHostedToolUsage:&relaycommon.OmniHostedToolUsage{}}
}

func TestOmniHostedUsageCloneDuplicateAndAttemptIsolation(t *testing.T) {
 c,_:=gin.CreateTestContext(httptest.NewRecorder());info:=nativeToolFixture(t)
 info.OmniNativeToolAttempt=info.OmniHostedToolUsage.Begin();clone:=*info;child:=c.Copy()
 var response dto.ClaudeResponse
 require.NoError(t,json.Unmarshal([]byte(`{"type":"message_delta","usage":{"server_tool_use":{"web_search_requests":2,"web_fetch_requests":1}}}`),&response))
 OmniObserveClaudeHostedUsage(child,&clone,&response);OmniObserveClaudeHostedUsage(child,&clone,&response)
 search,fetch:=info.OmniHostedToolUsage.Counts();require.Equal(t,2,search);require.Equal(t,1,fetch)
 require.Zero(t,c.GetInt("claude_web_search_requests"))
 summary:=&textQuotaSummary{GroupRatio:1}
 require.Equal(t,"10000",calculateTextToolCallSurcharge(c,info,summary).String())
 next:=info.OmniHostedToolUsage.Begin();OmniObserveClaudeHostedUsage(child,&clone,&response)
 search,_=info.OmniHostedToolUsage.Counts();require.Zero(t,search)
 clone.OmniNativeToolAttempt=next;OmniObserveClaudeHostedUsage(child,&clone,&response)
 search,_=info.OmniHostedToolUsage.Counts();require.Equal(t,2,search)
}

func TestOmniHostedQuoteReserveAndNativeAdmission(t *testing.T) {
 info:=nativeToolFixture(t);var request dto.ClaudeRequest
 require.NoError(t,json.Unmarshal([]byte(`{"model":"test","max_tokens":100,"messages":[{"role":"user","content":"hello"}],"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":2}]}`),&request));info.Request=&request
 require.NoError(t,OmniValidatePricedRequest(info));reserve,err:=OmniHostedToolReserveUSD(info);require.NoError(t,err);require.Equal(t,.02,reserve)
 require.False(t,OmniSafeTextRequest(info),"hosted work must not enter text-only shared scope")
 require.NoError(t,json.Unmarshal([]byte(`{"model":"test","max_tokens":100,"messages":[{"role":"user","content":"hello"}],"tools":[{"type":"bash_20250124","name":"bash"}]}`),&request))
 require.NoError(t,OmniValidatePricedRequest(info));require.True(t,OmniSafeTextRequest(info))
}

func TestOmniHostedToolVersionScope(t *testing.T) {
 info:=nativeToolFixture(t)
 var quote map[string]any;require.NoError(t,json.Unmarshal(info.OmniNativeToolQuote,&quote))
 quote["hosted_tool_prices"].(map[string]any)["web_search"].(map[string]any)["accepted_types"]=[]string{"web_search_20250305"}
 info.OmniNativeToolQuote,_=json.Marshal(quote)
 var request dto.ClaudeRequest
 require.NoError(t,json.Unmarshal([]byte(`{"model":"test","max_tokens":100,"messages":[{"role":"user","content":"hello"}],"tools":[{"type":"web_search_20250305","name":"web_search"}]}`),&request));info.Request=&request
 require.NoError(t,OmniValidatePricedRequest(info))
 require.NoError(t,json.Unmarshal([]byte(`{"model":"test","max_tokens":100,"messages":[{"role":"user","content":"hello"}],"tools":[{"type":"web_search_20990101","name":"web_search"}]}`),&request))
 require.Error(t,OmniValidatePricedRequest(info),"one accepted search version must not enable untested hosted execution")
}

func TestOmniHostedResponsesTerminalOnlyAndDuplicateUsage(t *testing.T) {
 info:=&relaycommon.RelayInfo{ResponsesUsageInfo:&relaycommon.ResponsesUsageInfo{BuiltInTools:map[string]*relaycommon.BuildInToolInfo{"web_search":{ToolName:"web_search"}}}}
 a:=NewResponsesUsageAccumulator(info);index:=0;first:=dto.ResponsesOutput{ID:"search1",Type:dto.BuildInCallWebSearchCall}
 event:=&dto.ResponsesStreamResponse{Type:dto.ResponsesOutputTypeItemDone,Item:&first,OutputIndex:&index}
 a.Observe(event);a.Observe(event)
 terminal:=&dto.ResponsesStreamResponse{Type:"response.completed",Response:&dto.OpenAIResponsesResponse{Output:[]dto.ResponsesOutput{first,{ID:"search2",Type:dto.BuildInCallWebSearchCall}}}}
 a.Observe(terminal);a.Observe(terminal);a.Finish();a.Finish()
 require.Equal(t,2,info.ResponsesUsageInfo.BuiltInTools["web_search"].CallCount)
}

func TestOmniSharedWireRequestUsesFrozenBudget(t *testing.T) {
 body:=[]byte(`{"model":"test","input":"hello","max_output_tokens":100}`)
 expression:=`v1:tier("base", p * 10 + c * 20)`
 requestInput:=billingexpr.RequestInput{}
 info:=&relaycommon.RelayInfo{OriginModelName:"test",OmniPoolID:"pool",OmniSharedDynamic:true,OmniPriceProtected:true,OmniBillingActive:true,OmniSharedUpperQuota:10000,ChannelMeta:&relaycommon.ChannelMeta{ChannelId:7},BillingRequestInput:&requestInput,TieredBillingSnapshot:&billingexpr.BillingSnapshot{ExprString:expression,ExprHash:billingexpr.ExprHashString(expression),GroupRatio:1,QuotaPerUnit:common.QuotaPerUnit,PreConsumeMultiplier:1},OmniSharedDispatchBounds:map[int]relaycommon.OmniSharedDispatchBound{7:{MaxRequestBytes:100000,MaxOutputTokens:1024,MaxQuota:50000}}}
 require.NoError(t,OmniValidateDispatchBody(info,body))
 info.OmniSharedUpperQuota=1;require.Error(t,OmniValidateDispatchBody(info,body))
 info.ChannelId=8;require.Error(t,OmniValidateDispatchBody(info,body))
 for _,bad:=range []string{`{"max_tokens":-1}`,`{"max_tokens":18446744073709551615,"n":2}`,`{"max_tokens":100,"n":0}`} {_,err:=omniWireOutput([]byte(bad));require.Error(t,err)}
}
