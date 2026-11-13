package openai

import("encoding/json";"testing")

func TestOmniClientToolContractsRecognizeResponsesCalls(t *testing.T){
 for _,tc:=range []struct{name,choice,output string;accepted bool}{
  {"required_custom",`"required"`,`[{"type":"custom_tool_call","name":"patch","call_id":"synthetic","input":"synthetic"}]`,true},
  {"required_client_search",`"required"`,`[{"type":"tool_search_call","execution":"client","call_id":"synthetic","arguments":{"query":"synthetic"}}]`,true},
  {"named_custom",`{"type":"custom","name":"patch"}`,`[{"type":"custom_tool_call","name":"patch","call_id":"synthetic","input":"synthetic"}]`,true},
  {"named_custom_missing",`{"type":"custom","name":"patch"}`,`[]`,false},
  {"named_custom_wrong_name",`{"type":"custom","name":"patch"}`,`[{"type":"custom_tool_call","name":"other","input":"synthetic"}]`,false},
  {"named_custom_wrong_kind",`{"type":"custom","name":"patch"}`,`[{"type":"function_call","name":"patch","arguments":"{}"}]`,false},
  {"named_function_wrong_kind",`{"type":"function","name":"patch"}`,`[{"type":"custom_tool_call","name":"patch","input":"synthetic"}]`,false},
  {"forced_client_search",`{"type":"tool_search"}`,`[{"type":"tool_search_call","execution":"client","arguments":{"query":"synthetic"}}]`,true},
  {"forced_search_missing",`{"type":"tool_search"}`,`[]`,false},
  {"required_without_call",`"required"`,`[{"type":"message","content":[{"type":"output_text","text":"hello"}]}]`,false},
 }{t.Run(tc.name,func(t *testing.T){
  var raw map[string]any
  _=json.Unmarshal([]byte(`{"tools":[{"type":"custom","name":"patch"},{"type":"tool_search","execution":"client"}],"tool_choice":`+tc.choice+`}`),&raw)
  frame:=`data: {"type":"response.completed","response":{"status":"completed","output":`+tc.output+`}}`
  err:=omniValidateBuffered([]string{frame},raw)
  if (err==nil)!=tc.accepted{t.Fatalf("accepted=%t error=%v",tc.accepted,err)}
 })}
}
