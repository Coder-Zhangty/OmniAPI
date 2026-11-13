package model
import("encoding/json";"regexp";"strings")
var omniSupplierPattern=regexp.MustCompile(`(?i)(https?://|hapiopen|api\.0-0|(^|[^0-9])0-0([^0-9]|$)|H[ -]?API|\(O[—–-]O\))`)
func OmniUserMessage(text string)string{if omniSupplierPattern.MatchString(text){return "服务暂不可用，请稍后重试；可凭请求编号联系客服"};return text}
func OmniPublicPriceVersion(version string)string{if version==""{return ""};return "price-"+omniHash(version)[:12]}
func OmniPublicLogOther(text string)string{
 var m map[string]json.RawMessage;if json.Unmarshal([]byte(text),&m)!=nil{return "{}"}
 changed:=false
 for key,value:=range m{
  k:=strings.ToLower(key)
  if strings.Contains(k,"channel") || strings.Contains(k,"upstream") || strings.Contains(k,"supplier") || strings.Contains(k,"cost") || k=="base_url" || k=="sources_by_dimension" || k=="reference_model"{delete(m,key);changed=true;continue}
  if omniSupplierPattern.Match(value){delete(m,key);changed=true}
 }
 if !changed{return text};raw,_:=json.Marshal(m);return string(raw)
}
func OmniPausedReason(name string)string{if strings.Contains(name,"image") || strings.Contains(name,"audio") || strings.Contains(name,"video") || strings.HasPrefix(name,"sora-"){return "功能暂未开放"};return "暂不可用"}
