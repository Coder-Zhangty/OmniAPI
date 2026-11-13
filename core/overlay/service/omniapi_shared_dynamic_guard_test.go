package service

import (
 "crypto/sha256"
 "encoding/hex"
 "encoding/json"
 "strings"
 "testing"
 "time"
 "github.com/QuantumNous/new-api/common"
 "github.com/QuantumNous/new-api/model"
 relaycommon "github.com/QuantumNous/new-api/relay/common"
 "github.com/QuantumNous/new-api/relaykit/dto"
 "github.com/stretchr/testify/require"
)

func TestOmniSharedDynamicGuardKeepsFrozenReservationAcrossDispatch(t *testing.T) {
 c,info:=actualAllowanceFixture(t)
 common.OptionMapRWMutex.Lock();oldRouting:=common.OptionMap["OmniAPIRouting"];common.OptionMap["OmniAPIRouting"]="{}";priceRaw:=common.OptionMap["OmniAPIPricing"];common.OptionMapRWMutex.Unlock()
 t.Cleanup(func(){common.OptionMapRWMutex.Lock();common.OptionMap["OmniAPIRouting"]=oldRouting;common.OptionMapRWMutex.Unlock()})
 hash:=func(raw string)string{sum:=sha256.Sum256([]byte(raw));return hex.EncodeToString(sum[:])}
 p:=model.OmniCommerceProfile{Models:`{"test-model":[7]}`,PricingHash:hash(priceRaw),RoutingHash:hash("{}")}
 bound:=model.OmniCapabilityBound{MaxQuota:9000,MaxRequestBytes:4096,MaxOutputTokens:1024,LocalControlEvidence:"synthetic finite text bound",AcceptanceHash:strings.Repeat("a",64),VerifiedAt:time.Now().Unix(),RequestBasedReservation:true}
 matrix:=model.OmniCapabilityMatrix{SharedScope:"bounded_text",ScopeApproval:strings.Repeat("b",64),PricingHash:p.PricingHash,RoutingHash:p.RoutingHash,Bounds:map[string]map[int]map[string]model.OmniCapabilityBound{"test-model":{7:{"/v1/responses":bound}}}}
 data,_:=json.Marshal(matrix);p.CapabilityMatrix=string(data);profile,_:=json.Marshal(p)
 limit:=uint(100);info.Request=&dto.OpenAIResponsesRequest{MaxOutputTokens:&limit,Input:json.RawMessage(`"hello"`)}
 info.RequestURLPath="/v1/responses";info.OmniPoolID="synthetic-pool";info.OmniPriceProtected=true;info.OmniPriceExpression="synthetic-expression";info.ChannelMeta=&relaycommon.ChannelMeta{ChannelId:7}
 require.NoError(t,model.DB.Model(&model.OmniAccount{}).Where("user_id = ?",info.UserId).Update("net_recharge_cents",1).Error)
 require.NoError(t,model.DB.Create(&model.OmniSharedMember{PoolID:info.OmniPoolID,UserID:info.UserId,State:"active",BudgetMicros:-1}).Error)
 require.NoError(t,model.DB.Create(&model.OmniReservation{RequestID:info.RequestId,UserID:info.UserId,Source:"omni_shared",SharedPoolID:info.OmniPoolID,CommerceProfile:string(profile),State:"pending",Reserved:1000}).Error)
 require.NoError(t,model.DB.Create(&model.OmniSharedHold{RequestID:info.RequestId,PoolID:info.OmniPoolID,UserID:info.UserId,State:"pending",Reserved:1000,DispatchState:"reserved"}).Error)
 require.NoError(t,omniValidateCertifiedShared(info,p));require.True(t,info.OmniSharedDynamic)
 info.OmniSharedUpperQuota=1000
 for i:=0;i<3;i++ {require.NoError(t,OmniRiskRequestGuard(c,info));require.EqualValues(t,1000,info.OmniSharedUpperQuota)}
 info.OmniSharedUpperQuota=1001;require.Error(t,OmniRiskRequestGuard(c,info),"cannot increase beyond the durable held amount")
}
