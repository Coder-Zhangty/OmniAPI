package service

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const xorNotifyURL = "https://omniapi.biz/api/omni/payments/xorpay/notify"

var xorDecimal = regexp.MustCompile(`^[0-9]{1,10}(\.[0-9]{1,2})?$`)
var xorID = regexp.MustCompile(`^[A-Za-z0-9_-]{8,80}$`)
var xorDigits = regexp.MustCompile(`^[0-9]{1,32}$`)
var xorHTTP = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
var xorFXHTTP = &http.Client{Timeout: 4 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

type XorConfig struct {
	AID         string `json:"aid"`
	Secret      string `json:"app_secret"`
	Enabled     bool   `json:"enabled"`
	Rollout     []int  `json:"rollout_user_ids"`
	GrantSecret string `json:"checkout_grant_secret"`
}

func XorPayConfig() (XorConfig, error) {
	var c XorConfig
	path := os.Getenv("OMNI_XORPAY_CONFIG")
	if path == "" {
		path = "/run/secrets/xorpay.json"
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 16384 || json.Unmarshal(data, &c) != nil || !xorDigits.MatchString(c.AID) || len(c.Secret) < 8 {
		return c, errors.New("自动支付尚未配置，请使用人工应急入口")
	}
	return c, nil
}
func (c XorConfig) Allows(uid int) bool {
	p := XorPayPolicy()
	if !c.Enabled || !p.Enabled || uid <= 0 {
		return false
	}
	if len(p.Rollout) == 0 {
		return true
	}
	for _, id := range p.Rollout {
		if uid == id {
			return true
		}
	}
	return false
}
func XorSignature(parts ...string) string {
	h := md5.Sum([]byte(strings.Join(parts, "")))
	return hex.EncodeToString(h[:])
}
func XorCents(s string) (int64, error) {
	if !xorDecimal.MatchString(s) {
		return 0, errors.New("金额须为最多两位小数的正数")
	}
	parts := strings.SplitN(s, ".", 2)
	whole, _ := strconv.ParseInt(parts[0], 10, 64)
	fraction := "00"
	if len(parts) == 2 {
		fraction = (parts[1] + "00")[:2]
	}
	f, _ := strconv.ParseInt(fraction, 10, 64)
	return whole*100 + f, nil
}
func xorAmount(cents int64) string { return fmt.Sprintf("%d.%02d", cents/100, cents%100) }

type xorFX struct {
	Success bool   `json:"success"`
	Rate    string `json:"rate"`
	Source  string `json:"source"`
	AsOf    string `json:"as_of"`
	Note    string `json:"note"`
}

func xorConversion(ctx context.Context, cents int64) (xorFX, string, int64, error) {
	var fx xorFX
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://host.docker.internal:8787/brand/api/subscription/fx", nil)
	res, err := xorFXHTTP.Do(req)
	if err != nil {
		return fx, "", 0, errors.New("汇率服务暂不可用，请稍后再试")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || json.NewDecoder(io.LimitReader(res.Body, 16384)).Decode(&fx) != nil || !fx.Success || fx.Source == "" || fx.AsOf == "" {
		return fx, "", 0, errors.New("汇率服务暂不可用，请稍后再试")
	}
	usd, quota, err := XorConvert(cents, fx.Rate)
	return fx, usd, quota, err
}
func XorConvert(cents int64, rate string) (string, int64, error) {
	// Parse decimals as rationals: no float, exponent or intermediate rounding.
	if !regexp.MustCompile(`^[0-9]{1,2}(\.[0-9]{1,8})?$`).MatchString(rate) {
		return "", 0, errors.New("汇率无效")
	}
	r, ok := new(big.Rat).SetString(rate)
	if !ok || r.Cmp(big.NewRat(1, 1)) < 0 || r.Cmp(big.NewRat(20, 1)) > 0 || cents <= 0 || cents > 1000000 {
		return "", 0, errors.New("金额或汇率超出范围")
	}
	micros := new(big.Rat).Quo(big.NewRat(cents*10000, 1), r)
	m := new(big.Int).Quo(micros.Num(), micros.Denom()).Int64()
	return fmt.Sprintf("%d.%06d", m/1000000, m%1000000), m / 2, nil // 500000 quota/USD
}
func xorJSON(ctx context.Context, method, path string, form url.Values) (map[string]json.RawMessage, error) {
	address := "https://xorpay.com" + path
	var body io.Reader
	if method == "GET" {
		address += "?" + form.Encode()
	} else {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, address, body)
	if err != nil {
		return nil, errors.New("支付请求失败")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := xorHTTP.Do(req)
	if err != nil {
		return nil, errors.New("支付结果待确认")
	}
	defer res.Body.Close()
	var data map[string]json.RawMessage
	if res.StatusCode != 200 || json.NewDecoder(io.LimitReader(res.Body, 65536)).Decode(&data) != nil {
		return nil, errors.New("支付结果待确认")
	}
	return data, nil
}
func xorString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return ""
}

// Keep only explicitly documented diagnostic codes. Provider info can contain
// arbitrary account data and must never be copied into the order or API reply.
func xorCreateError(status string, info json.RawMessage) string {
	if status != "alipay_api_error" {
		return ""
	}
	var code string
	if json.Unmarshal(info, &code) != nil {
		return ""
	}
	switch code {
	case "ACQ.ACCESS_FORBIDDEN":
		return "alipay_contract_required"
	case "aop.invalid-app-auth-token":
		return "alipay_auth_invalid"
	}
	return ""
}

func XorCreate(ctx context.Context, uid int, method, amount, requestID string) (model.OmniTopup, error) {
	var o model.OmniTopup
	cents, err := XorCents(amount)
	if err != nil {
		return o, err
	}
	if cents < 100 || cents > 1000000 {
		return o, errors.New("人民币充值范围为 ¥1–¥10000")
	}
	if (method != "alipay" && method != "wechat") || !xorID.MatchString(requestID) {
		return o, errors.New("支付参数无效")
	}
	err = model.DB.Where("user_id = ? AND request_id = ?", uid, requestID).First(&o).Error
	if err == nil {
		if o.Method != method || o.Cents != cents {
			return o, errors.New("同一请求编号的支付参数发生变化")
		}
		return o, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return o, errors.New("订单服务暂不可用")
	}
	cfg, err := XorPayConfig()
	if err != nil {
		return o, err
	}
	if !cfg.AllowsMethod(uid, method) {
		return o, errors.New("自动支付暂未对当前账户开放")
	}
	fx, usd, quota, err := xorConversion(ctx, cents)
	if err != nil {
		return o, err
	}
	random := make([]byte, 20)
	if _, err = rand.Read(random); err != nil {
		return o, errors.New("订单服务暂不可用")
	}
	now := time.Now().Unix()
	o = model.OmniTopup{ID: "xp_" + hex.EncodeToString(random), UserID: uid, RequestID: requestID, AID: cfg.AID, Method: method, Cents: cents, USD: usd, Quota: quota, FxRate: fx.Rate, FxSource: fx.Source, FxAsOf: fx.AsOf, FxNote: fx.Note, State: "creating", CreatedAt: now, ExpiresAt: now + 900}
	if method == "wechat" {
		o.PaymentFlow = XorCashierFlow
		o.State = "ready"
	}
	// Concurrent HTTP retries must never make a second provider call.
	result := model.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&o)
	if result.Error != nil {
		return o, errors.New("订单服务暂不可用")
	}
	if result.RowsAffected == 0 {
		err = model.DB.Where("user_id = ? AND request_id = ?", uid, requestID).First(&o).Error
		if err == nil && (o.Method != method || o.Cents != cents) {
			err = errors.New("同一请求编号的支付参数发生变化")
		}
		return o, err
	}
	if o.PaymentFlow == XorCashierFlow {
		return o, nil
	}
	if err = model.DB.Model(&o).Update("attempted_at", now).Error; err != nil {
		return o, errors.New("订单服务暂不可用")
	}
	o.AttemptedAt = now
	payType := "alipay"
	if method == "wechat" {
		payType = "native"
	}
	name := "OmniAPI 余额充值"
	price := xorAmount(cents)
	form := url.Values{"name": {name}, "pay_type": {payType}, "price": {price}, "order_id": {o.ID}, "notify_url": {xorNotifyURL}, "order_uid": {strconv.Itoa(uid)}, "expire": {"900"}}
	form.Set("sign", XorSignature(name, payType, price, o.ID, xorNotifyURL, cfg.Secret))
	data, callErr := xorJSON(ctx, "POST", "/api/pay/"+cfg.AID, form)
	status := xorString(data["status"])
	var info struct {
		QR string `json:"qr"`
	}
	_ = json.Unmarshal(data["info"], &info)
	aoid := xorString(data["aoid"])
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&o, "id = ?", o.ID).Error; e != nil {
			return e
		}
		if o.State == "credited" && (o.PaidAt > 0 || !model.GetOmniPolicy().TopupPromotion.Valid()) {
			return nil
		}
		if len(status) <= 64 {
			o.ProviderStatus = status
			o.CreateStatus = status
		}
		o.CreateErrorCode = xorCreateError(status, data["info"])
		if callErr != nil {
			o.State = "unknown"
			o.ErrorCode = "create_unknown"
		} else if status == "ok" && info.QR != "" && aoid != "" && len(aoid) <= 100 && len(info.QR) <= 4096 {
			if o.AOID != nil && *o.AOID != aoid {
				return errors.New("payment reference conflict")
			}
			o.AOID = &aoid
			o.QR = info.QR
			o.State = "pending"
		} else {
			o.State = "unknown"
			o.ErrorCode = "provider_rejected"
		}
		return tx.Save(&o).Error
	})
	if err != nil {
		return o, errors.New("订单结果待确认，请检查原订单")
	}
	return o, nil
}

