package controller

import (
 "bytes"
 "context"
 "errors"
 "io"
 "net/http"
 "strconv"
 "strings"
 "time"
 "github.com/QuantumNous/new-api/common"
 "github.com/QuantumNous/new-api/middleware"
 "github.com/QuantumNous/new-api/model"
 "github.com/QuantumNous/new-api/relay"
 "github.com/QuantumNous/new-api/relay/channel"
 relaycommon "github.com/QuantumNous/new-api/relay/common"
 "github.com/QuantumNous/new-api/relaykit/dto"
 "github.com/gin-gonic/gin"
)
func OmniRegistryAdmin(c *gin.Context){
 var rows []model.OmniDiagnosticAttempt
 err:=model.DB.Order("id desc").Limit(100).Find(&rows).Error
 omniReply(c,gin.H{"registry":model.GetOmniModelRegistry(),"diagnostics":rows},err)
}
func OmniRegistryAliases(c *gin.Context){
 var next struct{ExpectedVersion string `json:"expected_version"`;Registry model.OmniModelRegistry `json:"registry"`}
 if c.ShouldBindJSON(&next)!=nil{omniReply(c,nil,errors.New("invalid model registry"));return}
 current:=model.GetOmniModelRegistry()
 if next.ExpectedVersion!=current.Version||next.Registry.Version==current.Version{omniReply(c,nil,errors.New("registry version conflict"));return}
 if err:=model.OmniValidateRegistry(next.Registry);err!=nil{omniReply(c,nil,err);return}
 if !next.Registry.Enabled||len(next.Registry.Models)!=len(current.Models){omniReply(c,nil,errors.New("new model topology requires acceptance"));return}
 for name,row:=range current.Models{
  candidate,ok:=next.Registry.Models[name]
  if row.Lifecycle!=candidate.Lifecycle||row.LifecycleEvidenceHash!=candidate.LifecycleEvidenceHash{omniReply(c,nil,errors.New("model lifecycle change requires acceptance"));return}
  for _,alias:=range row.Aliases { found:=false;for _,nextAlias:=range candidate.Aliases {if nextAlias==alias{found=true;break}};if !found{omniReply(c,nil,errors.New("existing aliases cannot be removed or reassigned"));return} }
  oldBindings,_:=common.Marshal(row.Bindings);newBindings,_:=common.Marshal(candidate.Bindings)
  oldClient,_:=common.Marshal(row.NativeClientTools);newClient,_:=common.Marshal(candidate.NativeClientTools)
  oldHosted,_:=common.Marshal(row.HostedToolPrices);newHosted,_:=common.Marshal(candidate.HostedToolPrices)
  if string(oldClient)!=string(newClient)||string(oldHosted)!=string(newHosted){omniReply(c,nil,errors.New("tool capability or pricing change requires acceptance"));return}
  if !ok||row.Version!=candidate.Version||row.Capability!=candidate.Capability||string(oldBindings)!=string(newBindings){omniReply(c,nil,errors.New("model identity or topology change requires acceptance"));return}
 }
 err:=model.OmniUpdateRegistry(next.ExpectedVersion,next.Registry)
 omniReply(c,next.Registry,err)
}

