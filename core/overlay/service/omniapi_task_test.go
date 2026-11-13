package service

import (
 "context"
 "testing"
 "github.com/QuantumNous/new-api/common"
 "github.com/QuantumNous/new-api/model"
)

func TestOmniAsyncReservationHeldUntilConfirmedFailure(t *testing.T) {
 truncate(t)
 common.OptionMapRWMutex.Lock();old:=common.OptionMap["OmniAPICommercial"];if common.OptionMap==nil{common.OptionMap=map[string]string{}};common.OptionMap["OmniAPICommercial"]=`{"billing_enabled":true}`;common.OptionMapRWMutex.Unlock()
 t.Cleanup(func(){common.OptionMapRWMutex.Lock();common.OptionMap["OmniAPICommercial"]=old;common.OptionMapRWMutex.Unlock();model.DB.Where("user_id = ?",88091).Delete(&model.OmniReservation{});model.DB.Where("user_id = ?",88091).Delete(&model.OmniAccount{})})
 if err:=model.OmniMigrate();err!=nil{t.Fatal(err)}
 if err:=model.DB.Create(&model.User{Id:88091,Username:"omni-async",Quota:1000,UsedQuota:280}).Error;err!=nil{t.Fatal(err)}
 funding:=&OmniFunding{uid:88091,requestID:"async-reservation-test",asyncTask:true}
 if err:=funding.PreConsume(300);err!=nil{t.Fatal(err)}
 if err:=funding.Settle(-20);err!=nil{t.Fatal(err)}
 record,err:=model.OmniRequestRecord(funding.requestID);if err!=nil || record.State!="pending" || record.Reserved!=280{t.Fatal(record,err)}
 task:=&model.Task{UserId:88091,TaskID:"async-reservation-task",Quota:280,Status:model.TaskStatusFailure}
 task.PrivateData.OmniRequestID=funding.requestID
 if err:=model.DB.Create(task).Error;err!=nil{t.Fatal(err)}
 if !OmniFinishTask(context.Background(),task,0,"confirmed failure") || !OmniFinishTask(context.Background(),task,0,"repeated failure"){t.Fatal("refund failed")}
 var user model.User;model.DB.First(&user,88091);if user.Quota!=1000{t.Fatal("refund duplicated",user.Quota)}
 record,err=model.OmniRequestRecord(funding.requestID);if err!=nil || record.State!="refunded"{t.Fatal(record,err)}
}
