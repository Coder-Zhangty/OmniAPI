package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
)

func TestOmniXorCashierLifecycleAndPolicy(t *testing.T) {
	xorTest(t)
	calls := 0
	xorHTTP = &http.Client{Transport: xorTransport(func(r *http.Request) (*http.Response, error) { calls++; return xorResponse(`{"status":"payed"}`), nil })}
	o, err := XorCreate(context.Background(), 91, "wechat", "1.00", "cashier_ready_1")
	if err != nil || o.State != "ready" || o.PaymentFlow != XorCashierFlow || o.AttemptedAt != 0 || calls != 0 {
		t.Fatal("cashier readiness", err)
	}
	grant, err := XorCheckoutGrant(o)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(XorCheckoutURL(o), xorCheckoutPage+"#grant=") {
		t.Fatal("grant must be fragment")
	}
	if _, err = XorCheck(context.Background(), o.ID); err == nil || calls != 0 {
		t.Fatal("unissued request queried")
	}
	first, err := XorCheckoutLaunch(grant)
	if err != nil || first.Action != "https://xorpay.com/api/cashier/123" || first.Fields["pay_type"] != "jsapi" || first.Fields["order_id"] != o.ID || first.Fields["price"] != "1.00" {
		t.Fatal("cashier launch", err)
	}
	if first.Fields["sign"] != XorSignature("OmniAPI 余额充值", "jsapi", "1.00", o.ID, xorNotifyURL, "synthetic-test-secret") {
		t.Fatal("cashier signature")
	}
	if first.Fields["return_url"] != XorCheckoutURL(o) || first.Fields["cancel_url"] != first.Fields["return_url"] {
		t.Fatal("untrusted return")
	}
	// New orders close immediately, while issued requests and original retries survive.
	if err = os.WriteFile(os.Getenv("OMNI_XORPAY_POLICY"), []byte(`{"enabled":false,"allowed_methods":[],"rollout_user_ids":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	replay, err := XorCreate(context.Background(), 91, "wechat", "1", "cashier_ready_1")
	if err != nil || replay.ID != o.ID {
		t.Fatal("replay blocked by admission", err)
	}
	if _, err = XorCreate(context.Background(), 91, "wechat", "1", "cashier_closed_new"); err == nil {
		t.Fatal("policy bypass")
	}
	again, err := XorCheckoutLaunch(grant)
	if err != nil || !reflect.DeepEqual(first, again) {
		t.Fatal("frozen form changed", err)
	}
	credited, err := XorCheck(context.Background(), o.ID)
	if err != nil || credited.State != "credited" || credited.AOID != nil || calls != 1 {
		t.Fatal("query without aoid", err)
	}
	if _, err = XorCheckoutLaunch(grant); err == nil {
		t.Fatal("credited order launched")
	}
	f := url.Values{"aoid": {"cashier_provider_1"}, "order_id": {o.ID}, "pay_price": {"1.0"}, "pay_time": {"2026-10-24 12:00:00"}}
	f.Set("sign", XorSignature(f.Get("aoid"), o.ID, f.Get("pay_price"), f.Get("pay_time"), "synthetic-test-secret"))
	if _, err = XorNotify(f); err != nil {
		t.Fatal(err)
	}
	if _, err = XorNotify(f); err != nil {
		t.Fatal(err)
	}
	var n int64
	model.DB.Model(&model.OmniLedger{}).Count(&n)
	if n != 1 {
		t.Fatal("duplicate credit")
	}
	var stored model.OmniTopup
	model.DB.First(&stored, "id = ?", o.ID)
	if stored.AOID == nil || *stored.AOID != "cashier_provider_1" {
		t.Fatal("late aoid missing")
	}
	raw, _ := json.Marshal(stored)
	if strings.Contains(string(raw), "paySign") || strings.Contains(string(raw), "cashier_form") || strings.Contains(string(raw), grant) {
		t.Fatal("private form serialized")
	}
}

func TestOmniXorCashierGrantsAndRollback(t *testing.T) {
	xorTest(t)
	o, err := XorCreate(context.Background(), 91, "wechat", "1", "cashier_grants_1")
	if err != nil {
		t.Fatal(err)
	}
	grant, _ := XorCheckoutGrant(o)
	parts := strings.Split(grant, ".")
	parts[1] = "xp_another_order"
	for _, bad := range []string{"", grant + "x", strings.Join(parts, "."), strings.Repeat("x", 300)} {
		if _, err = XorCheckoutOrder(bad); err == nil {
			t.Fatal("bad grant accepted")
		}
	}
	old := o
	old.CreatedAt = time.Now().Add(-25 * time.Hour).Unix()
	oldGrant, _ := XorCheckoutGrant(old)
	if _, err = XorCheckoutOrder(oldGrant); err == nil {
		t.Fatal("expired grant")
	}
	// Simulate a failed persistence before the browser receives signed parameters.
	model.DB.Callback().Update().Before("gorm:update").Register("cashier_fail", func(tx *gorm.DB) { tx.AddError(context.Canceled) })
	if _, err = XorCheckoutLaunch(grant); err == nil {
		t.Fatal("failed persistence exposed a signed form")
	}
	model.DB.Callback().Update().Remove("cashier_fail")
	var stored model.OmniTopup
	model.DB.First(&stored, "id = ?", o.ID)
	if stored.AttemptedAt != 0 || stored.CashierForm != "" || stored.State != "ready" {
		t.Fatal("launch transaction did not roll back")
	}
	// Concurrent opens are the same payment request, never a second provider order.
	var wg sync.WaitGroup
	forms := make(chan XorCashierLaunch, 8)
	fail := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); f, e := XorCheckoutLaunch(grant); forms <- f; fail <- e }()
	}
	wg.Wait()
	close(forms)
	close(fail)
	for e := range fail {
		if e != nil {
			t.Fatal(e)
		}
	}
	var first *XorCashierLaunch
	for f := range forms {
		if first == nil {
			a := f
			first = &a
		} else if !reflect.DeepEqual(*first, f) {
			t.Fatal("concurrent form differed")
		}
	}
	model.DB.Model(&model.OmniTopup{}).Where("id = ?", o.ID).Update("expires_at", time.Now().Add(-time.Second).Unix())
	if _, err = XorCheckoutLaunch(grant); err == nil {
		t.Fatal("expired payment launched")
	}
	if _, err = XorCheckoutOrder(grant); err != nil {
		t.Fatal("receipt lookup lost after payment expiry")
	}
	os.WriteFile(os.Getenv("OMNI_XORPAY_POLICY"), []byte(`{"enabled":true,"allowed_methods":[],"rollout_user_ids":[91]}`), 0600)
	cfg, _ := XorPayConfig()
	if len(cfg.Methods(91)) != 0 {
		t.Fatal("empty channels fail open")
	}
	os.Remove(os.Getenv("OMNI_XORPAY_POLICY"))
	if cfg.AllowsMethod(91, "wechat") {
		t.Fatal("missing policy fail open")
	}
}
