package service

import (
 "net/http/httptest"
 "testing"
 "time"
 "github.com/QuantumNous/new-api/model"
 "github.com/QuantumNous/new-api/relaykit/dto"
)

func TestOmniImageActualWalletNoHoldAndPlatformOverage(t *testing.T) {
 c,info:=actualAllowanceFixture(t)
 c.Request=httptest.NewRequest("POST","/v1/images/generations",nil)
 model.DB.Model(&model.OmniSubscription{}).Where("user_id = ?",9916).Update("end",time.Now().Unix()-1)
 model.DB.Model(&model.User{}).Where("id = ?",9916).Update("quota",100)
 info.Request=&dto.ImageRequest{};info.RequestId="image-positive-wallet"
 session,apiErr:=NewOmniBillingSession(c,info,1000)
 if apiErr!=nil{t.Fatal("positive wallet image rejected by estimate",apiErr)}
 var user model.User;model.DB.First(&user,9916)
 if user.Quota!=100||session.GetPreConsumedQuota()!=0{t.Fatal("image request held wallet funds")}
 if err:=session.Settle(500);err!=nil{t.Fatal(err)}
 if err:=session.Settle(500);err!=nil{t.Fatal("idempotence",err)}
 model.DB.First(&user,9916)
 var record model.OmniReservation;model.DB.First(&record,"request_id = ?",info.RequestId)
 if user.Quota!=0||record.Actual!=500||record.PlatformQuota!=400||record.State!="settled"{t.Fatalf("unexpected image settlement: wallet=%d record=%+v",user.Quota,record)}
 info.RequestId="image-empty-wallet"
 if _,err:=NewOmniBillingSession(c,info,1000);err==nil{t.Fatal("empty wallet image admitted")}
}

func TestOmniImageActualFailureDoesNotCharge(t *testing.T) {
 c,info:=actualAllowanceFixture(t)
 model.DB.Model(&model.OmniSubscription{}).Where("user_id = ?",9916).Update("end",time.Now().Unix()-1)
 model.DB.Model(&model.User{}).Where("id = ?",9916).Update("quota",100)
 info.Request=&dto.ImageRequest{};info.RequestId="image-upstream-failed"
 session,apiErr:=NewOmniBillingSession(c,info,1000);if apiErr!=nil{t.Fatal(apiErr)}
 if err:=session.funding.Refund();err!=nil{t.Fatal(err)}
 var user model.User;model.DB.First(&user,9916)
 var record model.OmniReservation;model.DB.First(&record,"request_id = ?",info.RequestId)
 if user.Quota!=100||record.Actual!=0||record.Reserved!=0{t.Fatal("failed image request charged")}
}
