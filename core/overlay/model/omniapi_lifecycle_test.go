package model
import("testing";"time";"encoding/json";"strings";"gorm.io/gorm")
func legacyFixture(t *testing.T)UserSubscription{t.Helper();now:=time.Now().Unix();s:=UserSubscription{UserId:91,Status:"active",StartTime:now-1000,EndTime:now+7*86400,AmountTotal:100000,AmountUsed:23000,LastResetTime:now-1000,NextResetTime:now+86400};if e:=DB.Create(&s).Error;e!=nil{t.Fatal(e)};return s}
func TestOmniLegacyPauseRenewResume(t *testing.T){
 omniTestDB(t);old:=legacyFixture(t);now:=time.Now().Unix()
 if e:=OmniActivate("immediate",91,"starter",1,1900,6748900);e!=nil{t.Fatal(e)}
 var s OmniSubscription;DB.First(&s,"user_id = ?",91);if s.Start<now||s.Start>now+2||s.ActivationPending{t.Fatal(s)}
 var paused UserSubscription;DB.First(&paused,old.Id);if paused.Status!="paused"||paused.AmountUsed!=old.AmountUsed||paused.EndTime!=old.EndTime{t.Fatal(paused)}
 var p OmniLegacyPause;DB.First(&p,"subscription_id = ?",old.Id);if p.Remaining!=77000||p.RemainingSeconds!=old.EndTime-p.PausedAt{t.Fatal(p)}
 r,e:=OmniReserve(91,"new-call","test-model",1000);if e!=nil||r.Source!="omni_subscription"{t.Fatal(r,e)};if e=OmniAdjust("new-call",800,true);e!=nil{t.Fatal(e)}
 if e=OmniActivate("renew",91,"starter",1,1900,6748900);e!=nil{t.Fatal(e)};DB.First(&s,"user_id = ?",91);if s.FiveUsed!=800||s.WeekUsed!=800||s.Start!=p.PausedAt{t.Fatal(s)}
 if e=OmniActivate("next-tier",91,"standard",1,3900,6748900);e!=nil{t.Fatal(e)};DB.First(&s,"user_id = ?",91)
 // A queued different tier keeps the legacy plan paused through the next term.
 transition:=s.End+1;if e=DB.Transaction(func(tx *gorm.DB)error{return omniLifecycleTx(tx,91,transition)});e!=nil{t.Fatal(e)};DB.First(&paused,old.Id);if paused.Status!="paused"{t.Fatal(paused)}
 DB.First(&s,"user_id = ?",91);resume:=s.End+1;if e=DB.Transaction(func(tx *gorm.DB)error{return omniLifecycleTx(tx,91,resume)});e!=nil{t.Fatal(e)};DB.First(&paused,old.Id)
 delta:=resume-p.PausedAt;if paused.Status!="active"||paused.AmountUsed!=23000||paused.EndTime!=old.EndTime+delta||paused.NextResetTime!=old.NextResetTime+delta||paused.LastResetTime!=old.LastResetTime+delta{t.Fatal(paused)}
 if e=DB.Transaction(func(tx *gorm.DB)error{return omniLifecycleTx(tx,91,resume+60)});e!=nil{t.Fatal(e)};var again UserSubscription;DB.First(&again,old.Id);if again.EndTime!=paused.EndTime{t.Fatal("duplicate resume")}
}
func TestOmniScheduledMigrationOnceNoDebit(t *testing.T){
 omniTestDB(t);legacyFixture(t);OmniActivate("already-paid",91,"starter",1,1900,6748900)
 future:=time.Now().Unix()+7*86400;DB.Model(&OmniSubscription{}).Where("user_id = ?",91).Updates(map[string]any{"start":future,"end":OmniCalendarEnd(future,1)})
 // Emulate the previous version's delayed activation with the old package still active.
 DB.Model(&UserSubscription{}).Where("user_id = ?",91).Update("status","active");DB.Where("user_id = ?",91).Delete(&OmniLegacyPause{})
 var before User;DB.First(&before,91);if e:=OmniReconcile(91);e!=nil{t.Fatal(e)};var s OmniSubscription;DB.First(&s,"user_id = ?",91);if s.Start>=future||s.End!=OmniCalendarEnd(s.Start,1){t.Fatal(s)}
 if e:=OmniReconcile(91);e!=nil{t.Fatal(e)};var again OmniSubscription;DB.First(&again,"user_id = ?",91);var u User;DB.First(&u,91);if u.Quota!=before.Quota||again.Start!=s.Start||again.End!=s.End{t.Fatal("migration changed money or expiry")}
 var n int64;DB.Model(&OmniLedger{}).Where("kind = ?","subscription_activation").Count(&n);if n!=1{t.Fatal(n)}
}
func TestOmniLegacyFlightDelaysActivationUntilSettlement(t *testing.T){
 omniTestDB(t);old:=legacyFixture(t);if e:=OmniLegacyBegin(91,"old-inflight");e!=nil{t.Fatal(e)}
 if e:=OmniActivate("paid-busy",91,"starter",1,1900,6748900);e!=nil{t.Fatal(e)};var s OmniSubscription;DB.First(&s,"user_id = ?",91);if !s.ActivationPending{t.Fatal("activated during request")}
 if _,e:=OmniActive(91);e==nil{t.Fatal("new request admitted during transition")};var row UserSubscription;DB.First(&row,old.Id);if row.Status!="active"{t.Fatal("old request funding changed")}
 DB.Model(&row).Update("amount_used",24000);OmniLegacyFinish(91,"old-inflight");DB.First(&s,"user_id = ?",91);DB.First(&row,old.Id);if s.ActivationPending||row.Status!="paused"||row.AmountUsed!=24000{t.Fatal(s,row)}
 var p OmniLegacyPause;DB.First(&p,"subscription_id = ?",old.Id);if p.Remaining!=76000{t.Fatal(p)}
}
func TestOmniCancelledLegacyNeverResumes(t *testing.T){omniTestDB(t);old:=legacyFixture(t);OmniActivate("new",91,"starter",1,1900,6748900);DB.Model(&UserSubscription{}).Where("id = ?",old.Id).Update("status","cancelled");var s OmniSubscription;DB.First(&s,"user_id = ?",91);if e:=DB.Transaction(func(tx *gorm.DB)error{return omniLifecycleTx(tx,91,s.End+1)});e!=nil{t.Fatal(e)};var row UserSubscription;DB.First(&row,old.Id);if row.Status!="cancelled"{t.Fatal(row)}}
func TestOmniPublicInformationIsolation(t *testing.T){
 for _,raw:=range []string{"没有 0-0 主线路","H API busy","https://api.0-0.pro bad"}{if strings.Contains(OmniUserMessage(raw),raw){t.Fatal(raw)}}
 if OmniUserMessage("2026-10-02")!="2026-10-02"{t.Fatal("date over-redacted")}
 raw:=`{"channel_attempts":[1,6],"upstream_request_id":"secret","costs":{"x":1},"amount":1234567890123456,"billing_source":"omni_subscription","timing":1}`
 clean:=OmniPublicLogOther(raw);var m map[string]json.RawMessage;if e:=json.Unmarshal([]byte(clean),&m);e!=nil{t.Fatal(e)};if string(m["amount"])!="1234567890123456"||m["channel_attempts"]!=nil||m["costs"]!=nil||m["upstream_request_id"]!=nil{t.Fatal(clean)}
 s:=OmniSubscription{IncludedModels:"private-model-list",PendingIncludedModels:"another-private-list"};data,_:=json.Marshal(s);if strings.Contains(string(data),"private-"){t.Fatal("subscription list leaked")}
}

func TestOmniQueuedPurchaseKeepsCurrentBillingCycle(t *testing.T){omniTestDB(t);if e:=OmniActivate("annual",91,"starter",12,18240,6748900);e!=nil{t.Fatal(e)};if e:=OmniActivate("queued-month",91,"standard",1,3900,6748900);e!=nil{t.Fatal(e)};var s OmniSubscription;DB.First(&s,"user_id = ?",91);if s.LastPurchaseMonths!=12||s.PlanKey!="starter"||s.PendingPlan!="standard"{t.Fatal(s)}}
