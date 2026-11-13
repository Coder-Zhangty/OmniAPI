package service

import (
	"context"
	"encoding/json"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type xorTransport func(*http.Request) (*http.Response, error)

func (f xorTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func xorResponse(s string) *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(s)), Header: make(http.Header)}
}
func xorTest(t *testing.T) {
	t.Helper()
	old := model.DB
	kind := common.MainDatabaseType()
	dialect := gorm.Dialector(sqlite.Open(":memory:"))
	dsn := os.Getenv("OMNI_TEST_DSN")
	if dsn != "" {
		parsed, e := url.Parse(dsn)
		if e != nil || parsed.Path != "/omniapi_v2_test" || parsed.Hostname() != "127.0.0.1" {
			t.Fatal("only isolated loopback omniapi_v2_test is allowed")
		}
		q := parsed.Query()
		q.Set("search_path", "xorpay_service_tests")
		parsed.RawQuery = q.Encode()
		dialect = postgres.Open(parsed.String())
		common.SetMainDatabaseType(common.DatabaseTypePostgreSQL)
	}
	db, err := gorm.Open(dialect, &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	model.DB = db
	sql, _ := db.DB()
	if dsn == "" {
		sql.SetMaxOpenConns(1)
	} else {
		sql.SetMaxOpenConns(12)
		if err = db.Exec("DROP SCHEMA IF EXISTS xorpay_service_tests CASCADE; CREATE SCHEMA xorpay_service_tests;").Error; err != nil {
			t.Fatal(err)
		}
	}
	if err = db.AutoMigrate(&model.User{}, &model.OmniTopup{}, &model.OmniAccount{}, &model.OmniLedger{}, &model.OmniNotice{}); err != nil {
		t.Fatal(err)
	}
	if err = db.Create(&model.User{Id: 91, Username: "xor-test"}).Error; err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "xorpay.json")
	raw, _ := json.Marshal(XorConfig{AID: "123", Secret: "synthetic-test-secret", GrantSecret: strings.Repeat("synthetic-grant-key-", 3), Enabled: true, Rollout: []int{91}})
	os.WriteFile(path, raw, 0600)
	t.Setenv("OMNI_XORPAY_CONFIG", path)
	policyPath := filepath.Join(t.TempDir(), "policy.json")
	policy, _ := json.Marshal(XorPolicy{Enabled: true, AllowedMethods: []string{"alipay", "wechat"}, Rollout: []int{91}})
	if err = os.WriteFile(policyPath, policy, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OMNI_XORPAY_POLICY", policyPath)
	oldHTTP, oldFX := xorHTTP, xorFXHTTP
	t.Cleanup(func() {
		xorHTTP = oldHTTP
		xorFXHTTP = oldFX
		model.DB = old
		common.SetMainDatabaseType(kind)
		sql.Close()
	})
	xorFXHTTP = &http.Client{Transport: xorTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "host.docker.internal:8787" {
			t.Fatal(r.URL.Host)
		}
		return xorResponse(`{"success":true,"rate":"6.7195","source":"fixture","as_of":"2026-10-22"}`), nil
	})}
}
func TestOmniXorPrecision(t *testing.T) {
	if _, _, err := XorConvert(100, "6.7195"); err != nil {
		t.Fatal(err)
	}
	usd, quota, err := XorConvert(100, "6.7195")
	if err != nil || usd != "0.148820" || quota != 74410 {
		t.Fatal(usd, quota, err)
	}
	for _, s := range []string{"1.001", "-1", "1e2", "NaN", "Infinity", " 1", ".1"} {
		if _, err := XorCents(s); err == nil {
			t.Fatal(s)
		}
	}
	if got := XorSignature("中文", "alipay", "1.00", "id", "notify", "secret"); got != "77525818c23c0883d4fd83ad3fdb6177" {
		t.Fatal("UTF-8 signature", got)
	}
}
func TestOmniXorCreateReplayAndRawNotify(t *testing.T) {
	xorTest(t)
	calls := 0
	xorHTTP = &http.Client{Transport: xorTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		r.ParseForm()
		f := r.PostForm
		if f.Get("price") != "1.00" || f.Get("pay_type") != "alipay" || f.Get("expire") != "900" || f.Get("sign") != XorSignature(f.Get("name"), "alipay", "1.00", f.Get("order_id"), xorNotifyURL, "synthetic-test-secret") {
			t.Fatal("invalid signed create")
		}
		return xorResponse(`{"status":"ok","aoid":"provider_test","info":{"qr":"https://qr.alipay.com/synthetic"}}`), nil
	})}
	o, err := XorCreate(context.Background(), 91, "alipay", "1.00", "request_123")
	if err != nil || o.State != "pending" {
		t.Fatal(o, err)
	}
	replay, err := XorCreate(context.Background(), 91, "alipay", "1", "request_123")
	if err != nil || replay.ID != o.ID || calls != 1 {
		t.Fatal(replay, err, calls)
	}
	if _, err = XorCreate(context.Background(), 91, "alipay", "2", "request_123"); err == nil {
		t.Fatal("changed replay accepted")
	}
	if _, err = XorCreate(context.Background(), 92, "alipay", "1", "request_456"); err == nil {
		t.Fatal("rollout bypass")
	}
	cfg, _ := XorPayConfig()
	cfg.Enabled = false
	raw, _ := json.Marshal(cfg)
	os.WriteFile(os.Getenv("OMNI_XORPAY_CONFIG"), raw, 0600)
	if replay, err = XorCreate(context.Background(), 91, "alipay", "1", "request_123"); err != nil || replay.ID != o.ID || calls != 1 {
		t.Fatal("closed admission lost original order", err)
	}
	if _, err = XorCreate(context.Background(), 91, "alipay", "1", "request_new_closed"); err == nil {
		t.Fatal("closed admission created a new order")
	}
	f := url.Values{"aoid": {"provider_test"}, "order_id": {o.ID}, "pay_price": {"1.0"}, "pay_time": {"2026-10-22 12:00:00"}, "more": {"{\"user_id\":92,\"quota\":999999999}"}}
	f.Set("sign", XorSignature(f.Get("aoid"), o.ID, "1.0", f.Get("pay_time"), "synthetic-test-secret"))
	got, err := XorNotify(f)
	if err != nil || got.State != "credited" {
		t.Fatal(got, err)
	}
	if _, err = XorNotify(f); err != nil {
		t.Fatal(err)
	}
	f.Set("pay_price", "1.00")
	if _, err = XorNotify(f); err == nil {
		t.Fatal("signature verified after amount normalization")
	}
	var n int64
	model.DB.Model(&model.OmniLedger{}).Count(&n)
	if n != 1 {
		t.Fatal(n)
	}
}
func TestOmniXorUnknownCreationAndQueryRecovery(t *testing.T) {
	xorTest(t)
	xorHTTP = &http.Client{Transport: xorTransport(func(r *http.Request) (*http.Response, error) { return nil, context.DeadlineExceeded })}
	o, err := XorCreate(context.Background(), 91, "alipay", "1", "request_unknown")
	if err != nil || o.State != "unknown" {
		t.Fatal(o, err)
	}
	// Query2 is documented to return only status: no invented amount or AOID.
	xorHTTP = &http.Client{Transport: xorTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/query2/123" || r.URL.Query().Get("order_id") != o.ID {
			t.Fatal(r.URL.Path)
		}
		return xorResponse(`{"status":"payed"}`), nil
	})}
	got, err := XorCheck(context.Background(), o.ID)
	if err != nil || got.State != "credited" || got.AOID != nil {
		t.Fatal(got, err)
	}
	unknown := model.OmniTopup{ID: "xp_not_attempted", UserID: 91, RequestID: "never_sent", AID: "123", Cents: 100, Quota: 74410, CreatedAt: time.Now().Unix()}
	model.DB.Create(&unknown)
	if _, err = XorCheck(context.Background(), unknown.ID); err == nil {
		t.Fatal("credited request never sent")
	}
}
