package model

import (
 "context"
 "encoding/json"
 "gorm.io/gorm"
 "errors"
 "math"
 "reflect"
 "slices"
 "sync"
 "strconv"
 "time"
 "github.com/QuantumNous/new-api/common"
)

var omniCapabilityLoadMutex sync.Mutex

var omniCapabilityOptionKeys=[]string{"OmniAPIPricing","OmniAPIRouting","OmniAPIModelRegistry","OmniAPICommerceV3","OmniAPICommerceCapabilities"}

type OmniCapabilityPublicationInput struct {
 Profile OmniCommerceProfile `json:"profile"`
 Pricing string `json:"pricing"`
 Routing string `json:"routing"`
 Registry string `json:"registry"`
 EvidenceHash string `json:"evidence_hash"`
 PrimaryRoutes map[string]int `json:"primary_routes,omitempty"`
 RefreshPrimaryRoutes map[string]int `json:"refresh_primary_routes,omitempty"`
 NativeSubscriptionRoutes map[string]int `json:"native_subscription_routes,omitempty"`
}

type omniRequestOptionsKey struct{}

func omniRequestDB(snapshots ...map[string]string) *gorm.DB {
 if len(snapshots)==0 || snapshots[0]==nil {return DB}
 return DB.WithContext(context.WithValue(context.Background(),omniRequestOptionsKey{},snapshots[0]))
}

func omniRequestOptionsFor(tx *gorm.DB) map[string]string {
 if tx != nil && tx.Statement != nil && tx.Statement.Context != nil {
  if snapshot,ok:=tx.Statement.Context.Value(omniRequestOptionsKey{}).(map[string]string);ok{return snapshot}
 }
 return OmniRequestOptionSnapshot()
}

func OmniRequestOptionSnapshot() map[string]string {
 common.OptionMapRWMutex.RLock()
 defer common.OptionMapRWMutex.RUnlock()
 values := make(map[string]string,len(omniCapabilityOptionKeys)+1)
 for _,key := range omniCapabilityOptionKeys {values[key]=common.OptionMap[key]}
 values["OmniAPIResponsesFailover"]=common.OptionMap["OmniAPIResponsesFailover"]
 return values
}

func omniCapabilityStoredOptions() (map[string]string,error) {
 var rows []Option
 if err:=DB.Where("key IN ?",omniCapabilityOptionKeys).Find(&rows).Error;err!=nil{return nil,err}
 out:=map[string]string{}
 for _,key:=range omniCapabilityOptionKeys{out[key]=""}
 for _,row:=range rows{out[row.Key]=row.Value}
 return out,nil
}

func omniJSONSame(a,b any) bool {
 left,_:=json.Marshal(a);right,_:=json.Marshal(b)
 var x,y any
 return json.Unmarshal(left,&x)==nil&&json.Unmarshal(right,&y)==nil&&reflect.DeepEqual(x,y)
}

