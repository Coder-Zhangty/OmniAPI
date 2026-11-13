package model

import (
 "encoding/json"
 "errors"
 "time"

 "github.com/QuantumNous/new-api/common"
 "gorm.io/gorm"
 "gorm.io/gorm/clause"
)

func omniCustomerSharedReserveTx(tx *gorm.DB,p OmniSharedPool,period OmniSharedPeriod,m OmniSharedMember,uid,tokenID int,id,name string,estimate int64,out *OmniSharedHold)error{
 if e:=omniCustomerDebtCheck(tx,p.OwnerID);e!=nil{return e}
 var total struct{Granted,Used int64}
 if e:=tx.Model(&OmniSharedBatch{}).Where("period_id = ?",period.ID).Select("COALESCE(SUM(granted),0) AS granted,COALESCE(SUM(used),0) AS used").Scan(&total).Error;e!=nil{return e}
 if total.Granted<=total.Used{return ErrOmniSharedUnavailable}
 var spent int64
 if e:=tx.Model(&OmniSharedHold{}).Where("period_id = ? AND user_id = ? AND state <> ?",period.ID,uid,"pending").Select("COALESCE(SUM(cost_used_micros + wallet_cost_micros),0)").Scan(&spent).Error;e!=nil{return e}
 if m.BudgetMicros>=0&&spent>=m.BudgetMicros{return ErrOmniSharedUnavailable}
 var profile OmniCommerceProfile;if e:=json.Unmarshal([]byte(period.ProfileJSON),&profile);e!=nil{return e}
 scope:=omniFreezeCapabilityProfileFrom(period.ProfileJSON,omniRequestOptionsFor(tx));now:=time.Now().Unix()
 *out=OmniSharedHold{BillingPolicy:OmniCustomerBillingV1,PayerID:p.OwnerID,EstimatedQuota:estimate,Owner:OmniRiskProcessID,DispatchState:"reserved",RequestID:id,PoolID:p.ID,PeriodID:period.ID,UserID:uid,TokenID:tokenID,Model:name,ProfileJSON:scope,Allocations:"[]",Reserved:0,CostHoldMicros:0,State:"pending",CreatedAt:now}
 r:=OmniReservation{RequestID:id,UserID:uid,PayerID:p.OwnerID,Source:"omni_shared",SharedPoolID:p.ID,SharedTokenID:tokenID,CommerceProfile:scope,EstimatedQuota:estimate,Reserved:0,State:"pending",CreatedAt:now,FlexibleAllowance:true}
 omniCustomerSnapshot(&r,name,profile.FXMicros,omniRequestOptionsFor(tx))
 if e:=tx.Create(out).Error;e!=nil{return e};return tx.Create(&r).Error
}

func omniCustomerSharedSettle(lookup OmniSharedHold,actual int64,audit *OmniSharedRecovery)(bool,error){
 changed:=false;tokenID:=0
 defer invalidateUserCache(lookup.PayerID)
 e:=DB.Transaction(func(tx *gorm.DB)error{
  p,e:=omniCommercePool(tx,lookup.PoolID);if e!=nil{return e}
  var h OmniSharedHold;if e=tx.Clauses(clause.Locking{Strength:"UPDATE"}).First(&h,"request_id = ?",lookup.RequestID).Error;e!=nil{return e}
  if audit!=nil {
   if audit.automatic {
    if h.State!="pending"||h.DispatchState!="reserved"{return nil}
    var proc OmniSharedProcess;if e=tx.Where("id = ?",h.Owner).Find(&proc).Error;e!=nil{return e}
    if h.Owner==OmniRiskProcessID||proc.HeartbeatAt>common.GetTimestamp()-120{return nil}
   }
   audit.PoolID=h.PoolID
   var prior OmniSharedRecovery
   e=tx.First(&prior,"key = ?",audit.Key).Error
   if e==nil {if prior.RequestID!=h.RequestID||prior.Actual!=actual||prior.Evidence!=audit.Evidence||prior.ActorID!=audit.ActorID{return errors.New("recovery key conflict")};return nil}
   if !errors.Is(e,gorm.ErrRecordNotFound){return e}
   if !audit.automatic&&(h.State!="pending"||h.DispatchState!="unknown"){return errors.New("不是待核对的重启请求")}
   if e=tx.Create(audit).Error;e!=nil{return e}
  }
  if h.State!="pending"{if h.Actual!=actual{return errors.New("shared settlement replay conflict")};return nil}
  var r OmniReservation;if e=tx.Clauses(clause.Locking{Strength:"UPDATE"}).First(&r,"request_id = ?",h.RequestID).Error;e!=nil{return e}
  if r.BillingPolicy!=h.BillingPolicy||r.PayerID!=h.PayerID||r.State!="pending"{return errors.New("shared settlement identity conflict")}
  // Same user row serializes personal debits and settlements from every pool.
  var payer User;if e=tx.Clauses(clause.Locking{Strength:"UPDATE"}).Select("id","quota").First(&payer,r.PayerID).Error;e!=nil{return e}
  var batches []OmniSharedBatch;if e=tx.Clauses(clause.Locking{Strength:"UPDATE"}).Where("period_id = ?",h.PeriodID).Order("sequence ASC").Find(&batches).Error;e!=nil{return e}
  remaining:=actual;cost:=int64(0);alloc:=[]OmniSharedAllocation{}
  for i:=range batches{
   b:=&batches[i];q:=min(remaining,max(0,b.Granted-b.Used-b.Reserved));if q==0{continue}
   paid,e:=omniIntRatio(b.Used+q,b.PriceCents*10000,b.Granted,false);if e!=nil{return e};delta:=paid-b.CostUsedMicros
   if delta<0{return errors.New("batch cost invariant failed")}
   cost+=delta;alloc=append(alloc,OmniSharedAllocation{BatchID:b.ID,Quota:q,CostHoldMicros:delta});b.Used+=q;b.CostUsedMicros=paid;remaining-=q
   if e=tx.Save(b).Error;e!=nil{return e};if remaining==0{break}
  }
  r.AllowanceActual=actual-remaining
  if e=omniCustomerWalletDebit(tx,&r,remaining);e!=nil{return e}
  r.Actual=actual;r.PlatformQuota=0;r.State="settled";if actual==0{r.State="refunded"}
  if e=omniCustomerTokenSettle(tx,&r,actual);e!=nil{return e};tokenID=r.BillingTokenID
  if e=tx.Save(&r).Error;e!=nil{return e}
  raw,_:=json.Marshal(alloc);h.Allocations=string(raw);h.Actual=actual;h.AllowanceActual=r.AllowanceActual;h.WalletActual=r.WalletActual;h.WalletCostMicros=r.WalletCNYMicros;h.PlatformQuota=0;h.CostUsedMicros=cost;h.State=r.State;h.SettledAt=time.Now().Unix()
  if e=tx.Save(&h).Error;e!=nil{return e};changed=true
  return omniSharedAdvance(tx,&p,time.Now().Unix())
 })
 if e==nil{omniCustomerInvalidateToken(tokenID)}
 return changed,e
}
