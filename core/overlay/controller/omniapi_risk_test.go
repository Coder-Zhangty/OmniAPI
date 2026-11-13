package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestOmniRiskRootAccessBoundary(t *testing.T) {
	user, token := setupAccessTokenAudit(t)
	require.NoError(t, model.OmniRiskMigrate(model.DB))
	router := gin.New()
	router.GET("/api/omni/admin/risk/policy", middleware.RootAuth(), OmniRiskPolicy)
	require.Equal(t, 401, auditRequest(router, "GET", "/api/omni/admin/risk/policy", "").Code)
	for _, role := range []int{common.RoleCommonUser, common.RoleAdminUser} {
		require.NoError(t, modelRoleForMonitor(t, user.Id, role))
		require.Equal(t, 403, auditRequest(router, "GET", "/api/omni/admin/risk/policy", token).Code)
	}
	require.NoError(t, modelRoleForMonitor(t, user.Id, common.RoleRootUser))
	require.Equal(t, 200, auditRequest(router, "GET", "/api/omni/admin/risk/policy", token).Code)
}
