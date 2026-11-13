package service

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/QuantumNous/new-api/model"
)

func TestOmniXorQueryCannotEraseConcurrentNotifyConflict(t *testing.T) {
	xorTest(t)
	o := model.OmniTopup{ID: "xp_notify_query_conflict", UserID: 91, RequestID: "query_conflict", AID: "123", Cents: 100, Quota: 74410, State: "pending", AttemptedAt: 1}
	if err := model.DB.Create(&o).Error; err != nil {
		t.Fatal(err)
	}
	// The signed amount mismatch arrives after XorCheck has read the row, while
	// it waits for its provider response. Each provider response must preserve it.
	for _, providerStatus := range []string{"fee_error", "unknown_future_status", "new", "expire"} {
		model.DB.Model(&model.OmniTopup{}).Where("id = ?", o.ID).Updates(map[string]any{"state": "pending", "error_code": "", "last_check_at": 0})
		xorHTTP = &http.Client{Transport: xorTransport(func(r *http.Request) (*http.Response, error) {
			f := url.Values{"aoid": {"conflicting_receipt"}, "order_id": {o.ID}, "pay_price": {"0.99"}, "pay_time": {"2026-10-22 18:00:00"}}
			f.Set("sign", XorSignature(f.Get("aoid"), o.ID, f.Get("pay_price"), f.Get("pay_time"), "synthetic-test-secret"))
			if _, err := XorNotify(f); err == nil {
				t.Fatal("signed conflicting amount was accepted")
			}
			return xorResponse(`{"status":"` + providerStatus + `"}`), nil
		})}
		if _, err := XorCheck(context.Background(), o.ID); err != nil {
			t.Fatal(err)
		}
		var row model.OmniTopup
		model.DB.First(&row, "id = ?", o.ID)
		if row.ErrorCode != "notify_conflict" || row.State != "review" {
			t.Fatalf("%s erased quarantine: %+v", providerStatus, row)
		}
		model.DB.Model(&row).Update("last_check_at", 0)
		xorHTTP = &http.Client{Transport: xorTransport(func(*http.Request) (*http.Response, error) { return xorResponse(`{"status":"payed"}`), nil })}
		if _, err := XorCheck(context.Background(), o.ID); err == nil {
			t.Fatal("query bypassed concurrent notification conflict")
		}
	}
	var n int64
	model.DB.Model(&model.OmniLedger{}).Count(&n)
	if n != 0 {
		t.Fatal("conflicting payment credited", n)
	}
}
