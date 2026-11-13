package model

import (
 "encoding/json"
 "errors"
 "fmt"
 "sync"
 "testing"
 "github.com/stretchr/testify/require"
)

func customerBillingFixture(t *testing.T) {
 t.Helper()
 raw,_:=json.Marshal(GetOmniPolicy());var p map[string]any;require.NoError(t,json.Unmarshal(raw,&p))
 p["customer_overage_policy"]="customer_actual_v1"
 commerceOption(t,"OmniAPICommercial",p)
}

func TestOmniCustomerWalletConcurrentActualDebt(t *testing.T) {
 omniTestDB(t);customerBillingFixture(t)
 require.NoError(t,DB.Model(&User{}).Where("id = ?",91).Update("quota",100).Error)
 for i:=0;i<20;i++ {r,e:=OmniReserveAllowance(91,fmt.Sprintf("customer-wallet-%d",i),"",1000000);require.NoError(t,e);require.Zero(t,r.Reserved)}
 require.NoError(t,OmniAdjust("customer-wallet-19",0,true))
 var wg sync.WaitGroup;errs:=make(chan error,38)
 for i:=0;i<19;i++ {for j:=0;j<2;j++ {wg.Add(1);go func(i int){defer wg.Done();errs<-OmniAdjust(fmt.Sprintf("customer-wallet-%d",i),30,true)}(i)}}
 wg.Wait();close(errs);for e:=range errs{require.NoError(t,e)}
 var u User;require.NoError(t,DB.First(&u,91).Error);require.Equal(t,-470,u.Quota)
 var rows []OmniReservation;require.NoError(t,DB.Where("request_id LIKE ?","customer-wallet-%").Find(&rows).Error)
 var actual,platform int64;for _,r:=range rows{actual+=r.Actual;platform+=r.PlatformQuota};require.EqualValues(t,570,actual);require.Zero(t,platform)
 _,e:=OmniReserveAllowance(91,"customer-debtor-new","",1);require.Error(t,e)
 require.NoError(t,DB.Model(&User{}).Where("id = ?",91).UpdateColumn("quota",100).Error)
 _,e=OmniReserveAllowance(91,"customer-repaid-new","",1000000);require.NoError(t,e)
}

func TestOmniCustomerSubscriptionTailIgnoresTheoreticalReserve(t *testing.T) {
 omniTestDB(t);customerBillingFixture(t)
 require.NoError(t,OmniActivate("customer-sub",91,"starter",1,1900,7000000))
 require.NoError(t,DB.Model(&OmniSubscription{}).Where("user_id = ?",91).Updates(map[string]any{"five_limit":400,"week_limit":1000}).Error)
 _,e:=OmniSetWalletOverflow(91,false);require.NoError(t,e)
 require.NoError(t,DB.Model(&User{}).Where("id = ?",91).Update("quota",0).Error)
 r,e:=OmniReserveAllowance(91,"customer-sub-tail","test-model",1000000);require.NoError(t,e);require.Equal(t,"omni_subscription",r.Source);require.Zero(t,r.Reserved)
 require.NoError(t,OmniAdjust(r.RequestID,500,true))
 var u User;require.NoError(t,DB.First(&u,91).Error);require.Equal(t,-100,u.Quota)
 var s OmniSubscription;require.NoError(t,DB.First(&s,"user_id = ?",91).Error);require.EqualValues(t,400,s.FiveUsed);require.EqualValues(t,400,s.WeekUsed)
 _,e=OmniReserveAllowance(91,"customer-sub-debt-next","test-model",1);require.Error(t,e)
 require.NoError(t,DB.Model(&User{}).Where("id = ?",91).Update("quota",1000).Error)
 _,e=OmniReserveAllowance(91,"customer-sub-disabled-overflow","test-model",1);require.Error(t,e)
}

func TestOmniCustomerPolicyPinnedForOldAndNewRequests(t *testing.T) {
 omniTestDB(t);require.NoError(t,DB.Model(&User{}).Where("id = ?",91).Update("quota",100).Error)
 old,e:=OmniReserveAllowance(91,"old-policy-pending","",1000);require.NoError(t,e)
 customerBillingFixture(t)
 require.NoError(t,OmniAdjust(old.RequestID,150,true))
 var u User;require.NoError(t,DB.First(&u,91).Error);require.Zero(t,u.Quota)
 require.NoError(t,DB.Model(&User{}).Where("id = ?",91).Update("quota",100).Error)
 r,e:=OmniReserveAllowance(91,"new-policy-pending","",1000);require.NoError(t,e)
 // Switching the global option back must not change a request's settlement.
 commerceOption(t,"OmniAPICommercial",OmniPolicy{})
 require.NoError(t,OmniAdjust(r.RequestID,150,true));require.NoError(t,DB.First(&u,91).Error);require.Equal(t,-50,u.Quota)
}

