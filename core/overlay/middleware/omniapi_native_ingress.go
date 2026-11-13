package middleware

import (
 "github.com/QuantumNous/new-api/model"
 "github.com/gin-gonic/gin"
)

// A native Messages client must reach the accepted native Messages adapter.
// Converting native -> Chat -> native discards hosted-tool state and usage.
// This changes the selected request context, never the stored channel type.
func omniNativeIngressChannelType(c *gin.Context,ch *model.Channel,name string) int {
 if ch==nil{return 0}
 if ch.Type!=1||c==nil||c.Request==nil||c.Request.URL.Path!="/v1/messages" {return ch.Type}
 registry:=model.OmniRegistryAt(c.GetString("omni_registry_version"))
 for _,b:=range registry.Models[registry.Canonical(name)].Bindings {
  if b.ChannelID!=ch.Id||b.Status!="verified" {continue}
  for _,protocol:=range b.Protocols {if protocol=="claude"||protocol=="anthropic" {return 14}}
 }
 return ch.Type
}