func XorCheck(ctx context.Context, id string) (model.OmniTopup, error) {
	var o model.OmniTopup
	if err := model.DB.First(&o, "id = ?", id).Error; err != nil {
		return o, err
	}
	if o.State == "credited" {
		return o, nil
	}
	cfg, err := XorPayConfig()
	if err != nil {
		return o, err
	}
	if o.AID != cfg.AID || o.AttemptedAt == 0 {
		return o, errors.New("订单尚未发起支付")
	}
	now := time.Now().Unix()
	claim := model.DB.Model(&model.OmniTopup{}).Where("id = ? AND last_check_at < ?", id, now-10).Update("last_check_at", now)
	if claim.Error != nil {
		return o, claim.Error
	}
	if claim.RowsAffected == 0 {
		return o, nil
	}
	data, err := xorJSON(ctx, "GET", "/api/query2/"+cfg.AID, url.Values{"order_id": {o.ID}, "sign": {XorSignature(o.ID, cfg.Secret)}})
	if err != nil {
		return o, errors.New("支付结果暂未确认，请稍后检查原订单")
	}
	status := xorString(data["status"])
	if status == "payed" || status == "success" {
		return model.OmniFulfillTopup(o.ID, cfg.AID, "", 0, false, xorPaymentEvidence(xorString(data["pay_time"]), "xorpay:authenticated_query"))
	}
	updates := map[string]any{"provider_status": status}
	switch status {
	case "new":
		if o.ExpiresAt > now && (o.QR != "" || o.PaymentFlow == XorCashierFlow) {
			updates["state"] = "pending"
		}
	case "expire":
		updates["state"] = "expired"
	case "fee_error":
		updates["state"] = "review"
		updates["error_code"] = "fee_error"
	case "not_exist":
	default:
		if o.ErrorCode != "provider_rejected" {
			updates["error_code"] = "query_unknown"
		}
	}
	// A query can race a signed conflicting notification. Never overwrite its
	// quarantine marker with a provider error and later auto-credit that order.
	if err = model.DB.Model(&model.OmniTopup{}).Where("id = ? AND state <> ? AND error_code <> ?", id, "credited", "notify_conflict").Updates(updates).Error; err != nil {
		return o, err
	}
	err = model.DB.First(&o, "id = ?", id).Error
	return o, err
}