// Preview is read-only. Operators commit exactly its sealed values in one DB
// transaction, then reload the coherent option set through the root endpoint.
func OmniPreviewCapabilityPublication(input OmniCapabilityPublicationInput,actor int) (map[string]any,error) {
 if actor<=0||len(input.EvidenceHash)!=64{return nil,errors.New("能力发布缺少批准与验收摘要")}
 if len(input.RefreshPrimaryRoutes)>0 {
  if len(input.NativeSubscriptionRoutes)>0 {return nil,errors.New("换线整改不能混用原生订阅发布") }
  if input.PrimaryRoutes==nil {input.PrimaryRoutes=map[string]int{}}
  for name,id:=range input.RefreshPrimaryRoutes {
   if _,duplicate:=input.PrimaryRoutes[name];duplicate {return nil,errors.New("重复的首选线路验收")}
   input.PrimaryRoutes[name]=id
  }
 }
 before,err:=omniCapabilityStoredOptions();if err!=nil{return nil,err}
 var cfg OmniCommerceSwitches
 if json.Unmarshal([]byte(before["OmniAPICommerceV3"]),&cfg)!=nil{return nil,errors.New("商品配置不可用")}
 var current OmniCommerceProfile
 if err=DB.First(&current,"id = ?",cfg.ProfileID).Error;err!=nil{return nil,err}
 p:=input.Profile
 var existingID int64
 if err=DB.Model(&OmniCommerceProfile{}).Where("id = ?",p.ID).Count(&existingID).Error;err!=nil{return nil,err}
 if existingID!=0{return nil,errors.New("能力发布必须使用新的不可变权益版本")}
 if p.PriceVersion!=current.PriceVersion||p.PriceSnapshot!=current.PriceSnapshot||p.FXMicros!=current.FXMicros||p.WorstCostPPM!=current.WorstCostPPM||p.PaymentFeePPM!=current.PaymentFeePPM||p.RiskReservePPM!=current.RiskReservePPM||p.FixedFeeCents!=current.FixedFeeCents||p.CapabilityReleaseVersion!="" {return nil,errors.New("能力扩展不能改变商品价格或已确认的额度生成参数")}
 p.PricingHash=omniCapabilityOptionHash(input.Pricing);p.RoutingHash=omniCapabilityOptionHash(input.Routing);p.ApprovedBy=actor;p.ApprovedAt=time.Now().Unix();p.ContentHash=""
 if p.AuditExpiresAt<=p.ApprovedAt||p.AuditExpiresAt>p.ApprovedAt+7*86400{return nil,errors.New("能力核价有效期无效")}
 if err=p.Validate();err!=nil{return nil,err};if _,err=p.Plans();err!=nil{return nil,err}
 var registry,oldRegistry OmniModelRegistry
 if json.Unmarshal([]byte(input.Registry),&registry)!=nil||json.Unmarshal([]byte(before["OmniAPIModelRegistry"]),&oldRegistry)!=nil{return nil,errors.New("模型注册表损坏")}
 if err=OmniValidateRegistry(registry);err!=nil{return nil,err}
 for name,old:=range oldRegistry.Models {
  next,ok:=registry.Models[name];if !ok{return nil,errors.New("不能删除历史模型身份")}
  for _,alias:=range old.Aliases {found:=false;for _,a:=range next.Aliases {if a==alias{found=true}};if !found{return nil,errors.New("不能删除或改派历史模型别名")}}
 }
 var oldPrice,newPrice struct {Models map[string]json.RawMessage `json:"models"`}
 var oldRouting,newRouting struct {Models map[string][]int `json:"models"`}
 if json.Unmarshal([]byte(before["OmniAPIPricing"]),&oldPrice)!=nil||json.Unmarshal([]byte(input.Pricing),&newPrice)!=nil||json.Unmarshal([]byte(before["OmniAPIRouting"]),&oldRouting)!=nil||json.Unmarshal([]byte(input.Routing),&newRouting)!=nil{return nil,errors.New("模型价格或路由损坏")}
 for name,row:=range oldPrice.Models {
  if _, switching := input.PrimaryRoutes[name]; switching {continue}
  if !omniJSONSame(row,newPrice.Models[name])||!reflect.DeepEqual(oldRouting.Models[name],newRouting.Models[name]){return nil,errors.New("自动能力发布只能添加型号，不能重写已有模型价格或路由")}
 }
 var scope map[string][]int
 _=json.Unmarshal([]byte(p.Models),&scope)
 var previousScope map[string][]int
 if json.Unmarshal([]byte(current.Models),&previousScope)!=nil{return nil,errors.New("已有模型范围损坏")}
 nativeExtension:=len(input.NativeSubscriptionRoutes)>0
 if nativeExtension && len(input.PrimaryRoutes)>0{return nil,errors.New("订阅能力扩展须单独发布")}
 if len(input.PrimaryRoutes)>0 {
  if err=omniValidatePrimaryPublication(input,before,oldRegistry,registry,previousScope,scope,p.WorstCostPPM);err!=nil{return nil,err}
 } else if !nativeExtension {
  for name,routes:=range previousScope {if !reflect.DeepEqual(routes,scope[name]){return nil,errors.New("能力补充不能移除已有模型或主备通道")}}
 }
 for name,ids:=range scope {
  // Primary validation already compares every subscription route against the
  // previous subscription scope. The wallet chain can include additional routes.
  if len(input.PrimaryRoutes)==0 && !nativeExtension {
   expected:=newRouting.Models[name]
   if prior,existing:=previousScope[name];existing{expected=prior}
   if !reflect.DeepEqual(ids,expected){return nil,errors.New("核验范围与实际主备路由不一致")}
  }
  identity,ok:=registry.Models[name];if !ok{return nil,errors.New("范围内模型未注册")}
  for _,id:=range ids {found:=false;for _,b:=range identity.Bindings{if b.ChannelID==id&&b.Status=="verified"{found=true}};if !found{return nil,errors.New("范围内主备通道缺少验收")}}
  if _,old:=oldPrice.Models[name];old{continue}
  var price struct {Retail map[string]string `json:"retail"`;Costs map[string]json.RawMessage `json:"costs"`}
  if json.Unmarshal(newPrice.Models[name],&price)!=nil{return nil,errors.New("新增模型缺少核价")}
  for _,id:=range ids {
   var cost map[string]json.RawMessage;if json.Unmarshal(price.Costs[strconv.Itoa(id)],&cost)!=nil{return nil,errors.New("新增通道缺少采购估算")}
   var complete bool;_ = json.Unmarshal(cost["complete_prices_verified"],&complete)
   if !complete&&!p.UsesMultiplierEstimate(){return nil,errors.New("新增通道报价未核验且未批准估算")}
   snapshots:=[]map[string]json.RawMessage{cost}
   var tiers []struct{Prices map[string]json.RawMessage `json:"prices"`};_ = json.Unmarshal(cost["tiers"],&tiers)
   for _,tier:=range tiers{snapshots=append(snapshots,tier.Prices)}
   for _,snapshot:=range snapshots {for dimension,key:=range map[string]string{"input":"input_usd_per_million","output":"output_usd_per_million","cache_read":"cache_read_usd_per_million","cache_write":"cache_write_usd_per_million","cache_write_1h":"cache_write_1h_usd_per_million"} {
    var amount float64
    raw,exists:=snapshot[key];if !exists||json.Unmarshal(raw,&amount)!=nil||math.IsNaN(amount)||math.IsInf(amount,0)||amount<0{return nil,errors.New("新增模型计价维度不完整，缺项不能当零")}
    retail,err:=strconv.ParseFloat(price.Retail[dimension],64)
    if err!=nil||math.IsNaN(retail)||math.IsInf(retail,0)||retail<0||amount>retail*float64(p.WorstCostPPM)/1000000+1e-12{return nil,errors.New("新增模型采购估算超过已售权益成本约束")}
   }}
  }
 }
 var matrix OmniCapabilityMatrix
 if json.Unmarshal([]byte(p.CapabilityMatrix),&matrix)!=nil||matrix.ScopeVersion==""||matrix.InventoryHash!=input.EvidenceHash{return nil,errors.New("能力范围缺少独立版本与验收摘要")}
 var previousMatrix OmniCapabilityMatrix
 if current.CapabilityMatrix!="" || len(input.PrimaryRoutes)>0 || nativeExtension {
  if json.Unmarshal([]byte(current.CapabilityMatrix),&previousMatrix)!=nil{return nil,errors.New("既有订阅能力矩阵损坏")}
 }
 if nativeExtension {
  if err=omniValidateNativeSubscriptionPublication(input,before,registry,previousScope,scope,previousMatrix,matrix,p.WorstCostPPM);err!=nil{return nil,err}
 } else if !omniJSONSame(previousMatrix.NativeMessageOnly,matrix.NativeMessageOnly) {
  expected:=map[string]int{}
  for name,id:=range previousMatrix.NativeMessageOnly {
   if input.RefreshPrimaryRoutes[name]==id && len(matrix.Bounds[name][id])>1 {continue}
   expected[name]=id
  }
  if !omniJSONSame(expected,matrix.NativeMessageOnly) && (len(expected)!=0 || len(matrix.NativeMessageOnly)!=0) {
   return nil,errors.New("未核验的原生通道变更")
  }
 }
 if len(input.PrimaryRoutes)>0 || nativeExtension {
  if err=omniValidatePrimaryBounds(previousMatrix,matrix,input.RefreshPrimaryRoutes);err!=nil{return nil,err}
 }
 if err=omniValidateScopedChannelPaths(input,registry,previousMatrix,matrix);err!=nil{return nil,err}
 if err=OmniCommerceLaunchReadySnapshot(p,input.Pricing,input.Routing);err!=nil{return nil,err}
 sealed,_:=json.Marshal(p);p.ContentHash=omniCapabilityOptionHash(string(sealed))
 release:=OmniCommerceCapabilityRelease{Version:matrix.ScopeVersion,ApprovedBy:actor,ApprovedAt:p.ApprovedAt,EvidenceHash:input.EvidenceHash,Profile:p,SourceProfiles:map[string]bool{},CompatiblePairs:[]string{current.PricingHash+":"+current.RoutingHash}}
 var profiles []OmniCommerceProfile
 if err=DB.Find(&profiles).Error;err!=nil{return nil,err}
 var previous OmniCommerceCapabilityRelease
 if json.Unmarshal([]byte(before["OmniAPICommerceCapabilities"]),&previous)==nil {for _,pair:=range previous.CompatiblePairs {found:=false;for _,existing:=range release.CompatiblePairs{if existing==pair{found=true}};if !found{release.CompatiblePairs=append(release.CompatiblePairs,pair)}}}
 if len(input.PrimaryRoutes)>0 || nativeExtension {
  release.PreserveSources=make(map[string]bool)
  priorPricing:=omniCapabilityOptionHash(before["OmniAPIPricing"])
  priorRouting:=omniCapabilityOptionHash(before["OmniAPIRouting"])
  if priorPricing!=current.PricingHash||priorRouting!=current.RoutingHash{return nil,errors.New("既有订阅核价与线上配置不一致，先恢复当前兼容版本")}
  for _,old:=range profiles {
   if len(old.ContentHash)!=64{continue}
   if old.ID==current.ID || previous.SourceProfiles[old.ContentHash]&&old.WorstCostPPM>=p.WorstCostPPM {release.SourceProfiles[old.ContentHash]=true;continue}
   if previous.PreserveSources[old.ContentHash] || old.PricingHash==priorPricing&&old.RoutingHash==priorRouting {release.PreserveSources[old.ContentHash]=true}
  }
 } else {
  release.PreserveSources=previous.PreserveSources
  for _,old:=range profiles {if old.WorstCostPPM>=p.WorstCostPPM&&len(old.ContentHash)==64&&!release.PreserveSources[old.ContentHash]{release.SourceProfiles[old.ContentHash]=true}}
 }
 release.SourceProfiles[p.ContentHash]=true
 cfg.ProfileID=p.ID
 cfgRaw,_:=json.Marshal(cfg);releaseRaw,_:=json.Marshal(release)
 values:=map[string]string{"OmniAPIPricing":input.Pricing,"OmniAPIRouting":input.Routing,"OmniAPIModelRegistry":input.Registry,"OmniAPICommerceV3":string(cfgRaw),"OmniAPICommerceCapabilities":string(releaseRaw)}
 expected:=map[string]string{};for key,value:=range before{expected[key]=omniCapabilityOptionHash(value)}
 return map[string]any{"profile":p,"release":release,"values":values,"expected_hashes":expected,"models":len(scope),"financial_snapshots_changed":false},nil
}

