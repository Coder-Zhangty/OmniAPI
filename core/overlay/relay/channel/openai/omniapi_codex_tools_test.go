package openai

import("encoding/json";"slices";"testing")

func TestOmniCodexToolsRouteByVerifiedCapabilities(t *testing.T){
 for _,tc:=range []struct{body string;required []string}{
  {`{"tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"apply_patch"}]}]}`,[]string{"tools","tool_namespaces","custom_tools"}},
  {`{"tools":[{"type":"tool_search","execution":"client"}]}`,[]string{"tools","client_tool_search"}},
  {`{"input":[{"type":"tool_search_output","execution":"client","tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"apply_patch"}]}]}]}`,[]string{"client_tool_search","tool_namespaces","custom_tools"}},
 }{
  var raw map[string]any;if err:=json.Unmarshal([]byte(tc.body),&raw);err!=nil{t.Fatal(err)}
  actual:=omniRequiredCapabilities(raw)
  for _,capability:=range tc.required{if !slices.Contains(actual,capability){t.Fatalf("missing %s: %v",capability,actual)}}
 }
}

func TestOmniCodexPlainTextFormatDoesNotRequireStructured(t *testing.T){
 for _,tc:=range []struct{body string;structured bool}{
  {`{"text":{"format":{"type":"text"},"verbosity":"medium"}}`,false},
  {`{"text":{"format":{"type":"json_schema","name":"test","schema":{"type":"object"}}}}`,true},
  {`{"response_format":{"type":"json_object"}}`,true},
 }{
  var raw map[string]any;_ = json.Unmarshal([]byte(tc.body),&raw)
  if slices.Contains(omniRequiredCapabilities(raw),"structured")!=tc.structured{t.Fatalf("wrong structured routing for %s",tc.body)}
 }
}
