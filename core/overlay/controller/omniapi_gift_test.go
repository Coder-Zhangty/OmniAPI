package controller

import (
	"encoding/json"
	"fmt"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOmniGiftUserIsolationAndLegacyWalletEntry(t *testing.T) {
	user, bundle := appFixture(t)
	setting := operation_setting.GetPaymentSetting()
	old := *setting
	t.Cleanup(func() { *setting = old })
	setting.ComplianceConfirmed = true
	setting.ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion

	require.NoError(t, model.OmniMigrate())
	require.NoError(t, model.DB.AutoMigrate(&model.Log{}))
	campaign, e := model.OmniGiftCreate(1, "controller-gift-request1", "页面测试", "1", 3, 0)
	require.NoError(t, e)
	call := func(body string, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(r)
		c.Set("id", user.Id)
		c.Set("auth_version", int64(1))
		c.Request = httptest.NewRequest("POST", "https://omniapi.biz/api/user/topup", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Request.Header.Set("Origin", origin)
		TopUp(c)
		return r
	}
	for i := 0; i < 2; i++ {
		r := call(`{"key":"`+campaign.Code+`"}`, "https://omniapi.biz")
		require.Equal(t, 200, r.Code)
		var payload struct {
			Success bool
			Data    int64
		}
		require.NoError(t, json.Unmarshal(r.Body.Bytes(), &payload))
		if i == 0 {
			require.True(t, payload.Success, r.Body.String())
			require.Equal(t, int64(500000), payload.Data)
		} else {
			require.False(t, payload.Success)
			require.Zero(t, payload.Data)
			require.Contains(t, r.Body.String(), "gift_already_claimed")
		}
	}
	// A browser-supplied user ID is ignored by the new claim endpoint.
	r := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(r)
	c.Set("id", user.Id)
	c.Request = httptest.NewRequest("GET", "/api/omni/gifts?user_id=999", nil)
	OmniGiftSelf(c)
	require.NotContains(t, r.Body.String(), campaign.Code)
	var result struct{ Data []model.OmniGiftClaim }
	require.NoError(t, json.Unmarshal(r.Body.Bytes(), &result))
	require.Len(t, result.Data, 1)
	require.Equal(t, user.Id, result.Data[0].UserID)
	// RootAuth is exercised, rather than trusting a stored browser role.
	router := gin.New()
	router.GET("/admin", middleware.RootAuth(), OmniGiftAdminList)
	req := httptest.NewRequest("GET", "/admin", nil)
	req.Header.Set("Authorization", "Bearer "+bundle.AccessToken)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	require.NotEqual(t, 200, response.Code)
	// The explicit JSON write route rejects cross-origin requests.
	response = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(response)
	c.Set("id", user.Id)
	c.Request = httptest.NewRequest("POST", "/api/omni/gifts/redeem", strings.NewReader(`{"code":"`+campaign.Code+`"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("Origin", "https://untrusted.invalid")
	OmniGiftRedeem(c)
	require.Equal(t, 403, response.Code)
}

func TestOmniGiftCampaignListUsesCreationOrder(t *testing.T) {
	appFixture(t)
	require.NoError(t, model.OmniGiftMigrate(model.DB))
	for i := 0; i < 105; i++ {
		require.NoError(t, model.DB.Create(&model.OmniGiftCampaign{ID: fmt.Sprintf("gift_%04d", 104-i), Code: fmt.Sprintf("GIFT-%020d", i), CreatedAt: int64(100 + i), Version: 1}).Error)
	}
	call := func(path string) ([]model.OmniGiftCampaign, string) {
		r := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(r)
		c.Request = httptest.NewRequest("GET", path, nil)
		OmniGiftAdminList(c)
		var result struct {
			Data struct {
				Items []model.OmniGiftCampaign
				Next  string
			}
		}
		require.NoError(t, json.Unmarshal(r.Body.Bytes(), &result))
		return result.Data.Items, result.Data.Next
	}
	first, next := call("/api/omni/admin/gifts")
	require.Len(t, first, 100)
	require.EqualValues(t, 204, first[0].CreatedAt)
	require.NotEmpty(t, next)
	second, next := call("/api/omni/admin/gifts?before=" + next)
	require.Len(t, second, 5)
	require.EqualValues(t, 104, second[0].CreatedAt)
	require.Empty(t, next)
}
