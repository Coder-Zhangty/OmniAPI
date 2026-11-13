package model

import (
 "errors"
 "fmt"
 "strings"
 "time"

 "github.com/QuantumNous/new-api/common"
 "github.com/shopspring/decimal"
 "gorm.io/gorm"
 "gorm.io/gorm/clause"
)

const OmniCustomerBillingV1 = "customer_actual_v1"
var ErrOmniAccountDebt = errors.New("account_in_debt: 付款账户欠费，请补足欠款后重试；在途请求继续结算")

func OmniCustomerBillingEnabled() bool { return GetOmniPolicy().CustomerOveragePolicy == OmniCustomerBillingV1 }
func OmniCustomerBillingFor(uid int)bool{
 p:=GetOmniPolicy();if p.CustomerOveragePolicy!=OmniCustomerBillingV1{return false}
 if len(p.CustomerOverageUserIDs)==0{return true}
 for _,id:=range p.CustomerOverageUserIDs{if id==uid{return true}};return false
}
func OmniCustomerPublicPolicy(uid int)string{if OmniCustomerBillingFor(uid){return OmniCustomerBillingV1};return ""}

// Windows survive subscription replacement/reset. Pending requests retain these
// exact keys; settlement never takes credit from a later period.
type OmniAllowanceWindow struct {
 UserID int `gorm:"primaryKey;autoIncrement:false"`
 Start int64 `gorm:"primaryKey;autoIncrement:false"`
 Kind string `gorm:"primaryKey;size:8"`
 Epoch int64 `gorm:"primaryKey;autoIncrement:false"`
 Limit int64
 Used int64
}

func omniCustomerWindow(tx *gorm.DB, s OmniSubscription, kind string, epoch, limit, used int64) (OmniAllowanceWindow,error) {
 w:=OmniAllowanceWindow{UserID:s.UserID,Start:s.Start,Kind:kind,Epoch:epoch,Limit:limit,Used:used}
 if e:=tx.Clauses(clause.OnConflict{DoNothing:true}).Create(&w).Error;e!=nil{return w,e}
 e:=tx.Clauses(clause.Locking{Strength:"UPDATE"}).First(&w,"user_id = ? AND start = ? AND kind = ? AND epoch = ?",s.UserID,s.Start,kind,epoch).Error
 return w,e
}

// Reset callers already hold the account lock and reject unfinished requests.
func omniCustomerResetWindows(tx *gorm.DB,s OmniSubscription,five,week bool)error{
 for _,kind:=range []string{"five","week"}{
  if kind=="five"&&!five||kind=="week"&&!week{continue}
  epoch,used:=s.WeekEpoch,s.WeekUsed;if kind=="five"{epoch,used=s.FiveEpoch,s.FiveUsed}
  if e:=tx.Model(&OmniAllowanceWindow{}).Where("user_id = ? AND start = ? AND kind = ? AND epoch = ?",s.UserID,s.Start,kind,epoch).UpdateColumn("used",used).Error;e!=nil{return e}
 }
 return nil
}

func omniCustomerSnapshot(r *OmniReservation, name string, fallbackFX int64, snapshots ...map[string]string) {
 prices:=GetOmniPrices()
 if len(snapshots)>0 && snapshots[0]!=nil {prices=OmniPricesFromSnapshot(snapshots[0]["OmniAPIPricing"])}
 r.BillingPolicy=OmniCustomerBillingV1;r.BillingPriceVersion=prices.Version
 r.BillingPriceSnapshot=string(prices.Models[name])
 rate,e:=decimal.NewFromString(strings.Trim(string(prices.RiskFX),"\""))
 if e==nil&&rate.GreaterThanOrEqual(decimal.NewFromInt(1))&&rate.LessThanOrEqual(decimal.NewFromInt(20)){r.BillingFXMicros=rate.Mul(decimal.NewFromInt(1000000)).Round(0).IntPart()}else{r.BillingFXMicros=fallbackFX}
}

func omniCustomerDebtCheck(tx *gorm.DB, uid int) error {
 var u User
 if e:=tx.Clauses(clause.Locking{Strength:"UPDATE"}).Select("id","quota").First(&u,uid).Error;e!=nil{return e}
 if u.Quota<0{return ErrOmniAccountDebt};return nil
}

