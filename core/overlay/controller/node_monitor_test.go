package controller

import (
 "net/http"
 "net/http/httptest"
 "strings"
 "testing"
 "github.com/QuantumNous/new-api/common"
 "github.com/QuantumNous/new-api/middleware"
 "github.com/QuantumNous/new-api/model"
 "github.com/gin-gonic/gin"
 "github.com/stretchr/testify/require"
)

func modelRoleForMonitor(t *testing.T,id,role int) error {
 t.Helper()
 return model.DB.Model(&model.User{}).Where("id = ?",id).Update("role",role).Error
}

func TestOmniNodeMonitorRootBoundary(t *testing.T) {
 user,token:=setupAccessTokenAudit(t)
 upstream:=httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  require.Equal(t,"Bearer "+strings.Repeat("m",40),r.Header.Get("Authorization"))
  w.Header().Set("Content-Type","application/json");w.Write([]byte(`{"success":true,"data":{"checked":true}}`))
 }));defer upstream.Close()
 t.Setenv("OMNI_NODE_MONITOR_URL",upstream.URL);t.Setenv("OMNI_NODE_MONITOR_ADMIN_TOKEN",strings.Repeat("m",40))
 router:=gin.New();router.GET("/api/omni/admin/node-monitor/*path",middleware.RootAuth(),NodeMonitorProxy)
 require.Equal(t,401,auditRequest(router,"GET","/api/omni/admin/node-monitor/snapshot","").Code)
 for _,role:=range []int{common.RoleCommonUser,common.RoleAdminUser} {
  require.NoError(t,modelRoleForMonitor(t,user.Id,role))
  require.Equal(t,403,auditRequest(router,"GET","/api/omni/admin/node-monitor/snapshot",token).Code)
 }
 require.NoError(t,modelRoleForMonitor(t,user.Id,common.RoleRootUser))
 require.Equal(t,200,auditRequest(router,"GET","/api/omni/admin/node-monitor/snapshot",token).Code)
 require.Equal(t,404,auditRequest(router,"GET","/api/omni/admin/node-monitor/arbitrary-command",token).Code)
}
