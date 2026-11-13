package controller

import "github.com/QuantumNous/new-api/model"

func omniAppendWalletBalance(user *model.User, data map[string]any) {
 if model.DB==nil || user==nil {return}
 snapshot,err:=model.OmniWalletSnapshot(user.Id)
 if err!=nil{return}
 data["quota"]=int(snapshot.AvailableQuota)
 data["wallet_balance_quota"]=snapshot.BalanceQuota
 data["wallet_reserved_quota"]=snapshot.ReservedQuota
}
