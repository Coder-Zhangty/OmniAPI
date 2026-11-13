package service

import (
 "encoding/json"
 "strings"
 "testing"
 relaycommon "github.com/QuantumNous/new-api/relay/common"
 "github.com/QuantumNous/new-api/relaykit/dto"
)

func TestOmniCodexClientToolDefinitions(t *testing.T) {
 for _,tc:=range []struct{name,tool,quote string; accepted bool}{
  {"namespace_function",`{"type":"namespace","name":"functions","tools":[{"type":"function","name":"ping","parameters":{"type":"object"}}]}`,`{"restrictions":["client_tool_namespaces_token_priced"]}`,true},
  {"namespace_custom",`{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"apply_patch"}]}`,`{"restrictions":["client_tool_namespaces_token_priced","client_custom_tools_token_priced"]}`,true},
  {"namespace_without_quote",`{"type":"namespace","name":"functions","tools":[{"type":"function","name":"ping"}]}`,`{"restrictions":[]}`,false},
  {"namespace_hosted_tool",`{"type":"namespace","name":"functions","tools":[{"type":"web_search"}]}`,`{"restrictions":["client_tool_namespaces_token_priced"]}`,false},
  {"namespace_blank_name",`{"type":"namespace","name":" ","tools":[{"type":"function","name":"ping"}]}`,`{"restrictions":["client_tool_namespaces_token_priced"]}`,false},
  {"namespace_empty",`{"type":"namespace","name":"functions","tools":[]}`,`{"restrictions":["client_tool_namespaces_token_priced"]}`,false},
  {"namespace_invalid_child",`{"type":"namespace","name":"functions","tools":[{}]}`,`{"restrictions":["client_tool_namespaces_token_priced"]}`,false},
  {"namespace_custom_without_quote",`{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"patch"}]}`,`{"restrictions":["client_tool_namespaces_token_priced"]}`,false},
  {"client_search",`{"type":"tool_search","execution":"client","parameters":{"type":"object"}}`, `{"restrictions":["client_tool_search_token_priced"]}`,true},
  {"hosted_search",`{"type":"tool_search","execution":"server"}`, `{"restrictions":["client_tool_search_token_priced"]}`,false},
  {"implicit_hosted_search",`{"type":"tool_search"}`, `{"restrictions":["client_tool_search_token_priced"]}`,false},
  {"client_search_without_quote",`{"type":"tool_search","execution":"client"}`, `{"restrictions":[]}`,false},
 } {
  t.Run(tc.name,func(t *testing.T){
   var request dto.OpenAIResponsesRequest
   if err:=json.Unmarshal([]byte(`{"model":"gpt-6.1-sol","input":"synthetic","tools":[`+tc.tool+`]}`),&request);err!=nil{t.Fatal(err)}
   err:=OmniValidatePricedRequest(&relaycommon.RelayInfo{Request:&request,OriginModelName:request.Model,OmniPriceQuote:[]byte(tc.quote)})
   if (err==nil)!=tc.accepted{t.Fatalf("accepted=%t error=%v",tc.accepted,err)}
  })
 }
}

func TestOmniCodexUnverifiedClientToolsNeverReportHostedFees(t *testing.T){
 for _,name:=range []string{"claude-fable-5","claude-fable-5-1","claude-haiku-4-5-20251001","claude-opus-4-6","claude-opus-4-8","claude-opus-5","claude-opus-5-5","claude-sonnet-4-6","claude-sonnet-5","gemini-3.7-flash","gemini-3.8-flash","glm-5.3-flash","gpt-5.5","gpt-5.6-sol","gpt-5.6-terra","gpt-6-astra","gpt-6-sol","gpt-6.1-sol","grok-4.5","grok-4.6","grok-4.7","deepseek-v4-flash","deepseek-v4-pro","glm-5.3","gpt-6-luna","deepseek-v4.1-flash","glm-5.2","grok-4.7-build-fast"}{
  for _,tool:=range []string{`{"type":"custom","name":"apply_patch"}`,`{"type":"namespace","name":"functions","tools":[{"type":"function","name":"ping"}]}`,`{"type":"tool_search","execution":"client"}`} {
   var request dto.OpenAIResponsesRequest
   _=json.Unmarshal([]byte(`{"model":"`+name+`","input":"synthetic","tools":[`+tool+`]}`),&request)
   err:=OmniValidatePricedRequest(&relaycommon.RelayInfo{Request:&request,OriginModelName:name,OmniPriceQuote:[]byte(`{"restrictions":[]}`)})
   if err==nil||strings.Contains(err.Error(),"附加费用")||!strings.Contains(err.Error(),"兼容"){t.Fatalf("%s: wrong client tool classification: %v",name,err)}
  }
 }
}

func TestOmniCodexDiscoveredSchemaIsNotMedia(t *testing.T){
 var request dto.OpenAIResponsesRequest
 _=json.Unmarshal([]byte(`{"model":"gpt-6.1-sol","input":[{"type":"tool_search_output","execution":"client","call_id":"synthetic","tools":[{"type":"function","name":"describe","parameters":{"type":"object","properties":{"input_audio":{"type":"string"},"image_url":{"type":"string"}}}}]}]}`),&request)
 err:=OmniValidatePricedRequest(&relaycommon.RelayInfo{Request:&request,OriginModelName:request.Model,OmniPriceQuote:[]byte(`{"restrictions":["client_tool_search_token_priced"]}`)})
 if err!=nil{t.Fatal("discovered function schema classified as media:",err)}
}

func TestOmniCodexDiscoveredToolsKeepQuoteGate(t *testing.T){
 for _,tc:=range []struct{tools string;accepted bool}{
  {`[{"type":"namespace","name":"functions","tools":[{"type":"function","name":"ping"}]}]`,true},
  {`[{"type":"web_search"}]`,false},
  {`[{"type":"namespace","name":"functions","tools":[{"type":"code_interpreter"}]}]`,false},
 }{
  var request dto.OpenAIResponsesRequest
  if err:=json.Unmarshal([]byte(`{"model":"gpt-6.1-sol","input":[{"type":"tool_search_output","execution":"client","call_id":"synthetic","tools":`+tc.tools+`}]}`),&request);err!=nil{t.Fatal(err)}
  err:=OmniValidatePricedRequest(&relaycommon.RelayInfo{Request:&request,OriginModelName:request.Model,OmniPriceQuote:[]byte(`{"restrictions":["client_tool_search_token_priced","client_tool_namespaces_token_priced"]}`)})
  if (err==nil)!=tc.accepted{t.Fatalf("accepted=%t error=%v",tc.accepted,err)}
 }
}
