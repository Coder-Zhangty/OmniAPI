package service

import (
 "context"
 "testing"
 "github.com/QuantumNous/new-api/common"
 "github.com/QuantumNous/new-api/model"
 "github.com/stretchr/testify/require"
)

func customerPolicy(t *testing.T){
 t.Helper();common.OptionMapRWMutex.Lock();old:=common.OptionMap["OmniAPICommercial"];common.OptionMap["OmniAPICommercial"]=`{"billing_enabled":true,"model_scope":"all_available","customer_overage_policy":"customer_actual_v1"}`;common.OptionMapRWMutex.Unlock()
 t.Cleanup(func(){common.OptionMapRWMutex.Lock();common.OptionMap["OmniAPICommercial"]=old;common.OptionMapRWMutex.Unlock()})
}

func TestOmniCustomerBillingSessionAtomicTokenNoDoubleCharge(t *testing.T){
 c,info:=actualAllowanceFixture(t);customerPolicy(t)
 require.NoError(t,model.DB.Where("user_id = ?",info.UserId).Delete(&model.OmniSubscription{}).Error)
 require.NoError(t,model.DB.Model(&model.User{}).Where("id = ?",info.UserId).Update("quota",100).Error)
 key:=model.Token{UserId:info.UserId,Key:"customer-service-test",RemainQuota:100,Status:1,ExpiredTime:-1};require.NoError(t,model.DB.Create(&key).Error)
 info.IsPlayground=false;info.TokenId=key.Id;info.TokenKey=key.Key
 s,e:=NewOmniBillingSession(c,info,10000000);require.Nil(t,e);require.Equal(t,0,s.GetPreConsumedQuota())
 require.NoError(t,s.Settle(150));require.NoError(t,s.Settle(150))
 var u model.User;require.NoError(t,model.DB.First(&u,info.UserId).Error);require.Equal(t,-50,u.Quota)
 require.NoError(t,model.DB.First(&key,key.Id).Error);require.Equal(t,-50,key.RemainQuota);require.Equal(t,150,key.UsedQuota)
 info.RequestId="customer-service-next";_,e=NewOmniBillingSession(c,info,1);require.NotNil(t,e);require.Equal(t,"account_in_debt",string(e.GetErrorCode()))
}

func TestOmniCustomerAsyncActualNotTruncated(t *testing.T){
 _,info:=actualAllowanceFixture(t);customerPolicy(t)
 require.NoError(t,model.DB.AutoMigrate(&model.Task{}))
 require.NoError(t,model.DB.Where("user_id = ?",info.UserId).Delete(&model.OmniSubscription{}).Error)
 require.NoError(t,model.DB.Model(&model.User{}).Where("id = ?",info.UserId).Update("quota",100).Error)
 f:=&OmniFunding{uid:info.UserId,requestID:"customer-service-async",asyncTask:true,flexibleAllowance:true}
 require.NoError(t,f.PreConsume(10));require.NoError(t,f.Settle(10))
 task:=&model.Task{UserId:info.UserId,TaskID:"customer-service-async-task",Quota:0,Status:model.TaskStatusSuccess};task.PrivateData.OmniRequestID=f.requestID
 require.NoError(t,model.DB.Create(task).Error)
 require.True(t,OmniFinishTask(context.Background(),task,150,"synthetic confirmed usage"));require.True(t,OmniFinishTask(context.Background(),task,150,"same terminal result"))
 r,e:=model.OmniRequestRecord(f.requestID);require.NoError(t,e);require.EqualValues(t,150,r.Actual);require.Zero(t,r.PlatformQuota)
 var u model.User;require.NoError(t,model.DB.First(&u,info.UserId).Error);require.Equal(t,-50,u.Quota)
}