func TestOmniCustomerSharedDebtLocksOriginalPoolOwner(t *testing.T) {
 _,poolID,key:=sharedFixture(t);customerBillingFixture(t)
 require.NoError(t,DB.Model(&OmniSharedBatch{}).Where("pool_id = ?",poolID).Updates(map[string]any{"granted":100,"used":0,"reserved":0,"price_cents":100}).Error)
 require.NoError(t,DB.Model(&User{}).Where("id = ?",91).Update("quota",0).Error)
 h,e:=OmniSharedReserve(91,key.Id,poolID,"customer-shared-tail","gpt-6.1-sol",1000000);require.NoError(t,e)
 require.NoError(t,DB.Create(&User{Id:92,Username:"new-customer-owner",AffCode:"cnewowner",Quota:1000,Status:1}).Error)
 require.NoError(t,DB.Model(&OmniSharedPool{}).Where("id = ?",poolID).Update("owner_id",92).Error)
 require.NoError(t,OmniSharedSettle(h.RequestID,250));require.NoError(t,OmniSharedSettle(h.RequestID,250))
 var oldOwner,newOwner User;require.NoError(t,DB.First(&oldOwner,91).Error);require.NoError(t,DB.First(&newOwner,92).Error)
 require.Equal(t,-150,oldOwner.Quota);require.Equal(t,1000,newOwner.Quota)
 var b OmniSharedBatch;require.NoError(t,DB.First(&b,"pool_id = ?",poolID).Error);require.EqualValues(t,100,b.Used);require.Zero(t,b.Reserved)
 require.NoError(t,DB.First(h,"request_id = ?",h.RequestID).Error);require.Zero(t,h.PlatformQuota)
 if e:=OmniSharedSettle(h.RequestID,251);e==nil{t.Fatal("conflicting settlement was accepted")}
}

func TestOmniCustomerSharedMemberDebtDoesNotChargeMember(t *testing.T) {
 _,poolID,_:=sharedFixture(t);customerBillingFixture(t)
 require.NoError(t,DB.Create(&User{Id:92,Username:"customer-debtor-member",AffCode:"cmember",Quota:-500,Status:1}).Error)
 require.NoError(t,DB.Create(&OmniSharedMember{PoolID:poolID,UserID:92,State:"active",BudgetMicros:-1}).Error)
 key:=Token{UserId:92,Key:"customer-shared-member-key",Status:1,OmniPoolID:poolID,OmniFallback:"off",ExpiredTime:-1,UnlimitedQuota:true};require.NoError(t,key.Insert())
 h,e:=OmniSharedReserve(92,key.Id,poolID,"customer-member-debt-shared","gpt-6.1-sol",100);require.NoError(t,e)
 require.NoError(t,OmniSharedSettle(h.RequestID,30))
 var u User;require.NoError(t,DB.First(&u,92).Error);require.Equal(t,-500,u.Quota)
 require.NoError(t,DB.Model(&User{}).Where("id = ?",91).Update("quota",-1).Error)
 _,e=OmniSharedReserve(92,key.Id,poolID,"customer-owner-in-debt","gpt-6.1-sol",1)
 require.Error(t,e);require.False(t,errors.Is(e,ErrOmniSharedUnavailable))
}

func TestOmniCustomerAtomicTokenAndMoneyReplay(t *testing.T){
 omniTestDB(t);customerBillingFixture(t);require.NoError(t,DB.AutoMigrate(&Token{}))
 key:=Token{UserId:91,Key:"customer-atomic-key",Status:1,RemainQuota:1,ExpiredTime:-1};require.NoError(t,DB.Create(&key).Error)
 r,e:=OmniReserveAllowance(91,"customer-token-event","",99999);require.NoError(t,e)
 require.NoError(t,OmniBindCustomerBilling(r.RequestID,key.Id,"test-price-v1",[]byte(`{"retail":"synthetic"}`)))
 require.Error(t,OmniBindCustomerBilling(r.RequestID,key.Id,"changed-price",[]byte(`{"retail":"different"}`)))
 for i:=0;i<3;i++{require.NoError(t,OmniAdjust(r.RequestID,30,true))}
 require.NoError(t,DB.First(&key,key.Id).Error);require.Equal(t,-29,key.RemainQuota);require.Equal(t,30,key.UsedQuota)
 require.NoError(t,DB.First(r,"request_id = ?",r.RequestID).Error);require.EqualValues(t,30,r.WalletActual);require.Equal(t,"test-price-v1",r.BillingPriceVersion)
}

