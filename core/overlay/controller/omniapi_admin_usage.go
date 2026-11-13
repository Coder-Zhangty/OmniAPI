package controller

import (
 "net/http"
 "strconv"
 "github.com/QuantumNous/new-api/common"
 "github.com/QuantumNous/new-api/model"
 "github.com/QuantumNous/new-api/service/authz"
 "github.com/QuantumNous/new-api/i18n"
 "github.com/gin-gonic/gin"
)

// GetOmniAdminUser preserves the existing user endpoint and role boundary,
// adding a read-only allowance projection alongside the wallet balance.
func GetOmniAdminUser(c *gin.Context) {
 id,err:=strconv.Atoi(c.Param("id"));if err!=nil {common.ApiError(c,err);return}
 user,err:=model.GetUserById(id,false);if err!=nil {common.ApiError(c,err);return}
 if !canManageTargetRole(c.GetInt("role"),user.Role) {common.ApiErrorI18n(c,i18n.MsgUserNoPermissionSameLevel);return}
 usage,err:=model.OmniReadAdminUsage(id);if err!=nil {common.ApiError(c,err);return}
 user.AdminPermissions=authz.Capabilities(user.Id,user.Role)
 c.Header("Cache-Control","no-store")
 c.JSON(http.StatusOK,gin.H{"success":true,"message":"","data":struct {
  *model.User
  Billing *model.OmniAdminUsage `json:"billing"`
 }{User:user,Billing:usage}})
}
