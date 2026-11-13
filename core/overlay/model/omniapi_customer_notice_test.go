package model

import "testing"

func TestOmniCustomerPopupIsUserScopedAndAcknowledged(t *testing.T) {
 omniTestDB(t)
 key:="announcement:customer:resolved:91"
 if err:=omniNotice(DB,key,91,"问题已解决","赠送20元余额以表歉意");err!=nil{t.Fatal(err)}
 rows,err:=OmniAnnouncements(91);if err!=nil{t.Fatal(err)}
 found:=false;for _,r:=range rows{if r.Key==key{found=true}}
 if !found{t.Fatal("private popup absent")}
 rows,err=OmniAnnouncements(92);if err!=nil{t.Fatal(err)}
 for _,r:=range rows{if r.Key==key{t.Fatal("private popup leaked")}}
 if err:=DB.Model(&OmniNotice{}).Where("key = ? AND user_id = ?",key,91).Update("read",true).Error;err!=nil{t.Fatal(err)}
 rows,err=OmniAnnouncements(91);if err!=nil{t.Fatal(err)}
 for _,r:=range rows{if r.Key==key{t.Fatal("acknowledged popup repeated")}}
}
