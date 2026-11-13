package service

import (
 "encoding/json"
 "testing"
 "github.com/QuantumNous/new-api/common"
 "github.com/QuantumNous/new-api/model"
 "github.com/QuantumNous/new-api/pkg/billingexpr"
 relaycommon "github.com/QuantumNous/new-api/relay/common"
 "github.com/stretchr/testify/require"
)
func privateTestOptions(t *testing.T) {
 common.OptionMapRWMutex.Lock();if common.OptionMap==nil{common.OptionMap=map[string]string{}};old:=common.OptionMap["OmniAPIPrivateBilling"];routing:=common.OptionMap["OmniAPIRouting"]
 common.OptionMap["OmniAPIPrivateBilling"]=`{"1009":{"enabled":true,"multiplier":1.1}}`
 common.OptionMap["OmniAPIRouting"]=`{"enabled":true,"models":{"test-private":[1,6]}}`;common.OptionMapRWMutex.Unlock()
 t.Cleanup(func(){common.OptionMapRWMutex.Lock();common.OptionMap["OmniAPIPrivateBilling"]=old;common.OptionMap["OmniAPIRouting"]=routing;common.OptionMapRWMutex.Unlock()})
}
func privateInfo() *relaycommon.RelayInfo {
 return &relaycommon.RelayInfo{UserId:1009,OriginModelName:"test-private",ChannelMeta:&relaycommon.ChannelMeta{ChannelId:1},OmniPriceQuote:[]byte(`{"costs":{"1":{"input_usd_per_million":2,"output_usd_per_million":10,"cache_read_usd_per_million":0.2,"cache_write_usd_per_million":2.5,"cache_write_1h_usd_per_million":4},"6":{"input_usd_per_million":4,"output_usd_per_million":20,"cache_read_usd_per_million":0.4,"cache_write_usd_per_million":5,"cache_write_1h_usd_per_million":8}}}`)}
}
func TestOmniPrivateActualRouteAndAllCacheDimensions(t *testing.T) {
 privateTestOptions(t);info:=privateInfo();require.NoError(t,OmniPreparePrivateBilling(info))
 expression:=info.OmniPriceExpression
 info.TieredBillingSnapshot=&billingexpr.BillingSnapshot{BillingMode:"tiered_expr",ExprString:expression,ExprHash:billingexpr.ExprHashString(expression),ExprVersion:1,QuotaPerUnit:common.QuotaPerUnit,GroupRatio:1}
 params:=billingexpr.TokenParams{P:100000,C:10000,CR:100000,CC:100000,CC1h:100000,Len:400000}
 ok,quota,_:=TryTieredSettle(info,params);require.True(t,ok)
 require.Equal(t,common.QuotaFromFloat((0.2+0.1+0.02+0.25+0.4)*1.1*common.QuotaPerUnit),quota)
 info.ChannelId=6;_,fallbackQuota,_:=TryTieredSettle(info,params);require.Equal(t,quota*2,fallbackQuota)
 require.Equal(t,expression,info.TieredBillingSnapshot.ExprString,"settlement must not mutate frozen reservation")
 common.OptionMapRWMutex.Lock();common.OptionMap["OmniAPIPrivateBilling"]=`{}`;common.OptionMapRWMutex.Unlock()
 _,frozenQuota,_:=TryTieredSettle(info,params);require.Equal(t,fallbackQuota,frozenQuota,"in-flight multiplier frozen")
}
func TestOmniPrivateUnrelatedAccountUnchangedAndUnknownCostsRejected(t *testing.T) {
 privateTestOptions(t);info:=privateInfo();info.UserId=1008;info.OmniPriceExpression="original"
 require.NoError(t,OmniPreparePrivateBilling(info));require.Equal(t,"original",info.OmniPriceExpression);require.Zero(t,info.OmniPrivateCostMultiplier)
 info=privateInfo();info.OmniPriceQuote=[]byte(`{"costs":{}}`);require.Error(t,OmniPreparePrivateBilling(info));require.Zero(t,info.OmniPrivateCostMultiplier)
}
func TestOmniPrivateNilChannelMetaDoesNotPanic(t *testing.T) {
 privateTestOptions(t)
 info:=privateInfo();info.ChannelMeta=nil
 require.NoError(t,OmniPreparePrivateBilling(info))
 _,err:=omniPrivateSelectedExpression(info);require.Error(t,err)
}
func TestOmniPrivateImageAndContextTierCosts(t *testing.T) {
 expression,err:=omniPrivateCostExpression(json.RawMessage(`{"per_image_usd":"0.05"}`),1.1);require.NoError(t,err)
 count:=2
 snap:=&billingexpr.BillingSnapshot{BillingMode:"tiered_expr",ExprString:omniPrivateWrap(expression),ExprVersion:1,QuotaPerUnit:common.QuotaPerUnit,GroupRatio:1}
 snap.ExprHash=billingexpr.ExprHashString(snap.ExprString)
 result,err:=billingexpr.ComputeTieredQuotaWithRequest(snap,billingexpr.TokenParams{},billingexpr.RequestInput{ImageCount:&count});require.NoError(t,err);require.Equal(t,common.QuotaFromFloat(.11*common.QuotaPerUnit),result.ActualQuotaAfterGroup)
 expression,err=omniPrivateCostExpression(json.RawMessage(`{"input_usd_per_million":1,"output_usd_per_million":2,"tiers":[{"min_tokens":200001,"max_tokens":null,"prices":{"input_usd_per_million":3,"output_usd_per_million":4}}]}`),1.1);require.NoError(t,err)
 snap.ExprString=omniPrivateWrap(expression);snap.ExprHash=billingexpr.ExprHashString(snap.ExprString)
 result,err=billingexpr.ComputeTieredQuota(snap,billingexpr.TokenParams{P:1000000,Len:300000});require.NoError(t,err);require.Equal(t,common.QuotaFromFloat(3.3*common.QuotaPerUnit),result.ActualQuotaAfterGroup)
}
func TestOmniPrivateRemarkOnlyInAdminMetadata(t *testing.T) {
 privateTestOptions(t);info:=privateInfo();require.NoError(t,OmniPreparePrivateBilling(info))
 info.TieredBillingSnapshot=&billingexpr.BillingSnapshot{ExprString:info.OmniPriceExpression}
 other:=model.NewLogOther();InjectTieredBillingInfo(other,info,nil)
 var row map[string]any;require.NoError(t,json.Unmarshal([]byte(other.JSONString()),&row))
 require.NotContains(t,row,"private_billing");require.NotContains(t,row,"expr_b64");require.Contains(t,row,"admin_info")
 require.Contains(t,row["admin_info"],"private_billing")
}