func XorNotify(form url.Values) (model.OmniTopup, error) {
	var o model.OmniTopup
	cfg, err := XorPayConfig()
	if err != nil {
		return o, err
	}
	for _, key := range []string{"aoid", "order_id", "pay_price", "pay_time", "sign"} {
		if len(form[key]) != 1 || form.Get(key) == "" || len(form.Get(key)) > 128 {
			return o, errors.New("invalid notification")
		}
	}
	if !strings.HasPrefix(form.Get("order_id"), "xp_") {
		return o, errors.New("invalid order")
	}
	expected := XorSignature(form.Get("aoid"), form.Get("order_id"), form.Get("pay_price"), form.Get("pay_time"), cfg.Secret)
	if subtle.ConstantTimeCompare([]byte(expected), []byte(form.Get("sign"))) != 1 {
		return o, errors.New("invalid signature")
	}
	cents, err := XorCents(form.Get("pay_price"))
	if err != nil {
		return o, err
	}
	o, err = model.OmniFulfillTopup(form.Get("order_id"), cfg.AID, form.Get("aoid"), cents, true, xorPaymentEvidence(form.Get("pay_time"), "xorpay:signed_notify"))
	return o, err
}

func init() {
	model.OmniTopupReconcile = func(id string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		_, err := XorCheck(ctx, id)
		return err
	}
}

// Unknown provider formats leave only the bonus pending; principal still settles.
func xorPaymentEvidence(raw, source string) model.OmniPaymentEvidence {
	zone := time.FixedZone("Asia/Shanghai", 8*3600)
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if n > 100000000000 {
			n /= 1000
		}
		if n > 0 && n <= time.Now().Unix()+300 {
			return model.OmniPaymentEvidence{PaidAt: n, Source: source}
		}
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(layout, raw, zone); err == nil && t.Unix() > 0 && t.Unix() <= time.Now().Unix()+300 {
			return model.OmniPaymentEvidence{PaidAt: t.Unix(), Source: source}
		}
	}
	return model.OmniPaymentEvidence{}
}
