package model
import("testing";"encoding/json";"sync";"github.com/QuantumNous/new-api/common")
func walletFixture(t *testing.T){
 omniTestDB(t)
 common.OptionMapRWMutex.Lock();old:=common.OptionMap["OmniAPIPricing"]
 p:=GetOmniPolicyUnsafeForWalletTest();raw,_:=json.Marshal(p);common.OptionMap["OmniAPICommercial"]=string(raw)
 common.OptionMap["OmniAPIPricing"]=`{"enabled":true,"cny_per_usd":"6.7351","models":{"test-model":{}}}`;common.OptionMapRWMutex.Unlock()
 t.Cleanup(func(){common.OptionMapRWMutex.Lock();common.OptionMap["OmniAPIPricing"]=old;common.OptionMapRWMutex.Unlock()})
 DB.Model(&User{}).Where("id = ?",91).Update("status",1)
}
func GetOmniPolicyUnsafeForWalletTest() OmniPolicy{return OmniPolicy{SalesEnabled:true,BillingEnabled:true,ModelScope:"all_available",Payment:OmniPayment{Enabled:true,Mode:"wallet_balance"}}}
func TestOmniWalletPurchaseAtomic(t *testing.T){
 walletFixture(t);var u User;DB.First(&u,91);before:=u.Quota
 if err:=OmniPurchaseWallet(91,"starter",1,"wallet-purchase-key-1");err!=nil{t.Fatal(err)}
 var s OmniSubscription;DB.First(&s,"user_id = ?",91);end:=s.End
 DB.First(&u,91);if u.Quota>=before{t.Fatal("wallet not debited")};after:=u.Quota
 if err:=OmniPurchaseWallet(91,"starter",1,"wallet-purchase-key-1");err!=nil{t.Fatal(err)}
 DB.First(&u,91);DB.First(&s,"user_id = ?",91);if u.Quota!=after || s.End!=end{t.Fatal("duplicate purchase")}
 if err:=OmniPurchaseWallet(91,"starter",12,"wallet-purchase-key-1");err==nil{t.Fatal("key conflict")}
 DB.Model(&User{}).Where("id = ?",91).Update("quota",0)
 if err:=OmniPurchaseWallet(91,"pro",1,"wallet-no-balance-key");err==nil{t.Fatal("insufficient accepted")}
 DB.First(&s,"user_id = ?",91);if s.End!=end || s.PendingPlan!=""{t.Fatal("failed purchase changed entitlement")}
 var n int64;DB.Model(&OmniLedger{}).Where("key LIKE ?","wallet-sub:%").Count(&n);if n!=1{t.Fatal("failed ledger persisted")}
 if _,err:=OmniCreateOrder(91,"starter",1);err==nil{t.Fatal("direct payment accepted")}
}
func TestOmniWalletPurchaseConcurrent(t *testing.T){
 walletFixture(t);DB.Model(&User{}).Where("id = ?",91).Update("quota",1500000)
 var wg sync.WaitGroup;errs:=make(chan error,8)
 for i:=0;i<8;i++{wg.Add(1);go func(){defer wg.Done();errs<-OmniPurchaseWallet(91,"starter",1,"same-concurrent-key")}()};wg.Wait();close(errs)
 for err:=range errs{if err!=nil{t.Fatal(err)}}
 var n int64;DB.Model(&OmniLedger{}).Where("key LIKE ?","wallet-sub:%").Count(&n);if n!=1{t.Fatal("duplicate charge")}
 var u User;DB.First(&u,91);if u.Quota<0{t.Fatal("overdraft")}
}
