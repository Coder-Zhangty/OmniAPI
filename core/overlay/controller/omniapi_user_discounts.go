package controller

import (
 "encoding/json"
 "io"
 "net/http"
 "strconv"

 "github.com/QuantumNous/new-api/common"
 "github.com/QuantumNous/new-api/model"
 "github.com/gin-gonic/gin"
 "github.com/google/uuid"
)

func omniDiscountTarget(c *gin.Context) (int,bool) {
 c.Header("Cache-Control","no-store")
 if c.GetInt("role")!=common.RoleRootUser {c.JSON(http.StatusForbidden,gin.H{"success":false,"message":"仅最高管理员可管理专属优惠"});return 0,false}
 id,err:=strconv.Atoi(c.Param("id"));if err!=nil||id<=0 {c.JSON(http.StatusBadRequest,gin.H{"success":false,"message":"无效的用户编号"});return 0,false}
 user,err:=model.GetUserById(id,false);if err!=nil||user==nil||user.Id!=id {c.JSON(http.StatusNotFound,gin.H{"success":false,"message":"用户不存在"});return 0,false}
 return id,true
}

func omniDiscountReply(c *gin.Context,id int,policy model.OmniUserDiscountPolicy) {
 rule,exists:=policy.Users[strconv.Itoa(id)]
 if !exists {rule=model.OmniUserDiscountRule{Enabled:false,Basis:"procurement_margin",TargetMargin:.4,FundingScope:"wallet",ModelScope:"text",PublicLabel:"价格优惠"}}
 c.JSON(http.StatusOK,gin.H{"success":true,"data":gin.H{"version":policy.Version,"user_id":id,"rule":rule,"legacy_conflict":model.OmniUserHasLegacyPrivateBilling(id)}})
}

func OmniUserDiscountGet(c *gin.Context) {
 id,ok:=omniDiscountTarget(c);if !ok{return}
 omniDiscountReply(c,id,model.GetOmniUserDiscounts())
}

func OmniUserDiscountPut(c *gin.Context) {
 id,ok:=omniDiscountTarget(c);if !ok{return}
 var body struct {ExpectedVersion *string `json:"expected_version"`;Enabled *bool `json:"enabled"`;TargetMargin *float64 `json:"target_margin"`}
 decoder:=json.NewDecoder(http.MaxBytesReader(c.Writer,c.Request.Body,4096));decoder.DisallowUnknownFields()
 if decoder.Decode(&body)!=nil||body.ExpectedVersion==nil||body.Enabled==nil||body.TargetMargin==nil {c.JSON(http.StatusBadRequest,gin.H{"success":false,"message":"优惠设置格式无效"});return}
 var extra any
 if decoder.Decode(&extra)!=io.EOF {c.JSON(http.StatusBadRequest,gin.H{"success":false,"message":"优惠设置格式无效"});return}
 before:=model.GetOmniUserDiscounts()
 if before.Version!=*body.ExpectedVersion {c.JSON(http.StatusConflict,gin.H{"success":false,"message":"优惠设置已被修改，请重新加载后再保存"});return}
 next:=model.OmniUserDiscountPolicy{Version:uuid.NewString(),Users:make(map[string]model.OmniUserDiscountRule,len(before.Users)+1)}
 for key,rule:=range before.Users {next.Users[key]=rule}
 next.Users[strconv.Itoa(id)]=model.OmniUserDiscountRule{Enabled:*body.Enabled,Basis:"procurement_margin",TargetMargin:*body.TargetMargin,FundingScope:"wallet",ModelScope:"text",PublicLabel:"价格优惠"}
 if err:=model.OmniValidateUserDiscounts(next);err!=nil {c.JSON(http.StatusBadRequest,gin.H{"success":false,"message":"毛利率必须在0%至90%范围内，且仅支持文字模型钱包优惠"});return}
 if err:=model.OmniUpdateUserDiscounts(before.Version,next);err!=nil {c.JSON(http.StatusConflict,gin.H{"success":false,"message":"保存失败：设置已变化，或该用户已有旧私有计费规则，请重新加载核对"});return}
 markAuditLogged(c)
 model.RecordAuditLog(c,model.AuditLog{UserId:c.GetInt("id"),Username:c.GetString("username"),ActorRole:c.GetInt("role"),Category:model.AuditCategoryOperation,Action:"user.price_discount.update",Status:http.StatusOK,Success:true,Content:"修改用户专属钱包优惠",Other:model.AuditOther{Op:&model.AuditOperation{Action:"user.price_discount.update",Params:model.AuditFields{"target_user_id":id}},AdminInfo:auditOperatorInfo(c),RootInfo:model.AuditFields{"before_version":before.Version,"after_version":next.Version,"before":before.Users[strconv.Itoa(id)],"after":next.Users[strconv.Itoa(id)]}}})
 omniDiscountReply(c,id,next)
}
