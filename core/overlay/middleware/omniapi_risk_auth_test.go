package middleware

import (
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOmniRiskRegistrationCommitAndFailedLoginAudit(t *testing.T) {
	old := model.DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() { model.DB = old })
	require.NoError(t, model.OmniRiskMigrate(db))
	r := gin.New()
	r.Use(OmniRiskAuthAudit())
	r.POST("/api/user/register", func(c *gin.Context) {
		c.Set("risk_auth_registered", true)
		c.Set("risk_auth_user_id", 42)
		c.Set("risk_auth_granted_quota", int64(1000))
		c.JSON(200, gin.H{"success": false, "message": "default token later failed"})
	})
	r.POST("/api/user/login", func(c *gin.Context) { c.JSON(200, gin.H{"success": false}) })
	for _, path := range []string{"/api/user/register", "/api/user/login"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", path, strings.NewReader(`{"password":"must-not-log","username":"do-not-export"}`))
		r.ServeHTTP(w, req)
	}
	var events []model.OmniRiskAuthEvent
	require.NoError(t, db.Order("id").Find(&events).Error)
	require.Len(t, events, 2)
	require.Equal(t, "registration.success", events[0].Action)
	require.True(t, events[0].Success)
	require.Equal(t, int64(1000), events[0].GrantedQuota)
	require.Equal(t, "login.failed", events[1].Action)
	require.False(t, events[1].Success)
}
