package service

import (
 "encoding/json"
 "math"
 "strings"
 "testing"
 "github.com/QuantumNous/new-api/common"
 "github.com/QuantumNous/new-api/pkg/billingexpr"
 relaycommon "github.com/QuantumNous/new-api/relay/common"
 "github.com/QuantumNous/new-api/relaykit/dto"
 "github.com/stretchr/testify/require"
)

func TestOmniSharedDynamicReserveSurvivesAdapterFraming(t *testing.T) {
 limit:=uint(100);request:=&dto.OpenAIResponsesRequest{Input:json.RawMessage(`"synthetic"`),MaxOutputTokens:&limit}
 expression:=`v1:tier("base", p * 10 + c * 20)`;input:=billingexpr.RequestInput{}
 info:=&relaycommon.RelayInfo{OriginModelName:"test",Request:request,OmniPoolID:"pool",OmniSharedDynamic:true,OmniPriceProtected:true,OmniPriceExpression:expression,OmniSharedInputOverhead:8192,ChannelMeta:&relaycommon.ChannelMeta{ChannelId:7},BillingRequestInput:&input,TieredBillingSnapshot:&billingexpr.BillingSnapshot{ExprString:expression,ExprHash:billingexpr.ExprHashString(expression),GroupRatio:1,QuotaPerUnit:common.QuotaPerUnit,PreConsumeMultiplier:1},OmniSharedDispatchBounds:map[int]relaycommon.OmniSharedDispatchBound{7:{MaxRequestBytes:16384,MaxOutputTokens:1024,MaxQuota:500000}}}
 // The same frozen quote prices the pre-adapter request; the upstream adapter
 // subsequently adds ordinary transport fields. No new funds may be taken.
 tokens:=OmniReservedInputTokens(info,1)
 info.OmniSharedUpperQuota=int64(math.Ceil((float64(tokens)*10+100*20)/1000000*common.QuotaPerUnit))
 raw,_:=json.Marshal(request);var wire map[string]any;require.NoError(t,json.Unmarshal(raw,&wire))
 wire["stream_options"]=map[string]bool{"include_usage":true};wire["model"]="accepted-upstream-model";wire["metadata"]=map[string]string{"transport":strings.Repeat("x",256)}
 body,_:=json.Marshal(wire);require.NoError(t,OmniValidateDispatchBody(info,body),"conversion framing must fit the amount held before conversion")
 held:=info.OmniSharedUpperQuota;wire["metadata"]=map[string]string{"transport":strings.Repeat("x",8192)};body,_=json.Marshal(wire)
 require.Error(t,OmniValidateDispatchBody(info,body),"unbounded adapter expansion must remain blocked");require.Equal(t,held,info.OmniSharedUpperQuota)
}
