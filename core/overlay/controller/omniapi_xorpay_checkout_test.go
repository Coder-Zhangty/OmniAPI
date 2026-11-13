package controller

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestOmniTopupCashierGrantBoundaries(t *testing.T) {
	old := model.DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	model.DB = db
	sql, _ := db.DB()
	sql.SetMaxOpenConns(1)
	t.Cleanup(func() { model.DB = old; sql.Close() })
	if err = db.AutoMigrate(&model.OmniTopup{}); err != nil {
		t.Fatal(err)
	}
	cfg := service.XorConfig{AID: "123", Secret: "synthetic-secret", GrantSecret: strings.Repeat("synthetic-grant-key-", 3), Enabled: true}
	raw, _ := json.Marshal(cfg)
	p := filepath.Join(t.TempDir(), "merchant.json")
	os.WriteFile(p, raw, 0600)
	t.Setenv("OMNI_XORPAY_CONFIG", p)
	t.Setenv("OMNI_XORPAY_POLICY", filepath.Join(t.TempDir(), "missing-policy.json"))
	now := time.Now().Unix()
	o := model.OmniTopup{ID: "xp_controller_cashier", UserID: 91, RequestID: "controller_cashier", AID: "123", Method: "wechat", PaymentFlow: service.XorCashierFlow, Cents: 100, Quota: 74410, USD: "0.148820", State: "ready", CreatedAt: now, ExpiresAt: now + 900}
	if err = db.Create(&o).Error; err != nil {
		t.Fatal(err)
	}
	grant, err := service.XorCheckoutGrant(o)
	if err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.POST("/status", OmniXorCheckoutStatus)
	r.POST("/check", OmniXorCheckoutCheck)
	r.POST("/launch", OmniXorCheckoutLaunch)
	request := func(path, body, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	body, _ := json.Marshal(map[string]any{"grant": grant, "user_id": 92, "cents": 99999999, "return_url": "https://untrusted.invalid/"})
	w := request("/status", string(body), "https://omniapi.biz")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code)
	}
	for _, private := range []string{"user_id", "access_token", "cashier_form", "payment_url", "synthetic-grant-key", "synthetic-secret"} {
		if strings.Contains(w.Body.String(), private) {
			t.Fatal("private status field", private)
		}
	}
	var response struct {
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
	}
	if json.Unmarshal(w.Body.Bytes(), &response) != nil || !response.Success || response.Data["cents"] != float64(100) {
		t.Fatal("status changed original money")
	}
	if request("/status", `{"grant":"invalid"}`, "").Code != 403 {
		t.Fatal("invalid grant accepted")
	}
	if request("/launch", string(body), "https://untrusted.invalid").Code != 403 {
		t.Fatal("foreign origin accepted")
	}
	if request("/launch", `{`, "").Code != 400 {
		t.Fatal("invalid body accepted")
	}
	w = request("/launch", string(body), "https://omniapi.biz")
	var launch struct {
		Success bool                     `json:"success"`
		Data    service.XorCashierLaunch `json:"data"`
	}
	if json.Unmarshal(w.Body.Bytes(), &launch) != nil || !launch.Success || launch.Data.Fields["order_uid"] != "91" || launch.Data.Fields["price"] != "1.00" || launch.Data.Fields["return_url"] != "https://omniapi.biz/brand/checkout.html#grant="+grant {
		t.Fatal("untrusted launch field")
	}
	// Creating admission is closed by missing policy; the original handoff still works.
	if cfg.AllowsMethod(91, "wechat") {
		t.Fatal("missing policy opened new orders")
	}
	var stored model.OmniTopup
	db.First(&stored, "id = ?", o.ID)
	if stored.UserID != 91 || stored.AttemptedAt == 0 || stored.CashierForm == "" {
		t.Fatal("handoff not persisted")
	}
	ownerRaw, _ := json.Marshal(topupView(stored))
	if !strings.Contains(string(ownerRaw), "payment_url") || !strings.Contains(string(ownerRaw), "data:image/png;base64,") || strings.Contains(string(ownerRaw), "synthetic-secret") || strings.Contains(string(ownerRaw), "cashier_form") {
		t.Fatal("owner checkout view invalid")
	}
}
