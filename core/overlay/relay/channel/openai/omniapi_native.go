package openai

// Routing owns retries; native adaptors own conversion, headers and transport.
import (
 "bufio"
 "bytes"
 "encoding/json"
 "errors"
 "io"
 "net/http"
 "strings"
 "github.com/QuantumNous/new-api/common"
 "github.com/QuantumNous/new-api/model"
 "github.com/QuantumNous/new-api/relay/channel"
 "github.com/QuantumNous/new-api/relay/channel/claude"
 relaycommon "github.com/QuantumNous/new-api/relay/common"
 "github.com/QuantumNous/new-api/relaykit/dto"
 "github.com/QuantumNous/new-api/relaykit/relayconvert"
 "github.com/QuantumNous/new-api/relaykit/types"
 "github.com/QuantumNous/new-api/service"
 "github.com/gin-gonic/gin"
)

func omniNativeRequest(c *gin.Context, info *relaycommon.RelayInfo, raw map[string]any, kind int) (*http.Response,error) {
 if info.OmniNativeClaude {
  return omniSendClaude(c, info, kind)
 }
 raw=omniOpusToolRequest(info.OriginModelName,raw)
 kind=omniChannelKind(info.OmniRegistryVersion,info.OriginModelName,info.ChannelId,kind)
 info.ChannelType=kind;info.ApiType,_=common.ChannelType2APIType(kind)
 if model.OmniKuaiMeteringFor(info.OmniRegistryVersion, info.OriginModelName, info.ChannelId) != nil && info.IsStream && !omniResponsesPath(info) {
  options, _ := raw["stream_options"].(map[string]any)
  copied := make(map[string]any,len(options)+1)
  for key,value := range options {copied[key]=value}
  copied["include_usage"]=true
  raw["stream_options"]=copied
 }
 encoded,err:=json.Marshal(raw); if err!=nil{return nil,err}
 var native channel.Adaptor = &Adaptor{}
 if kind==14 {
  native=&claude.Adaptor{}
  var converted any
  var request dto.GeneralOpenAIRequest
  // Conversion must describe the normalized body, rather than the client protocol.
  clone:=*info; clone.RelayFormat="openai"
  if omniResponsesPath(info) {
   var responseRequest dto.OpenAIResponsesRequest
   if err=json.Unmarshal(encoded,&responseRequest);err!=nil{return nil,err}
   clone.RelayFormat=types.RelayFormatOpenAIResponses
   converted,err=native.ConvertOpenAIResponsesRequest(c,&clone,responseRequest)
  } else {
   if err=json.Unmarshal(encoded,&request);err!=nil{return nil,err}
   converted,err=native.ConvertOpenAIRequest(c,&clone,&request)
  }
  if err!=nil{return nil,err}
  encoded,err=json.Marshal(converted);if err!=nil{return nil,err}
 } else if kind!=1 {return nil,errors.New("route protocol not verified")}
 native.Init(info)
 resp,err:=channel.DoApiRequest(native,c,info,bytes.NewReader(encoded))
 if err!=nil||resp.StatusCode!=200||kind!=14{return resp,err}
 if !info.IsStream {
  defer resp.Body.Close()
  data,e:=io.ReadAll(io.LimitReader(resp.Body,(16<<20)+1));if e!=nil{return nil,e}
  if len(data)>16<<20{return nil,errors.New("native response too large")}
  var message dto.ClaudeResponse
  if e=json.Unmarshal(data,&message);e!=nil{return nil,e}
  if message.GetClaudeError()!=nil{return nil,errors.New("native upstream error")}
  service.OmniObserveClaudeHostedUsage(c,info,&message)
  if omniResponsesPath(info) {
   converted,e:=service.ConvertResponse(c,info,types.RelayFormatOpenAIResponses,&message);if e!=nil{return nil,e}
   data,e=json.Marshal(converted.Value);if e!=nil{return nil,e}
  } else {
   converted:=relayconvert.ResponseClaude2OpenAI(&message)
   if message.Usage!=nil{converted.Usage=*relayconvert.UsageFromClaudeAPIUsage(message.Usage)}
   data,e=json.Marshal(converted);if e!=nil{return nil,e}
  }
  resp.Body=io.NopCloser(bytes.NewReader(data));resp.ContentLength=int64(len(data));return resp,nil
 }
 original:=resp.Body
 reader,writer:=io.Pipe()
 resp.Body=reader;resp.ContentLength=-1
 go func(){
  defer original.Close();defer writer.Close()
  scanner:=bufio.NewScanner(original);scanner.Buffer(make([]byte,4096),1<<20)
  state:=relayconvert.NewClaudeToChatStreamState()
  responseState,e:=relayconvert.NewResponseStreamState(types.RelayFormatClaude,types.RelayFormatOpenAIResponses,relayconvert.ResponseStreamOptions{Model:info.OriginModelName,EmitSequenceNumber:true})
  if e!=nil{writer.CloseWithError(e);return}
  usage:=&relayconvert.ClaudeResponseInfo{Usage:&dto.Usage{}}
  for scanner.Scan(){
   line:=scanner.Text();if !strings.HasPrefix(line,"data:"){continue}
   var event dto.ClaudeResponse
   if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line,"data:"))),&event)!=nil{writer.CloseWithError(errors.New("invalid Claude frame"));return}
   if e:=event.GetClaudeError();e!=nil&&e.Type!=""{writer.CloseWithError(errors.New("Claude upstream stream error"));return}
   service.OmniObserveClaudeHostedUsage(c,info,&event)
   if omniResponsesPath(info) {
    relayconvert.FormatClaudeResponseInfo(&event,nil,usage)
    results,e:=service.ConvertStreamResponseChunk(c,info,responseState,&event);if e!=nil{writer.CloseWithError(e);return}
    if event.Type=="message_stop"{
     relayconvert.FinalizeClaudeStreamBillingUsage(usage);responseState.SetUsage(relayconvert.UsageFromClaudeUsage(usage.Usage))
     final,e:=service.FinalizeStreamResponse(c,info,responseState);if e!=nil{writer.CloseWithError(e);return};results=append(results,final...)
    }
    for _,result:=range results{
     mapped,ok:=result.Value.(relayconvert.ChatToResponsesStreamEvent);if !ok{writer.CloseWithError(errors.New("invalid Responses conversion"));return}
     mapped.Payload.Type=mapped.Type;data,_:=json.Marshal(mapped.Payload)
     if _,e=writer.Write(append(append([]byte("data: "),data...),[]byte("\n\n")...));e!=nil{return}
    }
    if event.Type=="message_stop"{return};continue
   }
   chunk,e:=state.ConvertChunk(&event);if e!=nil{writer.CloseWithError(e);return}
   relayconvert.FormatClaudeResponseInfo(&event,chunk,usage)
   if chunk!=nil{data,_:=json.Marshal(chunk);if _,e=writer.Write(append(append([]byte("data: "),data...),[]byte("\n\n")...));e!=nil{return}}
   if event.Type=="message_stop"{
    relayconvert.FinalizeClaudeStreamBillingUsage(usage)
    data,_:=json.Marshal(map[string]any{"choices":[]any{},"usage":relayconvert.UsageFromClaudeUsage(usage.Usage)})
    if _,e=writer.Write(append(append([]byte("data: "),data...),[]byte("\n\ndata: [DONE]\n\n")...));e!=nil{return};return
   }
  }
  writer.CloseWithError(io.ErrUnexpectedEOF)
 }()
 return resp,nil
}
