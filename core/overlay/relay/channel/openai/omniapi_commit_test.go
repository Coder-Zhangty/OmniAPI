package openai

import (
 "encoding/json"
 "fmt"
 "testing"
)

func TestOmniBufferedSchemaAndArguments(t *testing.T) {
 var raw map[string]any
 json.Unmarshal([]byte(`{"response_format":{"type":"json_schema","json_schema":{"schema":{"type":"object","required":["answer"],"properties":{"answer":{"type":"string"}}}}}}`),&raw)
 frame:=func(text string)string{encoded,_:=json.Marshal(map[string]any{"choices":[]any{map[string]any{"delta":map[string]any{"content":text}}}});return "data: "+string(encoded)}
 if omniValidateBuffered([]string{frame(`{"answer":"OK"}`)},raw)!=nil{t.Fatal("valid schema rejected")}
 if omniValidateBuffered([]string{frame(`{"answer":12}`)},raw)==nil{t.Fatal("schema mismatch accepted")}
 if omniValidateBuffered([]string{`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{"}}]}}]}`},map[string]any{})==nil{t.Fatal("partial tool JSON accepted")}
}
func TestOmniSchemaCannotFetchExternalReferences(t *testing.T) {
 schema:=map[string]any{"type":"json_schema","json_schema":map[string]any{"schema":map[string]any{"$ref":"http://127.0.0.1/private"}}}
 if err:=omniValidateStructured(`{}`,schema);err==nil{t.Fatal(fmt.Sprint("external reference accepted"))}
}
