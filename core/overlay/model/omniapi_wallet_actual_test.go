package model

import (
 "fmt"
 "sync"
 "testing"
)

func TestOmniWalletActualNoHoldAndConcurrentSettlement(t *testing.T) {
 omniTestDB(t)
 DB.Model(&User{}).Where("id = ?",91).Update("quota",100)
 for i:=0;i<20;i++ {
  r,err:=OmniReserveAllowance(91,fmt.Sprintf("wallet-actual-%d",i),"",1000000)
  if err!=nil {t.Fatal(err)}
  if r.Reserved!=0 || !r.FlexibleAllowance || r.Source!="wallet" {t.Fatal("wallet precharged",r)}
 }
 var u User; DB.First(&u,91);if u.Quota!=100{t.Fatal("pending requests changed balance",u.Quota)}
 if err:=OmniAdjust("wallet-actual-19",0,true);err!=nil{t.Fatal(err)}
 DB.First(&u,91);if u.Quota!=100{t.Fatal("failure deducted wallet")}
 var wg sync.WaitGroup;errs:=make(chan error,38)
 for i:=0;i<19;i++ {for j:=0;j<2;j++ {wg.Add(1);go func(i int){defer wg.Done();errs<-OmniAdjust(fmt.Sprintf("wallet-actual-%d",i),30,true)}(i)}}
 wg.Wait();close(errs);for err:=range errs{if err!=nil{t.Fatal(err)}}
 DB.First(&u,91);if u.Quota!=0{t.Fatal("settlement went negative or repeated",u.Quota)}
 var rows []OmniReservation;DB.Where("request_id LIKE ?","wallet-actual-%").Find(&rows)
 var actual,platform int64;for _,r:=range rows{actual+=r.Actual;platform+=r.PlatformQuota}
 if actual!=570 || platform!=470{t.Fatal("actual and platform accounting mismatch",actual,platform)}
 if _,err:=OmniReserveAllowance(91,"wallet-empty","",1);err==nil{t.Fatal("empty wallet admitted request")}
}
