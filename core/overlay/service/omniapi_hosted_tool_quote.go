package service

import (
 "encoding/json"
 "errors"
 "strings"
 "slices"
 "math"
 "github.com/QuantumNous/new-api/model"
 relaycommon "github.com/QuantumNous/new-api/relay/common"
 "github.com/QuantumNous/new-api/relaykit/dto"
)

type OmniHostedToolPrice = model.OmniHostedToolPrice

type omniNativeToolQuote struct {
 Tools map[string]OmniHostedToolPrice `json:"hosted_tool_prices"`
 ClientTools []string `json:"native_client_tools"`
}

func omniFrozenNativeQuote(info *relaycommon.RelayInfo) omniNativeToolQuote {
 var quote omniNativeToolQuote
 if info==nil{return quote}
 if len(info.OmniNativeToolQuote)>0 { _=json.Unmarshal(info.OmniNativeToolQuote,&quote);return quote }
 registry:=model.OmniRegistryAt(info.OmniRegistryVersion)
 row:=registry.Models[registry.Canonical(info.GetBillingModelName())]
 quote.Tools=row.HostedToolPrices;quote.ClientTools=row.NativeClientTools
 info.OmniNativeToolQuote,_=json.Marshal(quote)
 return quote
}

func OmniHostedToolPriceFor(info *relaycommon.RelayInfo,name string) (OmniHostedToolPrice,bool) {
 quote:=omniFrozenNativeQuote(info)
 price,ok:=quote.Tools[name]
 return price,ok&&price.RetailUSDMicros>=0&&price.RetailUSDMicros<=1000000000&&price.EstimatedCostUSDMicros>=0&&len(price.EvidenceHash)==64&&price.Basis!=""&&price.DefaultMaxUses>0
}

func omniNativeClientTool(info *relaycommon.RelayInfo,tool map[string]any) (bool,error) {
 if _,ok:=info.Request.(*dto.ClaudeRequest);!ok{return false,nil}
 kind,_:=tool["type"].(string);name,_:=tool["name"].(string)
 if strings.HasPrefix(kind,"bash_")&&name=="bash" {
  if !slices.Contains(omniFrozenNativeQuote(info).ClientTools,"bash"){return true,errors.New("该模型的原生 Bash 工具尚未通过验收")}
  return true,nil
 }
 if strings.HasPrefix(kind,"text_editor_")&&name=="str_replace_based_edit_tool" {
  if !slices.Contains(omniFrozenNativeQuote(info).ClientTools,"text_editor"){return true,errors.New("该模型的原生编辑器工具尚未通过验收")}
  return true,nil
 }
 return false,nil
}

func omniNativeHostedTool(info *relaycommon.RelayInfo,tool map[string]any) (bool,error) {
 _,ok:=info.Request.(*dto.ClaudeRequest);if !ok{return false,nil}
 kind,_:=tool["type"].(string);name,_:=tool["name"].(string)
 if !(strings.HasPrefix(kind,"web_search_")&&name=="web_search" || strings.HasPrefix(kind,"web_fetch_")&&name=="web_fetch"){return false,nil}
 price,ok:=OmniHostedToolPriceFor(info,name)
 if !ok{return true,errors.New("该模型的"+name+"能力或附加费用尚未通过验收")}
 if len(price.AcceptedTypes)>0&&!slices.Contains(price.AcceptedTypes,kind){return true,errors.New("该模型尚未验收此联网工具版本，请使用已开放版本")}
 if value,exists:=tool["max_uses"];exists {
  n,ok:=value.(float64);if !ok||n<=0||n!=float64(int(n))||n>10000{return true,errors.New("max_uses 必须为 1—10000 的整数")}
 }else{tool["max_uses"]=price.DefaultMaxUses}
 // Tools is intentionally an open native object: retain provider state and fields.
 return true,nil
}

// Reserve per-call fees separately from tokens; actual usage is settled once.
func OmniHostedToolReserveUSD(info *relaycommon.RelayInfo) (float64,error) {
 if info==nil||info.Request==nil{return 0,nil}
 data,err:=json.Marshal(info.Request);if err!=nil{return 0,err}
 var raw map[string]any;if err=json.Unmarshal(data,&raw);err!=nil{return 0,err}
 tools,_:=raw["tools"].([]any);total:=int64(0)
 for _,value:=range tools {
  tool,ok:=value.(map[string]any);if !ok{continue}
  name,_:=tool["name"].(string);price,ok:=OmniHostedToolPriceFor(info,name);if !ok{continue}
  count:=price.DefaultMaxUses;if n,ok:=tool["max_uses"].(float64);ok {if n<=0||n>10000||n!=math.Trunc(n){return 0,errors.New("invalid tool reserve")};count=int(n)}
  if price.RetailUSDMicros>0&&int64(count)>(math.MaxInt64-total)/price.RetailUSDMicros{return 0,errors.New("tool reserve overflow")}
  total+=int64(count)*price.RetailUSDMicros
 }
 return float64(total)/1000000,nil
}
