package controller

import (
 "encoding/json"
 "net/http"
 "net/http/httptest"
 "strings"
 "testing"

 "github.com/QuantumNous/new-api/common"
 "github.com/QuantumNous/new-api/model"
 "github.com/gin-gonic/gin"
 "github.com/glebarez/sqlite"
 "github.com/stretchr/testify/require"
 "gorm.io/gorm"
)

func omniDiscountControllerFixture(t *testing.T) *gorm.DB {
 t.Helper();previousDB,previousLogs,previousOptions:=model.DB,model.LOG_DB,common.OptionMap
 db,err:=gorm.Open(sqlite.Open(":memory:"),&gorm.Config{});require.NoError(t,err)
 connection,err:=db.DB();require.NoError(t,err);connection.SetMaxOpenConns(1)
 require.NoError(t,db.AutoMigrate(&model.User{},&model.Option{},&model.AuditLog{}))
 model.DB,model.LOG_DB=db,db;common.OptionMap=map[string]string{}
 require.NoError(t,db.Create(&model.User{Id:16,Username:"discount-fixture",Role:common.RoleCommonUser,Status:common.UserStatusEnabled}).Error)
 t.Cleanup(func(){model.DB,model.LOG_DB,common.OptionMap=previousDB,previousLogs,previousOptions;_ = connection.Close()})
 return db
}
func omniDiscountCall(role int,method,path,body string,handler gin.HandlerFunc) *httptest.ResponseRecorder {
 rec:=httptest.NewRecorder();c,_:=gin.CreateTestContext(rec);c.Request=httptest.NewRequest(method,path,strings.NewReader(body));c.Request.Header.Set("Content-Type","application/json");c.Set("role",role);c.Set("id",1);c.Set("username","root-fixture");c.Params=gin.Params{{Key:"id",Value:"16"}};handler(c);return rec
}
func TestOmniUserDiscountControllerRootOnly(t *testing.T) {
 omniDiscountControllerFixture(t)
 for _,role:=range []int{common.RoleGuestUser,common.RoleCommonUser,common.RoleAdminUser} {
  require.Equal(t,http.StatusForbidden,omniDiscountCall(role,"GET","/api/omni/admin/users/16/discount","",OmniUserDiscountGet).Code)
  require.Equal(t,http.StatusForbidden,omniDiscountCall(role,"PUT","/api/omni/admin/users/16/discount",`{"expected_version":"","enabled":true,"target_margin":0.4}`,OmniUserDiscountPut).Code)
 }
}
func TestOmniUserDiscountControllerCASAuditAndFixedScope(t *testing.T) {
 db:=omniDiscountControllerFixture(t)
 first:=omniDiscountCall(common.RoleRootUser,"GET","/api/omni/admin/users/16/discount","",OmniUserDiscountGet)
 require.Equal(t,200,first.Code);require.Contains(t,first.Body.String(),`"enabled":false`)
 for _,body:=range []string{`{"enabled":true,"target_margin":0.4}`,`{"expected_version":"","enabled":true,"target_margin":0.4,"funding_scope":"all"}`,`{"expected_version":"","enabled":true,"target_margin":0.4} {}`} {
  require.Equal(t,http.StatusBadRequest,omniDiscountCall(common.RoleRootUser,"PUT","/api/omni/admin/users/16/discount",body,OmniUserDiscountPut).Code)
 }
 saved:=omniDiscountCall(common.RoleRootUser,"PUT","/api/omni/admin/users/16/discount",`{"expected_version":"","enabled":true,"target_margin":0.4}`,OmniUserDiscountPut)
 require.Equal(t,200,saved.Code,saved.Body.String())
 var response struct {Data struct {Version string `json:"version"`;Rule model.OmniUserDiscountRule `json:"rule"`} `json:"data"`}
 require.NoError(t,json.Unmarshal(saved.Body.Bytes(),&response));require.NotEmpty(t,response.Data.Version);require.Equal(t,"wallet",response.Data.Rule.FundingScope);require.Equal(t,"text",response.Data.Rule.ModelScope)
 require.Equal(t,http.StatusConflict,omniDiscountCall(common.RoleRootUser,"PUT","/api/omni/admin/users/16/discount",`{"expected_version":"","enabled":false,"target_margin":0.4}`,OmniUserDiscountPut).Code)
 var audit model.AuditLog;require.NoError(t,db.Where("action = ?","user.price_discount.update").First(&audit).Error)
 require.NotNil(t,audit.Other.RootInfo["after"]);require.NotContains(t,audit.Other.Op.Params,"target_margin")
 encoded,_:=json.Marshal(audit.Other.Op.Params);require.NotContains(t,string(encoded),"0.4")
}
func TestOmniUserDiscountGenericOptionsNeverReadOrWriteRules(t *testing.T) {
 db:=omniDiscountControllerFixture(t);common.OptionMap["OmniAPIUserDiscounts"]=`{"version":"sensitive","users":{"16":{"enabled":true,"target_margin":0.4}}}`
 list:=omniDiscountCall(common.RoleRootUser,"GET","/api/option/","",GetOptions)
 require.Equal(t,200,list.Code);require.NotContains(t,list.Body.String(),"OmniAPIUserDiscounts");require.NotContains(t,list.Body.String(),"sensitive")
 update:=omniDiscountCall(common.RoleRootUser,"PUT","/api/option/",`{"key":"OmniAPIUserDiscounts","value":"{}"}`,UpdateOption)
 require.Equal(t,http.StatusForbidden,update.Code)
 var count int64;require.NoError(t,db.Model(&model.Option{}).Where("key = ?","OmniAPIUserDiscounts").Count(&count).Error);require.Zero(t,count)
}
