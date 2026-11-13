package model

import (
 "errors"
 "fmt"
 "sync"
 "testing"
 "time"
)

// Runs on SQLite locally and real PostgreSQL through OMNI_TEST_DSN. Account
// row locking must work across connections, as it does across gateway nodes.
func TestOmniAllowanceConcurrentAdmissionAndIdempotentSettlement(t *testing.T) {
 omniTestDB(t)
 now:=time.Now().Unix()
 s:=OmniSubscription{UserID:91,PlanKey:"standard",Start:now-60,End:now+86400,FiveLimit:307345,WeekLimit:1228638,WeekUsed:3708,QuotaVersion:2,BillingVersion:2}
 if err:=DB.Create(&s).Error;err!=nil{t.Fatal(err)}
 if _,err:=omniAccount(DB,91);err!=nil{t.Fatal(err)}
 DB.Model(&OmniAccount{}).Where("user_id = ?",91).Update("wallet_overflow",false)
 var wg sync.WaitGroup;var mu sync.Mutex;accepted:=[]string{};pending:=0;failures:=[]error{}
 for i:=0;i<20;i++ {wg.Add(1);go func(i int){defer wg.Done();id:=fmt.Sprintf("allowance-parallel-%d",i);r,err:=OmniReserveAllowance(91,id,"test-model",800000);mu.Lock();defer mu.Unlock();if err==nil {if r.Reserved!=307345{failures=append(failures,fmt.Errorf("wrong held amount %d",r.Reserved))};accepted=append(accepted,id)}else if errors.Is(err,ErrOmniAllowancePending){pending++}else{failures=append(failures,err)}}(i)}
 wg.Wait();if len(accepted)!=1 || pending!=19 || len(failures)>0{t.Fatalf("accepted=%d pending=%d failures=%v",len(accepted),pending,failures)}
 errs:=make(chan error,8)
 for i:=0;i<8;i++{wg.Add(1);go func(){defer wg.Done();errs<-OmniAdjust(accepted[0],800000,true)}()};wg.Wait();close(errs);for err:=range errs{if err!=nil{t.Fatal(err)}}
 DB.First(&s,"user_id = ?",91)
 var r OmniReservation;DB.First(&r,"request_id = ?",accepted[0])
 var u User;DB.First(&u,91)
 if s.FiveUsed!=800000 || s.WeekUsed!=803708 || !s.FivePaused || r.PlatformQuota!=492655 || r.Actual!=800000 || u.Quota!=10000000{t.Fatalf("concurrent finalization mischarged: %+v %+v wallet=%d",s,r,u.Quota)}
 if _,err:=OmniReserveAllowance(91,"overage-next","test-model",100);!errors.Is(err,ErrOmniInsufficient){t.Fatal("overage did not pause",err)}
 // Legacy strict holds cannot use the platform-funded settlement rule.
 DB.Model(&s).Updates(map[string]interface{}{"five_used":0,"week_used":0,"five_paused":false})
 if _,err:=OmniReserve(91,"strict-next","test-model",800000);!errors.Is(err,ErrOmniInsufficient){t.Fatal("strict request incorrectly granted grace",err)}
}