func OmniReloadCapabilityOptions() (string,error) {
 omniCapabilityLoadMutex.Lock()
 defer omniCapabilityLoadMutex.Unlock()
 values,err:=omniCapabilityStoredOptions();if err!=nil{return "",err}
 version,err:=omniInstallCapabilityOptions(values,true)
 if err==nil {InitChannelCache()}
 return version,err
}

func omniIsCapabilityOption(key string) bool {return slices.Contains(omniCapabilityOptionKeys,key)}

func omniInstallCapabilityOptions(values map[string]string, verifySales bool) (string,error) {
 var err error
 var cfg OmniCommerceSwitches;var release OmniCommerceCapabilityRelease;var registry OmniModelRegistry
 if json.Unmarshal([]byte(values["OmniAPICommerceV3"]),&cfg)!=nil||json.Unmarshal([]byte(values["OmniAPICommerceCapabilities"]),&release)!=nil||json.Unmarshal([]byte(values["OmniAPIModelRegistry"]),&registry)!=nil{return "",errors.New("能力发布数据不完整")}
 if release.Profile.ID!=cfg.ProfileID||release.Profile.PricingHash!=omniCapabilityOptionHash(values["OmniAPIPricing"])||release.Profile.RoutingHash!=omniCapabilityOptionHash(values["OmniAPIRouting"]){return "",errors.New("拒绝加载不一致的能力发布")}
 var stored OmniCommerceProfile
 if err=DB.First(&stored,"id = ?",cfg.ProfileID).Error;err!=nil||stored.ContentHash!=release.Profile.ContentHash{return "",errors.New("能力权益快照未正确提交")}
 if err=OmniValidateRegistry(registry);err!=nil{return "",err}
 if verifySales && cfg.SalesEnabled {if err=OmniCommerceLaunchReadySnapshot(stored,values["OmniAPIPricing"],values["OmniAPIRouting"]);err!=nil{return "",err}}
 common.OptionMapRWMutex.Lock();for key,value:=range values{common.OptionMap[key]=value};common.OptionMapRWMutex.Unlock()
 return release.Version,nil
}
