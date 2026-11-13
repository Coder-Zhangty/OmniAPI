package middleware

import (
 "net/http/httptest"
 "testing"
 "github.com/QuantumNous/new-api/common"
 "github.com/QuantumNous/new-api/model"
 "github.com/gin-gonic/gin"
 "github.com/stretchr/testify/require"
)
func TestOmniNativeIngressUsesAcceptedProtocolWithoutChangingChannel(t *testing.T) {
 common.OptionMapRWMutex.Lock();old:=common.OptionMap["OmniAPIModelRegistry"];common.OptionMap["OmniAPIModelRegistry"]=`{"enabled":true,"version":"native-ingress-test","models":{"test":{"version":"1","capability":"text","bindings":[{"channel_id":1,"upstream_model":"test","status":"verified","protocols":["openai","claude"]}]}}}`;common.OptionMapRWMutex.Unlock()
 t.Cleanup(func(){common.OptionMapRWMutex.Lock();common.OptionMap["OmniAPIModelRegistry"]=old;common.OptionMapRWMutex.Unlock()})
 c,_:=gin.CreateTestContext(httptest.NewRecorder());ch:=&model.Channel{Id:1,Type:1}
 c.Request=httptest.NewRequest("POST","/v1/messages",nil)
 require.Equal(t,14,omniNativeIngressChannelType(c,ch,"test"));require.Equal(t,1,ch.Type)
 c.Request=httptest.NewRequest("POST","/v1/chat/completions",nil);require.Equal(t,1,omniNativeIngressChannelType(c,ch,"test"))
 c.Request=httptest.NewRequest("POST","/v1/messages",nil);require.Equal(t,1,omniNativeIngressChannelType(c,ch,"unknown"))
 ch.Id=2;require.Equal(t,1,omniNativeIngressChannelType(c,ch,"test"))
 require.Equal(t,0,omniNativeIngressChannelType(c,nil,"test"))
}