func TestOmniCustomerOwnedCanaryDoesNotChangeOtherAccounts(t *testing.T){
 omniTestDB(t);customerBillingFixture(t)
 p:=GetOmniPolicy();p.CustomerOverageUserIDs=[]int{91};commerceOption(t,"OmniAPICommercial",p)
 require.True(t,OmniCustomerBillingFor(91));require.False(t,OmniCustomerBillingFor(92))
 require.NoError(t,DB.Create(&User{Id:92,Username:"legacy-during-canary",AffCode:"legacycan",Quota:100,Status:1}).Error)
 r,e:=OmniReserveAllowance(92,"outside-customer-canary","",10000);require.NoError(t,e);require.Empty(t,r.BillingPolicy)
 require.NoError(t,OmniAdjust(r.RequestID,150,true))
 var u User;require.NoError(t,DB.First(&u,92).Error);require.Zero(t,u.Quota)
 require.NoError(t,DB.First(r,"request_id = ?",r.RequestID).Error);require.EqualValues(t,50,r.PlatformQuota)
}

func TestOmniCustomerPinnedOldWindowSurvivesResetBoundary(t *testing.T){
 omniTestDB(t);customerBillingFixture(t)
 require.NoError(t,OmniActivate("customer-period-sub",91,"starter",1,1900,7000000))
 require.NoError(t,DB.Model(&OmniSubscription{}).Where("user_id = ?",91).Updates(map[string]any{"five_limit":100,"week_limit":200}).Error)
 r,e:=OmniReserveAllowance(91,"customer-period-tail","test-model",99999);require.NoError(t,e)
 // Make the next request advance the same subscription into new windows.
 require.NoError(t,DB.Model(&OmniSubscription{}).Where("user_id = ?",91).Updates(map[string]any{"five_epoch":r.FiveEpoch+1,"week_epoch":r.WeekEpoch+1,"five_used":0,"week_used":0}).Error)
 require.NoError(t,OmniAdjust(r.RequestID,150,true))
 var s OmniSubscription;require.NoError(t,DB.First(&s,"user_id = ?",91).Error);require.Zero(t,s.FiveUsed);require.Zero(t,s.WeekUsed)
 require.NoError(t,DB.First(r,"request_id = ?",r.RequestID).Error);require.EqualValues(t,100,r.AllowanceActual);require.EqualValues(t,50,r.WalletActual)
}

func TestOmniCustomerResetRestoresDurableWindow(t *testing.T){
 omniTestDB(t);customerBillingFixture(t)
 require.NoError(t,OmniActivate("customer-reset-sub",91,"starter",1,1900,7000000))
 require.NoError(t,DB.Model(&OmniSubscription{}).Where("user_id = ?",91).Updates(map[string]any{"five_limit":100,"week_limit":200}).Error)
 r,e:=OmniReserveAllowance(91,"customer-before-reset","test-model",99999);require.NoError(t,e);require.NoError(t,OmniAdjust(r.RequestID,100,true))
 codes,e:=OmniGrantCards(91,"five_hour",1);require.NoError(t,e);require.NoError(t,OmniRedeem(91,codes[0]))
 r,e=OmniReserveAllowance(91,"customer-after-reset","test-model",99999);require.NoError(t,e);require.Equal(t,"omni_subscription",r.Source)
 require.NoError(t,OmniAdjust(r.RequestID,80,true));require.NoError(t,DB.First(r,"request_id = ?",r.RequestID).Error);require.EqualValues(t,80,r.AllowanceActual);require.Zero(t,r.WalletActual)
}

func TestOmniCustomerSharedConcurrentTailMemberBudgetAndOwnerDebt(t *testing.T){
 _,poolID,key:=sharedFixture(t);customerBillingFixture(t)
 require.NoError(t,DB.Model(&OmniSharedBatch{}).Where("pool_id = ?",poolID).Updates(map[string]any{"granted":100,"used":0,"reserved":0,"price_cents":100}).Error)
 require.NoError(t,DB.Model(&OmniSharedMember{}).Where("pool_id = ?",poolID).Update("budget_micros",10000).Error)
 require.NoError(t,DB.Model(&User{}).Where("id = ?",91).Update("quota",0).Error)
 for i:=0;i<16;i++{_,e:=OmniSharedReserve(91,key.Id,poolID,fmt.Sprintf("customer-pool-concurrent-%d",i),"gpt-6.1-sol",999999);require.NoError(t,e)}
 _,e:=OmniSharedReserve(91,key.Id,poolID,"customer-pool-seventeenth","gpt-6.1-sol",1);require.ErrorIs(t,e,ErrOmniSharedConcurrent)
 for i:=0;i<16;i++{require.NoError(t,OmniSharedSettle(fmt.Sprintf("customer-pool-concurrent-%d",i),30))}
 var u User;require.NoError(t,DB.First(&u,91).Error);require.Equal(t,-380,u.Quota)
 view,e:=OmniSharedView(91,poolID,"");require.NoError(t,e);require.EqualValues(t,1000000,view["consumed_micros"])
 var pool OmniSharedPool;require.NoError(t,DB.First(&pool,"id = ?",poolID).Error);require.False(t,pool.Frozen)
}