func omniCustomerReserveTx(tx *gorm.DB, uid int, requestID, name string, estimate int64, mode,poolID string,tokenID int,profile string,a OmniAccount,out *OmniReservation) error {
 if e:=omniCustomerDebtCheck(tx,uid);e!=nil{return e}
 now:=time.Now().Unix();s:=OmniSubscription{}
 e:=tx.Clauses(clause.Locking{Strength:"UPDATE"}).First(&s,"user_id = ?",uid).Error
 active:=e==nil
 if e!=nil&&!errors.Is(e,gorm.ErrRecordNotFound){return e}
 if active {
  if e=s.advance(now);e!=nil{return e};if s.ActivationPending{return errors.New("订阅生效处理中，请稍后重试")}
  if e=tx.Save(&s).Error;e!=nil{return e};active=s.Start<=now&&s.End>now
 }
 if active&&poolID==""&&mode!="wallet_only"&&s.CommerceProfile!=""&&!s.Includes(name){return errors.New("该模型不在订阅核价范围，不会转扣钱包")}
 source:="wallet"
 if active&&mode!="wallet_only"&&s.Includes(name)&&!s.paused() {
  w,e:=omniCustomerWindow(tx,s,"week",s.WeekEpoch,s.WeekLimit,s.WeekUsed);if e!=nil{return e};available:=w.Limit-w.Used
  if s.FiveEnabled(){f,e:=omniCustomerWindow(tx,s,"five",s.FiveEpoch,s.FiveLimit,s.FiveUsed);if e!=nil{return e};available=min(available,f.Limit-f.Used)}
  if available>0{source="omni_subscription"}
 }
 if source=="wallet" {
  if mode=="subscription_only"||active&&!a.WalletOverflow{return omniAllowanceQuotaError(s,name,estimate)}
  var u User;if e:=tx.Select("id","quota").First(&u,uid).Error;e!=nil{return e};if u.Quota<=0{return omniWalletQuotaError(tx,uid,1)}
 }
 if profile==""&&source=="omni_subscription"{profile=s.CommerceProfile}
 *out=OmniReservation{RequestID:requestID,UserID:uid,PayerID:uid,Source:source,SharedPoolID:poolID,SharedTokenID:tokenID,CommerceProfile:omniFreezeCapabilityProfileFrom(profile,omniRequestOptionsFor(tx)),EstimatedQuota:estimate,Reserved:0,State:"pending",CreatedAt:now,FlexibleAllowance:true,SubscriptionStart:s.Start,FiveEpoch:s.FiveEpoch,WeekEpoch:s.WeekEpoch,AllowanceFiveEnabled:s.FiveEnabled()}
 omniCustomerSnapshot(out,name,s.FxMicros,omniRequestOptionsFor(tx))
 return tx.Create(out).Error
}

func omniCustomerWalletDebit(tx *gorm.DB,r *OmniReservation,amount int64) error {
 if amount<0{return errors.New("negative wallet debit")}
 if amount==0{return nil}
 if r.PayerID<=0{return errors.New("missing locked payer")}
 result:=tx.Model(&User{}).Where("id = ?",r.PayerID).UpdateColumn("quota",gorm.Expr("quota - ?",amount))
 if result.Error!=nil{return result.Error};if result.RowsAffected!=1{return errors.New("missing locked payer wallet")}
 r.WalletActual=amount
 if r.BillingFXMicros>0 {
  r.WalletCNYMicros=decimal.NewFromInt(amount).Mul(decimal.NewFromInt(r.BillingFXMicros)).Div(decimal.NewFromFloat(common.QuotaPerUnit)).Round(0).IntPart()
 }
 return nil
}

