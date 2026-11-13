package middleware

import (
	"encoding/json"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

func TestOmniUserModelUpgrade(t *testing.T) {
	common.OptionMapRWMutex.Lock()
	if common.OptionMap == nil {
		common.OptionMap = map[string]string{}
	}
	oldR, oldP := common.OptionMap["OmniAPIModelRegistry"], common.OptionMap["OmniAPIPricing"]
	common.OptionMap["OmniAPIModelRegistry"] = `{"enabled":true,"version":"upgrade-test","models":{"deepseek-v4-flash":{"version":"4","capability":"text","aliases":["legacy-flash"]},"deepseek-v4.1-flash":{"version":"4.1","capability":"text"}},"user_model_upgrades":{"149":{"deepseek-v4-flash":"deepseek-v4.1-flash"}}}`
	common.OptionMap["OmniAPIPricing"] = `{"enabled":true,"version":"upgrade-price","models":{"deepseek-v4-flash":{"billing_expr":"old"},"deepseek-v4.1-flash":{"billing_expr":"new"}}}`
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap["OmniAPIModelRegistry"] = oldR
		common.OptionMap["OmniAPIPricing"] = oldP
		common.OptionMapRWMutex.Unlock()
	})
	for _, tc := range []struct {
		uid                int
		input, want, price string
	}{
		{149, "deepseek-v4-flash", "deepseek-v4.1-flash", "new"},
		{149, "legacy-flash", "deepseek-v4.1-flash", "new"},
		{149, "deepseek-v4.1-flash", "deepseek-v4.1-flash", "new"},
		{152, "deepseek-v4-flash", "deepseek-v4-flash", "old"},
		{0, "deepseek-v4-flash", "deepseek-v4-flash", "old"},
	} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Set("id", tc.uid)
		req := &ModelRequest{Model: tc.input}
		omniNormalizeModel(c, req)
		require.Equal(t, tc.want, req.Model)
		require.Equal(t, tc.price, c.GetString("omni_price_expression"))
		require.Equal(t, tc.input, c.GetString("omni_requested_alias"))
	}
	// A source-only token must not gain permission to the upgraded target.
	require.False(t, TokenModelLimitAllows(map[string]bool{"deepseek-v4-flash": true}, "deepseek-v4.1-flash"))
	var p model.OmniModelRegistry
	require.NoError(t, json.Unmarshal([]byte(`{"version":"bad","models":{},"user_model_upgrades":{"149":{"missing":"target"}}}`), &p))
	require.Error(t, model.OmniValidateRegistry(p))
}
