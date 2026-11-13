package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/model"
)

func TestOmniXorCreationFailureSurvivesQuery(t *testing.T) {
	for _, tc := range []struct{ status, info, code string }{
		{"alipay_api_error", "ACQ.ACCESS_FORBIDDEN", "alipay_contract_required"},
		{"alipay_api_error", "aop.invalid-app-auth-token", "alipay_auth_invalid"},
		{"alipay_api_error", "synthetic-private-provider-detail", ""},
		{"fee_error", "", ""},
	} {
		t.Run(tc.status+tc.info, func(t *testing.T) {
			xorTest(t)
			xorHTTP = &http.Client{Transport: xorTransport(func(*http.Request) (*http.Response, error) {
				raw, _ := json.Marshal(map[string]string{"status": tc.status, "info": tc.info})
				return xorResponse(string(raw)), nil
			})}
			o, err := XorCreate(context.Background(), 91, "alipay", "1", "diagnosis_trial")
			if err != nil {
				t.Fatal(err)
			}
			verify := func(row model.OmniTopup) {
				t.Helper()
				raw, _ := json.Marshal(row)
				var view map[string]any
				json.Unmarshal(raw, &view)
				if view["create_status"] != tc.status {
					t.Fatalf("original creation failure lost: %s", raw)
				}
				if got, _ := view["create_error_code"].(string); got != tc.code {
					t.Fatalf("wrong safe diagnostic code: %q", got)
				}
				if strings.Contains(string(raw), "synthetic-private-provider-detail") {
					t.Fatal("raw provider detail exposed")
				}
			}
			verify(o)
			xorHTTP = &http.Client{Transport: xorTransport(func(*http.Request) (*http.Response, error) { return xorResponse(`{"status":"not_exist"}`), nil })}
			o, err = XorCheck(context.Background(), o.ID)
			if err != nil {
				t.Fatal(err)
			}
			if o.ProviderStatus != "not_exist" || o.ErrorCode != "provider_rejected" {
				t.Fatal("query or original rejection was lost")
			}
			verify(o)
			var n int64
			model.DB.Model(&model.OmniLedger{}).Count(&n)
			if n != 0 {
				t.Fatal("rejected payment credited")
			}
		})
	}
}
