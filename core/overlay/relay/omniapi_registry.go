package relay
import (
 "github.com/QuantumNous/new-api/model"
 relaycommon "github.com/QuantumNous/new-api/relay/common"
 "github.com/gin-gonic/gin"
)
func omniRequestPrice(c *gin.Context,info *relaycommon.RelayInfo)(string,string,string){
 if !c.GetBool("omni_price_captured"){return model.OmniPriceAvailability(info.OriginModelName)}
 info.OmniPriceQuote=[]byte(c.GetString("omni_price_quote"));info.OmniPriceExpression=c.GetString("omni_price_expression")
 version:=model.OmniPublicPriceVersion(c.GetString("omni_price_snapshot_version"))
 if c.GetBool("omni_price_available"){return "available","",version}
 return "paused",model.OmniPausedReason(info.OriginModelName),version
}
