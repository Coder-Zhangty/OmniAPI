package openai

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/require"
)

func TestOmniTrialUnpricedRouteSkipsBeforeSend(t *testing.T) {
	var firstCalls, secondCalls atomic.Int32
	c, info := omniHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
		firstCalls.Add(1)
		http.Error(w, "unexpected supplier call", 503)
	}, func(w http.ResponseWriter, r *http.Request) {
		secondCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"model":"test-model","choices":[{"message":{"role":"assistant","content":"ready"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":1}}`)
	})
	require.NoError(t, model.OmniRiskV2Migrate(model.DB))
	require.NoError(t, model.DB.Model(&model.OmniRiskPolicyV2{}).Where("id = 1").Updates(map[string]any{"shadow": false, "new_user_protection": true}).Error)
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", info.UserId).Updates(map[string]any{"role": 1, "created_at": time.Now().Unix()}).Error)
	require.NoError(t, model.DB.Where("user_id = ? AND type = 1", info.UserId).Delete(&model.Log{}).Error)
	secret := filepath.Join(t.TempDir(), "risk.json")
	require.NoError(t, os.WriteFile(secret, []byte(`{"hmac_key":"kkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkk","api_key":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}`), 0600))
	t.Setenv("OMNI_RISK_SECRETS_FILE", secret)
	common.OptionMapRWMutex.Lock()
	oldTrial, oldPricing := common.OptionMap[model.OmniTrialOption], common.OptionMap["OmniAPIPricing"]
	common.OptionMap[model.OmniTrialOption] = fmt.Sprintf(`{"mode":"pilot","user_ids":[%d]}`, info.UserId)
	common.OptionMap["OmniAPIPricing"] = `{"cny_per_usd":"7"}`
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		defer common.OptionMapRWMutex.Unlock()
		common.OptionMap[model.OmniTrialOption], common.OptionMap["OmniAPIPricing"] = oldTrial, oldPricing
	})
	ids := service.GetOmniRouting().Models[info.OriginModelName]
	info.IsStream = false
	payload := `{"model":"test-model","messages":[{"role":"user","content":"hello"}],"max_tokens":100}`
	var input dto.GeneralOpenAIRequest
	require.NoError(t, json.Unmarshal([]byte(payload), &input))
	info.Request = &input
	info.OmniPriceQuote = []byte(`{"costs":{}}`)
	a := &Adaptor{}
	_, err := a.omniRequest(c, info, strings.NewReader(payload))
	require.ErrorContains(t, err, "trial_quote_unavailable")
	require.NoError(t, model.OmniRiskAdmission(model.DB, info.UserId))
	require.Zero(t, firstCalls.Load())
	require.Zero(t, secondCalls.Load())

	info.RequestId += "-next"
	info.OmniPriceQuote = []byte(fmt.Sprintf(`{"costs":{"%d":{"complete_prices_verified":true,"input_usd_per_million":"1","output_usd_per_million":"2","cache_read_usd_per_million":"1","cache_write_usd_per_million":"1","cache_write_1h_usd_per_million":"1"}}}`, ids[1]))
	resp, err := a.omniRequest(c, info, strings.NewReader(payload))
	require.NoError(t, err)
	defer resp.Body.Close()
	result, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(result), "ready")
	require.Zero(t, firstCalls.Load())
	require.EqualValues(t, 1, secondCalls.Load())
	attempts := model.OmniRequestAttempts(info.RequestId)
	require.Len(t, attempts, 2)
	require.Equal(t, "skipped_before_send", attempts[0].Outcome)
	require.Equal(t, "completed", attempts[1].Outcome)
}
