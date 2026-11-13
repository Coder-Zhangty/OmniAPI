package model

import (
 "encoding/json"
 "testing"
 "github.com/QuantumNous/new-api/common"
)

func TestOmniAllAvailableIncludesFutureModels(t *testing.T) {
 omniTestDB(t)
 common.OptionMapRWMutex.Lock()
 old:=common.OptionMap["OmniAPIPricing"]
 policy:=OmniPolicy{SalesEnabled:true,BillingEnabled:true,ModelScope:"all_available",Payment:OmniPayment{Enabled:true,Contact:"test",QRPath:"/brand/payment-qr.png"}}
 raw,_:=json.Marshal(policy);common.OptionMap["OmniAPICommercial"]=string(raw)
 common.OptionMap["OmniAPIPricing"]=`{"enabled":true,"models":{"expensive":{}},"waiting":{"paused":"unverified"}}`
 common.OptionMapRWMutex.Unlock()
 t.Cleanup(func(){common.OptionMapRWMutex.Lock();common.OptionMap["OmniAPIPricing"]=old;common.OptionMapRWMutex.Unlock()})
 if !OmniSalesReady() || !OmniEligible("expensive") || OmniEligible("paused") {t.Fatal("all model availability")}
 if err:=OmniActivate("all-model-test",91,"starter",1,1900,6735100);err!=nil{t.Fatal(err)}
 var s OmniSubscription;DB.First(&s,"user_id = ?",91)
 originalStart,originalEnd:=s.Start,s.End
 common.OptionMapRWMutex.Lock();common.OptionMap["OmniAPIPricing"]=`{"enabled":true,"models":{"expensive":{},"future-image":{}},"waiting":{"paused":"unverified"}}`;common.OptionMapRWMutex.Unlock()
 if !s.Includes("future-image") || s.Includes("paused") || len(s.ModelNames())!=2 {t.Fatal("dynamic model inclusion")}
 r,err:=OmniReserve(91,"future-image-request","future-image",1200)
 if err!=nil || r.Source!="omni_subscription" {t.Fatal(r,err)}
 changed,err:=OmniAdjustOnce(r.RequestID,900,true);if err!=nil || !changed {t.Fatal(changed,err)}
 changed,err=OmniAdjustOnce(r.RequestID,0,true);if err!=nil || changed {t.Fatal("replayed settlement")}
 DB.First(&s,"user_id = ?",91)
 if s.Start!=originalStart || s.End!=originalEnd || s.FiveUsed!=900 || s.WeekUsed!=900 {t.Fatal("window or validity changed")}
}

func TestOmniReceiptCannotFulfillAnotherOrder(t *testing.T) {
 omniTestDB(t)
 first,err:=OmniCreateOrder(91,"starter",1);if err!=nil{t.Fatal(err)}
 DB.Model(&first).Update("state","pending")
 if err=OmniActivate("bound-receipt",91,"starter",1,1900,6735100,first.ID);err!=nil{t.Fatal(err)}
 next,err:=OmniCreateOrder(91,"starter",1);if err!=nil{t.Fatal(err)}
 DB.Model(&next).Update("state","pending")
 if err=OmniActivate("bound-receipt",91,"starter",1,1900,6735100,next.ID);err==nil{t.Fatal("one receipt accepted for two orders")}
 if err=OmniActivate("bound-receipt",91,"starter",1,1900,6735100,first.ID);err!=nil{t.Fatal("same order replay rejected",err)}
}
