package service
import("encoding/json";"strings";"testing";"time";"github.com/QuantumNous/new-api/model";relaycommon "github.com/QuantumNous/new-api/relay/common";"github.com/QuantumNous/new-api/relaykit/dto";"github.com/stretchr/testify/require")
func TestOmniSharedCertifiedHistoryMediaToolsAndAllRetryRoutes(t *testing.T){
 p:=model.OmniCommerceProfile{Models:`{"test":[7,8]}`,PricingHash:"pricing",RoutingHash:"routing"};matrix:=model.OmniCapabilityMatrix{PricingHash:p.PricingHash,RoutingHash:p.RoutingHash,Bounds:map[string]map[int]map[string]model.OmniCapabilityBound{"test":{}}}
 for _,cid:=range []int{7,8}{matrix.Bounds["test"][cid]=map[string]model.OmniCapabilityBound{"/v1/responses":{MaxQuota:int64(cid)*1000,MaxRequestBytes:50000,MaxOutputTokens:1024,ProviderHardCapEvidence:"synthetic provider hard total ceiling including history/media/tools",AcceptanceHash:strings.Repeat("1",64),VerifiedAt:time.Now().Unix()}}}
 raw,_:=json.Marshal(matrix);p.CapabilityMatrix=string(raw)
 for _,body:=range []string{`{"model":"test","previous_response_id":"history","input":"hello","max_output_tokens":10}`,`{"model":"test","input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.invalid/large.png"}]}],"max_output_tokens":10}`,`{"model":"test","tools":[{"type":"web_search"}],"max_tool_calls":3,"max_output_tokens":10}`} {
  var request dto.OpenAIResponsesRequest;require.NoError(t,json.Unmarshal([]byte(body),&request));info:=&relaycommon.RelayInfo{OriginModelName:"test",RequestURLPath:"/v1/responses",Request:&request}
  require.NoError(t,omniValidateCertifiedShared(info,p));require.EqualValues(t,8000,info.OmniSharedUpperQuota)
 }
 delete(matrix.Bounds["test"],8);raw,_=json.Marshal(matrix);p.CapabilityMatrix=string(raw);info:=&relaycommon.RelayInfo{OriginModelName:"test",RequestURLPath:"/v1/responses",Request:&dto.OpenAIResponsesRequest{}};require.NoError(t,omniValidateCertifiedShared(info,p));require.Contains(t,info.OmniSharedDispatchBounds,7);require.NotContains(t,info.OmniSharedDispatchBounds,8)
}
func TestOmniSharedCertifiedOutputOverflowAndCandidateProduct(t *testing.T){
 for _,body:=range []string{`{"max_tokens":18446744073709551615}`,`{"max_tokens":1024,"n":2}`,`{"max_tokens":1,"n":9223372036854775807}`,`{"generationConfig":{"maxOutputTokens":1024,"candidateCount":2}}`,`{"max_tokens":-1}`} {require.False(t,omniCertifiedOutputFits([]byte(body),1024),body)}
 require.True(t,omniCertifiedOutputFits([]byte(`{"max_tokens":512,"n":2}`),1024))
}

func TestOmniSharedRestrictedScopeRejectsHiddenCostsBeforeFallback(t *testing.T) {
 p:=model.OmniCommerceProfile{Models:`{"test":[7]}`,PricingHash:"pricing",RoutingHash:"routing"}
 matrix:=model.OmniCapabilityMatrix{SharedScope:"bounded_text",ScopeApproval:strings.Repeat("a",64),PricingHash:p.PricingHash,RoutingHash:p.RoutingHash,Bounds:map[string]map[int]map[string]model.OmniCapabilityBound{"test":{7:{"/v1/responses":{MaxQuota:9000,MaxRequestBytes:4096,MaxOutputTokens:1024,LocalControlEvidence:"synthetic local payload and output controls",AcceptanceHash:strings.Repeat("b",64),VerifiedAt:time.Now().Unix()}}}}}
 raw,_:=json.Marshal(matrix);p.CapabilityMatrix=string(raw)
 for _,body:=range []string{
  `{"model":"test","input":"hello","previous_response_id":"hidden","max_output_tokens":100}`,
  `{"model":"test","input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.invalid/image"}]}],"max_output_tokens":100}`,
  `{"model":"test","input":"hello","tools":[{"type":"web_search"}],"max_output_tokens":100}`,
  `{"model":"test","input":"hello","max_output_tokens":1025}`,
  `{"model":"test","input":"hello"}`,
 } {
  var r dto.OpenAIResponsesRequest;require.NoError(t,json.Unmarshal([]byte(body),&r));info:=&relaycommon.RelayInfo{OriginModelName:"test",OmniPoolID:"pool",OmniPriceProtected:true,RequestURLPath:"/v1/responses",Request:&r}
  require.Error(t,omniValidateCertifiedShared(info,p),body);require.Zero(t,info.OmniSharedUpperQuota)
 }
 var r dto.OpenAIResponsesRequest;require.NoError(t,json.Unmarshal([]byte(`{"model":"test","input":[{"type":"function_call_output","call_id":"synthetic","output":"OK"}],"max_output_tokens":100}`),&r))
 info:=&relaycommon.RelayInfo{OriginModelName:"test",OmniPoolID:"pool",OmniPriceProtected:true,RequestURLPath:"/v1/responses",Request:&r}
 require.NoError(t,omniValidateCertifiedShared(info,p));require.EqualValues(t,9000,info.OmniSharedUpperQuota)
 matrix.ScopeApproval="";raw,_=json.Marshal(matrix);p.CapabilityMatrix=string(raw);require.Error(t,omniValidateCertifiedShared(info,p))
}
