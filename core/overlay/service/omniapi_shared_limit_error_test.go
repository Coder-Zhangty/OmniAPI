package service

import (
 "encoding/json"
 "strings"
 "testing"
 "time"
 "github.com/QuantumNous/new-api/model"
 relaycommon "github.com/QuantumNous/new-api/relay/common"
 "github.com/QuantumNous/new-api/relaykit/dto"
 "github.com/stretchr/testify/require"
)

func TestOmniSharedLimitErrorReportsActualBudgetWithoutPrivateData(t *testing.T) {
 p:=model.OmniCommerceProfile{Models:`{"test":[7]}`,PricingHash:"pricing",RoutingHash:"routing"}
 matrix:=model.OmniCapabilityMatrix{SharedScope:"bounded_text",ScopeApproval:strings.Repeat("a",64),PricingHash:p.PricingHash,RoutingHash:p.RoutingHash,Bounds:map[string]map[int]map[string]model.OmniCapabilityBound{"test":{7:{"/v1/responses":{MaxQuota:9000,MaxRequestBytes:4096,MaxOutputTokens:1024,LocalControlEvidence:"synthetic",AcceptanceHash:strings.Repeat("b",64),VerifiedAt:time.Now().Unix()}}}}}
 raw,_:=json.Marshal(matrix);p.CapabilityMatrix=string(raw)
 var request dto.OpenAIResponsesRequest;require.NoError(t,json.Unmarshal([]byte(`{"model":"test","input":"PRIVATE_SYNTHETIC_PROMPT","max_output_tokens":2048}`),&request))
 info:=&relaycommon.RelayInfo{OriginModelName:"test",OmniPoolID:"pool",OmniPriceProtected:true,RequestURLPath:"/v1/responses",Request:&request}
 err:=omniValidateCertifiedShared(info,p);var limit *OmniSharedRequestLimitError;require.ErrorAs(t,err,&limit)
 require.EqualValues(t,2048,limit.OutputTokens);require.Positive(t,limit.RequestBytes)
 require.Contains(t,err.Error(),"1024 Token");require.Contains(t,err.Error(),"2048 Token");require.NotContains(t,err.Error(),"PRIVATE_SYNTHETIC_PROMPT");require.NotContains(t,err.Error(),"channel")
}