// Unlike native billing tests, this is an administrator-only fixed-channel
// transport probe. It does not reserve, settle or debit a customer's wallet.
func OmniDiagnosticChannelTest(c *gin.Context) bool {
 if !model.GetOmniModelRegistry().Enabled{return false}
 if c.GetInt("role")<common.RoleAdminUser{c.JSON(http.StatusForbidden,gin.H{"success":false});return true}
 id,err:=strconv.Atoi(c.Param("id"));if err!=nil{return false}
 ch,err:=model.GetChannelById(id,true);if err!=nil{omniReply(c,nil,err);return true}
 name:=c.Query("model");if name==""{name=strings.Split(ch.Models,",")[0]}
 registry:=model.GetOmniModelRegistry();canonical:=registry.Canonical(name)
 priceState,_,_:=model.OmniPriceAvailability(canonical)
 _,quoted:=model.GetOmniPrices().Quotes[canonical]
 result:=gin.H{"canonical_model":canonical,"price_ready":quoted||priceState=="available","sale_ready":priceState=="available","channel_id":id,"test_mode":"connectivity","identity_state":"unknown","diagnostic_state":"not_executed","success":false,"time":0.0}
 row,registered:=registry.Models[canonical]
 upstream:=name;var binding model.OmniModelBinding;bound:=false
 if registered{result["identity_state"]="registered";for _,b:=range row.Bindings{if b.ChannelID==id{binding=b;upstream=b.UpstreamModel;bound=true;break}}}
 if !bound{
  for _,candidate:=range strings.Split(ch.Models,","){if candidate==name||candidate==canonical{bound=true;break}}
 }
 unsupported:=!registered||(row.Capability!="text"&&row.Capability!="text+image")
 if !bound||unsupported||(ch.Type!=1&&ch.Type!=14){result["error_code"]="diagnostic_not_executed";result["message"]="未执行：需要确认模型绑定、协议及非文本测试费用";c.JSON(200,result);return true}
 endpoint:=c.Query("endpoint_type")
 if endpoint!=""&&endpoint!="openai"&&endpoint!="openai_chat"&&endpoint!="anthropic"{result["error_code"]="diagnostic_not_executed";result["message"]="未执行：当前受控样例仅用于文本连通测试";c.JSON(200,result);return true}
 ctx,cancel:=context.WithTimeout(c.Request.Context(),35*time.Second);defer cancel()
 child:=c.Copy();child.Request=c.Request.Clone(ctx);child.Request.Method="POST";child.Request.URL.Path="/v1/chat/completions";child.Request.Header=make(http.Header);child.Request.Header.Set("Content-Type","application/json")
 child.Writer=c.Writer;child.Set("using_group",strings.Split(ch.Group,",")[0]);child.Set("omni_registry_version",registry.Version)
 if apiErr:=middleware.SetupContextForSelectedChannel(child,ch,canonical);apiErr!=nil{result["error_code"]="diagnostic_not_executed";result["message"]="未执行：渠道参数尚未完整配置";c.JSON(200,result);return true}
 requestID:=common.NewRequestId();common.SetContextKey(child,common.RequestIdKey,requestID)
 stream,_:=strconv.ParseBool(c.Query("stream"))
 raw:=map[string]any{"model":upstream,"max_tokens":64,"stream":stream,"messages":[]map[string]string{{"role":"user","content":"Return only OK. Do not reason or call tools."}}}
 data,_:=common.Marshal(raw);var request dto.GeneralOpenAIRequest;_ = common.Unmarshal(data,&request)
 info:=&relaycommon.RelayInfo{Request:&request,RequestId:requestID,OriginModelName:canonical,UsingGroup:strings.Split(ch.Group,",")[0],UserId:c.GetInt("id"),IsStream:stream,RequestURLPath:"/v1/chat/completions",RelayFormat:"openai"}
 info.InitChannelMeta(child);info.UpstreamModelName=upstream;request.SetModelName(upstream);info.DisablePing=true
 kind:=ch.Type
 if endpoint=="anthropic"{kind=14}else{for _,protocol:=range binding.Protocols{if protocol=="claude"||protocol=="anthropic"{kind=14;break}}}
 info.ChannelType=kind;info.ApiType,_=common.ChannelType2APIType(kind)
 apiType,_:=common.ChannelType2APIType(kind);adaptor:=relay.GetAdaptor(apiType)
 if adaptor==nil{result["error_code"]="diagnostic_not_executed";result["message"]="未执行：协议适配器不可用";c.JSON(200,result);return true}
 adaptor.Init(info)
 converted,conversionErr:=adaptor.ConvertOpenAIRequest(child,info,&request)
 if conversionErr!=nil{result["error_code"]="diagnostic_not_executed";result["message"]="未执行：测试样例无法转换为当前协议";c.JSON(200,result);return true}
 body,_:=common.Marshal(converted);start:=time.Now()
 resp,transportErr:=channel.DoApiRequest(adaptor,child,info,bytes.NewReader(body))
 audit:=model.OmniDiagnosticAttempt{RequestID:requestID,AdminID:c.GetInt("id"),ChannelID:id,Model:canonical,State:"failed",CostEstimated:true}
 valid:=false;var decoded map[string]any
 if transportErr==nil&&resp!=nil{
  audit.StatusCode=resp.StatusCode
  payload,readErr:=io.ReadAll(io.LimitReader(resp.Body,2<<20));resp.Body.Close()
  if readErr==nil&&resp.StatusCode==200 {
   complete:=false;content:=false;invalid:=false
   frames:=[][]byte{payload}
   if stream{frames=nil;for _,line:=range strings.Split(string(payload),"\n"){if strings.HasPrefix(line,"data:"){value:=strings.TrimSpace(strings.TrimPrefix(line,"data:"));if value=="[DONE]"{complete=true;continue};frames=append(frames,[]byte(value))}}}else{complete=true}
   for _,frame:=range frames{
    var item map[string]any
    if common.Unmarshal(frame,&item)!=nil{invalid=true;continue}
    if item["error"]!=nil{invalid=true}
    if item["type"]=="message_stop"{complete=true}
    if item["usage"]!=nil{decoded=item}
    if choices,ok:=item["choices"].([]any);ok{for _,entry:=range choices{choice,_:=entry.(map[string]any);for _,key:=range []string{"message","delta"}{message,_:=choice[key].(map[string]any);for _,part:=range []string{"content","reasoning_content","reasoning"}{if text,ok:=message[part].(string);ok&&text!=""{content=true}}}}}
    if parts,ok:=item["content"].([]any);ok{for _,part:=range parts{value,_:=part.(map[string]any);if text,ok:=value["text"].(string);ok&&text!=""{content=true}}}
    if delta,ok:=item["delta"].(map[string]any);ok{if text,ok:=delta["text"].(string);ok&&text!=""{content=true}}
   }
   valid=complete&&content&&!invalid
  }
 }
 audit.DurationMillis=time.Since(start).Milliseconds()
 if usage,ok:=decoded["usage"].(map[string]any);ok{
  for _,pair:=range []struct{keys []string;target *int}{{[]string{"prompt_tokens","input_tokens"},&audit.InputTokens},{[]string{"completion_tokens","output_tokens"},&audit.OutputTokens}}{for _,key:=range pair.keys{if v,ok:=usage[key].(float64);ok&&v>=0&&v<=2147483647{*pair.target=int(v);break}}}
  if input,ok:=binding.Procurement["input_usd_per_million"];ok{if output,ok:=binding.Procurement["output_usd_per_million"];ok{cost:=(float64(audit.InputTokens)*input+float64(audit.OutputTokens)*output)/1000000;audit.CostUSD=&cost}}
 }
 if valid{audit.State="passed";result["success"]=true;result["diagnostic_state"]="passed";result["protocol_state"]="valid";result["message"]="连通与响应协议测试通过；销售资格须另行验收"}else{result["diagnostic_state"]="failed";result["protocol_state"]="failed";result["error_code"]="diagnostic_upstream_failed";result["message"]="当前渠道未返回完整有效结果；请查看管理员诊断记录"}
 result["time"]=float64(audit.DurationMillis)/1000;result["upstream_status"]=audit.StatusCode
 if err:=model.OmniRecordDiagnostic(audit);err!=nil{result["success"]=false;result["diagnostic_state"]="not_executed";result["error_code"]="diagnostic_not_executed";result["message"]="测试完成但成本记录保存失败，暂不认定验收通过"}
 c.JSON(http.StatusOK,result);return true
}
