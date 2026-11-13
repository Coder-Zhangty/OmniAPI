package service

import (
 "encoding/json"
 "testing"
 "net/http/httptest"
 "github.com/gin-gonic/gin"
 relaycommon "github.com/QuantumNous/new-api/relay/common"
 "github.com/QuantumNous/new-api/relaykit/dto"
)

func TestOmniQuotedCodexWebSearch(t *testing.T) {
 for _,tc:=range []struct{name,tool,quote string;ok bool}{
  {"cached",`{"type":"web_search","external_web_access":false}`,`{"restrictions":["hosted_web_search_token_priced"]}`,true},
  {"live",`{"type":"web_search"}`,`{"restrictions":["hosted_web_search_token_priced"]}`,true},
  {"dated",`{"type":"web_search_2025_08_26"}`,`{"restrictions":["hosted_web_search_token_priced"]}`,true},
  {"unquoted",`{"type":"web_search"}`,`{"restrictions":[]}`,false},
  {"other_hosted",`{"type":"code_interpreter","container":{"type":"auto"}}`, `{"restrictions":["hosted_web_search_token_priced"]}`,false},
  {"preview_unverified",`{"type":"web_search_preview"}`, `{"restrictions":["hosted_web_search_token_priced"]}`,false},
 } {t.Run(tc.name,func(t *testing.T){
  var req dto.OpenAIResponsesRequest
  if err:=json.Unmarshal([]byte(`{"model":"gpt-6.1-sol","input":"hello","tools":[`+tc.tool+`]}`),&req);err!=nil{t.Fatal(err)}
  err:=OmniValidatePricedRequest(&relaycommon.RelayInfo{Request:&req,OriginModelName:req.Model,OmniPriceQuote:[]byte(tc.quote)})
  if (err==nil)!=tc.ok{t.Fatalf("accepted=%v error=%v",tc.ok,err)}
 })}
}

func TestOmniWebSearchTokenBillingAvoidsDoubleCharge(t *testing.T){
 c,_:=gin.CreateTestContext(httptest.NewRecorder())
 for _,quoted:=range []bool{false,true}{
  info:=&relaycommon.RelayInfo{OriginModelName:"gpt-6.1-sol",ResponsesUsageInfo:&relaycommon.ResponsesUsageInfo{BuiltInTools:map[string]*relaycommon.BuildInToolInfo{"web_search":{CallCount:2}}}}
  if quoted {info.OmniPriceQuote=[]byte(`{"restrictions":["hosted_web_search_token_priced"]}`)}
  summary:=textQuotaSummary{ModelName:"gpt-6.1-sol",GroupRatio:1}
  surcharge:=calculateTextToolCallSurcharge(c,info,&summary)
  if surcharge.IsZero()!=quoted {t.Fatalf("quoted=%v unexpected search surcharge %v",quoted,surcharge)}
 }
}

