package dto

import (
 "encoding/json"
 "github.com/QuantumNous/new-api/relaykit/types"
)

// A tool-result image has the same token metadata as a direct image. Its wire
// base64 is transport encoding, not text sent to the language tokenizer.
func omniClaudeToolResultMeta(value any,texts *[]string,files *[]*types.FileMeta){
 switch v:=value.(type){
 case string:*texts=append(*texts,v)
 case []any:for _,item:=range v{omniClaudeToolResultMeta(item,texts,files)}
 case map[string]any:
  switch v["type"]{
  case "text":if text,ok:=v["text"].(string);ok{*texts=append(*texts,text);return}
  case "image":
   data,err:=json.Marshal(v);var image ClaudeMediaMessage
   if err==nil&&json.Unmarshal(data,&image)==nil {if source:=image.ToFileSource();source!=nil{*files=append(*files,&types.FileMeta{FileType:types.FileTypeImage,Source:source});return}}
  case "tool_result":omniClaudeToolResultMeta(v["content"],texts,files);return
  }
  data,_:=json.Marshal(v);*texts=append(*texts,string(data))
 case nil:return
 default:data,_:=json.Marshal(v);*texts=append(*texts,string(data))
 }
}
