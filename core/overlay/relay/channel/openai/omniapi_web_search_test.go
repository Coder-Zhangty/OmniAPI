package openai

import("encoding/json";"testing";"slices")

func TestOmniWebSearchRequiresVerifiedRoute(t *testing.T){
 for _,kind:=range []string{"web_search","web_search_2025_08_26"}{
  var raw map[string]any;_ =json.Unmarshal([]byte(`{"tools":[{"type":"`+kind+`","external_web_access":false}]}`),&raw)
  if !slices.Contains(omniRequiredCapabilities(raw),"web_search"){t.Fatal("search could use a route that silently ignores it")}
  if !slices.Contains(omniRequiredCapabilities(raw),"web_search_token_priced"){t.Fatal("search could use a route with an unquoted per-call fee")}
 }
}

func TestOmniWebSearchContract(t *testing.T){
 for _,tc:=range []struct{name,choice,output string;ok bool}{
  {"forced",`{"type":"web_search"}`,`[{"type":"web_search_call","status":"completed","action":{"type":"search","query":"synthetic"}}]`,true},
  {"forced_missing",`{"type":"web_search"}`,`[{"type":"message","content":[]}]`,false},
  {"required",`"required"`,`[{"type":"web_search_call","status":"completed","action":{"type":"search","query":"synthetic"}}]`,true},
  {"forced_wrong_kind",`{"type":"web_search"}`,`[{"type":"function_call","name":"web_search","arguments":"{}"}]`,false},
  {"forced_failed",`{"type":"web_search"}`,`[{"type":"web_search_call","status":"failed","action":{"type":"search"}}]`,false},
  {"auto_text",`"auto"`,`[{"type":"message","content":[]}]`,true},
 }{t.Run(tc.name,func(t *testing.T){
  var raw,result map[string]any;_=json.Unmarshal([]byte(`{"tools":[{"type":"web_search"}],"tool_choice":`+tc.choice+`}`),&raw);_=json.Unmarshal([]byte(`{"output":`+tc.output+`}`),&result)
  err:=omniValidateToolResult(result,raw);if (err==nil)!=tc.ok{t.Fatalf("accepted=%v error=%v",tc.ok,err)}
  frames:=[]string{`data: {"type":"response.completed","response":{"status":"completed","output":`+tc.output+`}}`}
  err=omniValidateBuffered(frames,raw);if (err==nil)!=tc.ok{t.Fatalf("buffered accepted=%v error=%v",tc.ok,err)}
 })}
}