func omniCustomerAdjust(id string,actual int64,final bool)(bool,error){
 if !final{return false,nil}
 var owner OmniReservation;if e:=DB.First(&owner,"request_id = ?",id).Error;e!=nil{return false,e}
 defer invalidateUserCache(owner.PayerID)
 changed:=false
 e:=DB.Transaction(func(tx *gorm.DB)error{
  if _,e:=omniAccount(tx,owner.PayerID);e!=nil{return e}
  var r OmniReservation;if e:=tx.Clauses(clause.Locking{Strength:"UPDATE"}).First(&r,"request_id = ?",id).Error;e!=nil{return e}
  if r.State!="pending"{if r.Actual!=actual{return errors.New("settlement replay conflict")};return nil}
  covered:=int64(0)
  if r.Source=="omni_subscription"{
   var windows []OmniAllowanceWindow
   if e:=tx.Clauses(clause.Locking{Strength:"UPDATE"}).Where("user_id = ? AND start = ? AND (kind = ? AND epoch = ? OR kind = ? AND epoch = ?)",r.UserID,r.SubscriptionStart,"week",r.WeekEpoch,"five",r.FiveEpoch).Find(&windows).Error;e!=nil{return e}
   expected:=1;if r.AllowanceFiveEnabled{expected=2};found:=0;covered=actual
   for _,w:=range windows{if w.Kind=="five"&&!r.AllowanceFiveEnabled{continue};found++;covered=min(covered,max(0,w.Limit-w.Used))}
   if found!=expected{return errors.New("missing pinned allowance window")}
   for i:=range windows{w:=&windows[i];if w.Kind=="five"&&!r.AllowanceFiveEnabled{continue};w.Used+=covered;if e:=tx.Model(&OmniAllowanceWindow{}).Where("user_id = ? AND start = ? AND kind = ? AND epoch = ?",w.UserID,w.Start,w.Kind,w.Epoch).UpdateColumn("used",w.Used).Error;e!=nil{return e}
    column,epoch:="week_used","week_epoch";if w.Kind=="five"{column,epoch="five_used","five_epoch"}
    if e:=tx.Model(&OmniSubscription{}).Where("user_id = ? AND start = ? AND "+epoch+" = ?",r.UserID,r.SubscriptionStart,w.Epoch).UpdateColumn(column,gorm.Expr(column+" + ?",covered)).Error;e!=nil{return e}
   }
  }
  r.AllowanceActual=covered
  if e:=omniCustomerWalletDebit(tx,&r,actual-covered);e!=nil{return e}
  r.Actual=actual;r.PlatformQuota=0;r.State="settled";if actual==0{r.State="refunded"}
  if e:=omniCustomerTokenSettle(tx,&r,actual);e!=nil{return e}
  if e:=tx.Save(&r).Error;e!=nil{return e};changed=true;return nil
 })
 if e==nil{omniCustomerInvalidateToken(owner.BillingTokenID)}
 return changed,e
}

// Binding and final token projection use the same durable record as money.
// No theoretical maximum is charged to the API Key before dispatch.
func OmniBindCustomerBilling(id string,tokenID int,priceVersion string,price []byte)error{
 return DB.Transaction(func(tx *gorm.DB)error{
  var r OmniReservation;if e:=tx.Clauses(clause.Locking{Strength:"UPDATE"}).First(&r,"request_id = ?",id).Error;e!=nil{return e}
  if r.BillingPolicy!=OmniCustomerBillingV1{return nil}
  if r.State!="pending"{return errors.New("request already finalized")}
  if r.BillingBound {
   if r.BillingTokenID!=tokenID||r.BillingPriceVersion!=priceVersion||r.BillingPriceSnapshot!=string(price){return errors.New("frozen billing quote conflict")}
   return nil
  }
  if r.BillingTokenID!=0&&r.BillingTokenID!=tokenID{return errors.New("request Key conflict")}
  if tokenID>0 {
   var token Token;if e:=tx.First(&token,tokenID).Error;e!=nil{return e}
   if token.UserId!=r.UserID||token.Status!=common.TokenStatusEnabled||!token.UnlimitedQuota&&token.RemainQuota<=0{return errors.New("API Key quota exhausted or disabled")}
  }
  r.BillingTokenID=tokenID;r.BillingPriceVersion=priceVersion;r.BillingPriceSnapshot=string(price);r.BillingBound=true
  return tx.Save(&r).Error
 })
}
func omniCustomerTokenSettle(tx *gorm.DB,r *OmniReservation,actual int64)error{
 if r.BillingTokenID<=0{return nil}
 result:=tx.Unscoped().Model(&Token{}).Where("id = ? AND user_id = ?",r.BillingTokenID,r.UserID).Updates(map[string]any{"remain_quota":gorm.Expr("remain_quota - ?",actual),"used_quota":gorm.Expr("used_quota + ?",actual),"accessed_time":time.Now().Unix()})
 if result.Error!=nil{return result.Error};if result.RowsAffected!=1{return fmt.Errorf("missing settlement Key")};return nil
}
func omniCustomerInvalidateToken(id int){
 if id<=0{return};var token Token;if DB.Unscoped().Select("key").First(&token,id).Error==nil{_ = invalidateTokenCacheForMutation(token.Key)}
}
