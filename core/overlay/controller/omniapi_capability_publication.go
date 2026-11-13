package controller

import (
 "errors"
 "github.com/QuantumNous/new-api/model"
 "github.com/gin-gonic/gin"
)
func OmniCapabilityPreview(c *gin.Context) {
 var input model.OmniCapabilityPublicationInput
 if c.ShouldBindJSON(&input)!=nil{omniReply(c,nil,errors.New("invalid capability publication"));return}
 result,err:=model.OmniPreviewCapabilityPublication(input,c.GetInt("id"));omniReply(c,result,err)
}
func OmniCapabilityReload(c *gin.Context) {version,err:=model.OmniReloadCapabilityOptions();omniReply(c,map[string]any{"version":version},err)}
