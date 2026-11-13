package dto

import("encoding/json";"strings";"testing")

func TestOmniClaudeToolResultMetadataPreservesTextAndImages(t *testing.T){
 for _,tc:=range []struct{content string;files int;contains []string;absent string}{
  {`"plain tool output"`,0,[]string{"plain tool output"},""},
  {`[{"type":"text","text":"before"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"SYNTHETIC_BASE64"}},{"type":"text","text":"after"}]`,1,[]string{"before","after"},"SYNTHETIC_BASE64"},
  {`[{"type":"image","source":{"type":"url","url":"https://example.invalid/image.png"}}]`,1,nil,"https://example.invalid/image.png"},
  {`[{"type":"future_tool_result","value":"keep_unknown"}]`,0,[]string{"future_tool_result","keep_unknown"},""},
  {`[{"type":"image","source":{}}]`,0,[]string{"image"},""},
 }{
  original:=`{"model":"claude-opus-5-5","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"synthetic","content":`+tc.content+`}]}]}`
  var request ClaudeRequest;if err:=json.Unmarshal([]byte(original),&request);err!=nil{t.Fatal(err)}
  before,_:=json.Marshal(request);meta:=request.GetTokenCountMeta();after,_:=json.Marshal(request)
  if string(before)!=string(after){t.Fatal("token metadata mutated request")}
  if len(meta.Files)!=tc.files{t.Fatalf("files=%d want=%d",len(meta.Files),tc.files)}
  for _,text:=range tc.contains{if !strings.Contains(meta.CombineText,text){t.Fatalf("dropped text %s",text)}}
  if tc.absent!=""&&strings.Contains(meta.CombineText,tc.absent){t.Fatal("encoded image counted as text")}
 }
}
