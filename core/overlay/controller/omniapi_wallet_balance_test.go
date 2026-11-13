package controller

import (
 "testing"
 "github.com/QuantumNous/new-api/model"
 "github.com/QuantumNous/new-api/common"
 "github.com/glebarez/sqlite"
 "gorm.io/gorm"
)

func TestOmniWalletSelfSeparatesPendingHoldFromBalance(t *testing.T) {
 old,cache:=model.DB,common.MemoryCacheEnabled
 db,err:=gorm.Open(sqlite.Open(":memory:"),&gorm.Config{});if err!=nil{t.Fatal(err)}
 model.DB=db;common.MemoryCacheEnabled=false;sqlDB,_:=db.DB();sqlDB.SetMaxOpenConns(1)
 t.Cleanup(func(){model.DB=old;common.MemoryCacheEnabled=cache;sqlDB.Close()})
 if err=db.AutoMigrate(&model.User{});err!=nil{t.Fatal(err)}
 if err=model.OmniMigrate();err!=nil{t.Fatal(err)}
 u:=model.User{Id:6262,Username:"wallet-balance-fixture",Quota:855838}
 if err=db.Create(&u).Error;err!=nil{t.Fatal(err)}
 // Represent the same completed balance before, during and after a failed call.
 cases:=[]struct{available,held,balance int64}{{855838,0,855838},{478734,377104,855838},{855838,0,855838},{854849,0,854849}}
 for _,tc:=range cases{
  db.Model(&u).Update("quota",tc.available);u.Quota=int(tc.available)
  db.Where("user_id = ?",u.Id).Delete(&model.OmniReservation{})
  if tc.held>0{db.Create(&model.OmniReservation{RequestID:"temporary-hold",UserID:u.Id,Source:"wallet",State:"pending",Reserved:tc.held})}
  dto:=buildSelfUserData(&u)
  if dto["wallet_balance_quota"]!=tc.balance || dto["wallet_reserved_quota"]!=tc.held {t.Fatalf("pending reservation presented as spending: balance=%v held=%v expected=%d/%d",dto["wallet_balance_quota"],dto["wallet_reserved_quota"],tc.balance,tc.held)}
 }
}
