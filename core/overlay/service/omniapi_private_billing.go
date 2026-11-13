package service

import (
 "encoding/json"
 "errors"
 "fmt"
 "math"
 "strconv"
 "strings"

 "github.com/QuantumNous/new-api/common"
 "github.com/QuantumNous/new-api/pkg/billingexpr"
 relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// Private rules live only in the administrator option store. They never alter
// public quotes, groups or the customer's account settings.
type omniPrivateRule struct { Enabled bool `json:"enabled"`; Multiplier float64 `json:"multiplier"` }
type omniPrivateCost struct {
 Input float64 `json:"input_usd_per_million"`
 Output float64 `json:"output_usd_per_million"`
 Read float64 `json:"cache_read_usd_per_million"`
 Write float64 `json:"cache_write_usd_per_million"`
 WriteHour float64 `json:"cache_write_1h_usd_per_million"`
 Image json.RawMessage `json:"per_image_usd"`
 Tiers []struct { Min int `json:"min_tokens"`; Max *int `json:"max_tokens"`; Prices json.RawMessage `json:"prices"` } `json:"tiers"`
}
func omniPrivateMultiplier(uid int) float64 {
 common.OptionMapRWMutex.RLock();raw:=common.OptionMap["OmniAPIPrivateBilling"];common.OptionMapRWMutex.RUnlock()
 var rules map[string]omniPrivateRule
 if json.Unmarshal([]byte(raw),&rules)!=nil{return 0}
 rule:=rules[strconv.Itoa(uid)]
 if !rule.Enabled||rule.Multiplier<1||rule.Multiplier>10||math.IsNaN(rule.Multiplier)||math.IsInf(rule.Multiplier,0){return 0}
 return rule.Multiplier
}
func omniPrivateNumber(v float64) string { return strconv.FormatFloat(v,'f',12,64) }
func omniPrivateWrap(expression string) string {
 if strings.HasPrefix(expression,"fixed(")&&strings.HasSuffix(expression," * image_count") { return "v1:tier(\"base\", "+strings.TrimSuffix(expression," * image_count")+") * image_count" }
 return "v1:tier(\"base\", "+expression+")"
}
func omniPrivateCostExpression(raw json.RawMessage, multiplier float64) (string,error) {
 var cost omniPrivateCost
 if json.Unmarshal(raw,&cost)!=nil{return "",errors.New("invalid procurement quote")}
 if len(cost.Image)>0 {
  text:=strings.Trim(string(cost.Image),`"`);value,err:=strconv.ParseFloat(text,64)
  if err!=nil||value<=0||math.IsNaN(value)||math.IsInf(value,0){return "",errors.New("invalid image procurement")}
  return "fixed("+omniPrivateNumber(value*multiplier)+") * image_count",nil
 }
 if cost.Input<=0||cost.Output<=0{return "",errors.New("missing procurement quote")}
 var dimensions map[string]json.RawMessage
 _=json.Unmarshal(raw,&dimensions)
 if _,ok:=dimensions["cache_read_usd_per_million"];!ok{cost.Read=cost.Input}
 if _,ok:=dimensions["cache_write_usd_per_million"];!ok{cost.Write=cost.Input}
 if _,ok:=dimensions["cache_write_1h_usd_per_million"];!ok{cost.WriteHour=cost.Input}
 values:=[]float64{cost.Input,cost.Output,cost.Read,cost.Write,cost.WriteHour}
 names:=[]string{"p","c","cr","cc","cc1h"};terms:=[]string{}
 for i,v:=range values {
  if v<0||math.IsNaN(v)||math.IsInf(v,0){return "",errors.New("invalid procurement dimension")}
  terms=append(terms,names[i]+" * "+omniPrivateNumber(v*multiplier))
 }
 expression:="("+strings.Join(terms," + ")+")"
 for n:=len(cost.Tiers)-1;n>=0;n-- {
  tier:=cost.Tiers[n];nested,err:=omniPrivateCostExpression(tier.Prices,multiplier);if err!=nil{return "",err}
  condition:=fmt.Sprintf("len >= %d",tier.Min)
  if tier.Max!=nil {condition+=fmt.Sprintf(" && len <= %d",*tier.Max)}
  expression="("+condition+" ? "+nested+" : "+expression+")"
 }
 return expression,nil
}
func omniPrivateQuotes(info *relaycommon.RelayInfo) (map[int]json.RawMessage,error) {
 var quote struct { Costs map[int]json.RawMessage `json:"costs"` }
 if json.Unmarshal(info.OmniPriceQuote,&quote)!=nil||len(quote.Costs)==0{return nil,errors.New("missing model procurement")}
 return quote.Costs,nil
}
// Reserve against every enabled exact-model route. Settlement uses only the
// successful route and the request-frozen procurement quote, including caches.
func OmniPreparePrivateBilling(info *relaycommon.RelayInfo) error {
 if info==nil||info.OmniPrivateCostMultiplier>0{return nil}
 multiplier:=omniPrivateMultiplier(info.UserId);if multiplier==0{return nil}
 quotes,err:=omniPrivateQuotes(info);if err!=nil{return errors.New("当前型号计费配置待核验，请联系客服")}
 ids:=GetOmniRouting().Models[info.OriginModelName]
 if len(ids)==0 {ids=[]int{info.GetChannelID()}}
 expressions:=[]string{}
 for _,id:=range ids {
  expression,err:=omniPrivateCostExpression(quotes[id],multiplier)
  if err!=nil{return errors.New("当前型号计费配置待核验，请联系客服")}
  expressions=append(expressions,expression)
 }
 expression:=expressions[0]
 for _,next:=range expressions[1:] {
  if strings.HasPrefix(expression,"fixed(")&&strings.HasPrefix(next,"fixed(") {
   a,_:=strconv.ParseFloat(strings.TrimSuffix(strings.TrimPrefix(expression,"fixed("),") * image_count"),64)
   b,_:=strconv.ParseFloat(strings.TrimSuffix(strings.TrimPrefix(next,"fixed("),") * image_count"),64)
   if b>a {expression=next}
  } else {expression="max("+expression+", "+next+")"}
 }
 info.OmniPrivateCostMultiplier=multiplier
 info.OmniPriceExpression=omniPrivateWrap(expression)
 return nil
}
func omniPrivateSelectedExpression(info *relaycommon.RelayInfo) (string,error) {
 quotes,err:=omniPrivateQuotes(info);if err!=nil{return "",err}
 expression,err:=omniPrivateCostExpression(quotes[info.GetChannelID()],info.OmniPrivateCostMultiplier)
 if err!=nil{return "",err}
 return omniPrivateWrap(expression),nil
}
func omniPrivateSettlementSnapshot(info *relaycommon.RelayInfo,snap *billingexpr.BillingSnapshot) *billingexpr.BillingSnapshot {
 if info.OmniPrivateCostMultiplier<=0||snap==nil{return snap}
 expression,err:=omniPrivateSelectedExpression(info)
 if err!=nil {common.SysError("private billing settlement procurement unavailable");return snap}
 frozen:=*snap;frozen.ExprString=expression;frozen.ExprHash=billingexpr.ExprHashString(expression);frozen.GroupRatio=1
 return &frozen
}
