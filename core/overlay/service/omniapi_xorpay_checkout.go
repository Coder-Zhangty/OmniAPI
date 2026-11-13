package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

const XorCashierFlow = "xorpay_cashier"
const xorCheckoutPage = "https://omniapi.biz/brand/checkout.html"

// A separate directory mount makes atomic policy updates visible without a
// gateway restart. Missing/invalid policy denies new orders, not reconciliation.
type XorPolicy struct {
	Enabled        bool     `json:"enabled"`
	AllowedMethods []string `json:"allowed_methods"`
	Rollout        []int    `json:"rollout_user_ids"`
}

func XorPayPolicy() XorPolicy {
	path := os.Getenv("OMNI_XORPAY_POLICY")
	if path == "" {
		path = "/run/omni-payments/policy.json"
	}
	raw, err := os.ReadFile(path)
	var p XorPolicy
	if err != nil || len(raw) > 16384 || json.Unmarshal(raw, &p) != nil {
		return XorPolicy{}
	}
	return p
}
func (c XorConfig) AllowsMethod(uid int, method string) bool {
	p := XorPayPolicy()
	if !c.Enabled || !p.Enabled || uid <= 0 {
		return false
	}
	allowed := len(p.Rollout) == 0
	for _, id := range p.Rollout {
		if id == uid {
			allowed = true
		}
	}
	if !allowed {
		return false
	}
	if method == "wechat" && len(c.GrantSecret) < 32 {
		return false
	}
	for _, m := range p.AllowedMethods {
		if m == method && (m == "wechat" || m == "alipay") {
			return true
		}
	}
	return false
}
func (c XorConfig) Methods(uid int) []string {
	result := []string{}
	for _, m := range []string{"wechat", "alipay"} {
		if c.AllowsMethod(uid, m) {
			result = append(result, m)
		}
	}
	return result
}
func xorGrantSignature(payload, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("omni-topup-checkout-v1\n" + payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func XorCheckoutGrant(o model.OmniTopup) (string, error) {
	cfg, err := XorPayConfig()
	if err != nil || len(cfg.GrantSecret) < 32 || o.AID != cfg.AID || o.PaymentFlow != XorCashierFlow || !xorID.MatchString(o.ID) || o.CreatedAt <= 0 {
		return "", errors.New("付款入口暂不可用，请保留原订单")
	}
	payload := "v1." + o.ID + "." + strconv.FormatInt(o.CreatedAt+86400, 10)
	return payload + "." + xorGrantSignature(payload, cfg.GrantSecret), nil
}
func XorCheckoutURL(o model.OmniTopup) string {
	grant, err := XorCheckoutGrant(o)
	if err != nil {
		return ""
	}
	return xorCheckoutPage + "#grant=" + grant
}
func XorCheckoutOrder(grant string) (model.OmniTopup, error) {
	var o model.OmniTopup
	invalid := errors.New("付款链接无效或已过期，请在原账户重新打开订单")
	if len(grant) > 256 {
		return o, invalid
	}
	parts := strings.Split(grant, ".")
	if len(parts) != 4 || parts[0] != "v1" || !xorID.MatchString(parts[1]) {
		return o, invalid
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || exp < time.Now().Unix() {
		return o, invalid
	}
	cfg, err := XorPayConfig()
	if err != nil || len(cfg.GrantSecret) < 32 {
		return o, invalid
	}
	expected := xorGrantSignature(strings.Join(parts[:3], "."), cfg.GrantSecret)
	if subtle.ConstantTimeCompare([]byte(expected), []byte(parts[3])) != 1 {
		return o, invalid
	}
	if err = model.DB.First(&o, "id = ?", parts[1]).Error; err != nil || o.PaymentFlow != XorCashierFlow || o.AID != cfg.AID || exp != o.CreatedAt+86400 {
		return model.OmniTopup{}, invalid
	}
	return o, nil
}

type XorCashierLaunch struct {
	Action string            `json:"action"`
	Fields map[string]string `json:"fields"`
}

// Freeze the full signed request before exposing it. Reopening on another
// device never switches payment type or extends the platform's expiry.
func XorCheckoutLaunch(grant string) (XorCashierLaunch, error) {
	result := XorCashierLaunch{}
	o, err := XorCheckoutOrder(grant)
	if err != nil {
		return result, err
	}
	cfg, err := XorPayConfig()
	if err != nil {
		return result, err
	}
	if cfg.AID != o.AID {
		return result, errors.New("原订单商户配置不一致，请联系客服")
	}
	err = model.DB.Session(&gorm.Session{Logger: logger.Discard}).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&o, "id = ?", o.ID).Error; err != nil {
			return err
		}
		now := time.Now().Unix()
		if o.State == "credited" {
			return errors.New("该订单已到账，请返回钱包")
		}
		if o.State == "review" {
			return errors.New("订单需要人工核对，请勿重复付款")
		}
		if o.ExpiresAt <= now || o.State == "expired" {
			return errors.New("订单已过期，请检查原订单付款结果")
		}
		if o.CashierForm != "" {
			if json.Unmarshal([]byte(o.CashierForm), &result.Fields) != nil {
				return errors.New("原订单参数暂不可用")
			}
			return nil
		}
		if o.AttemptedAt != 0 {
			return errors.New("原订单结果待确认，请勿重新下单")
		}
		callback := xorCheckoutPage + "#grant=" + grant
		fields := map[string]string{"name": "OmniAPI 余额充值", "pay_type": "jsapi", "price": xorAmount(o.Cents), "order_id": o.ID, "order_uid": strconv.Itoa(o.UserID), "notify_url": xorNotifyURL, "return_url": callback, "cancel_url": callback, "expire": strconv.FormatInt(o.ExpiresAt-now, 10)}
		fields["sign"] = XorSignature(fields["name"], fields["pay_type"], fields["price"], o.ID, xorNotifyURL, cfg.Secret)
		raw, err := json.Marshal(fields)
		if err != nil {
			return err
		}
		o.CashierForm = string(raw)
		o.AttemptedAt = now
		o.State = "pending"
		if err = tx.Save(&o).Error; err != nil {
			return errors.New("订单保存失败，请稍后重试原订单")
		}
		result.Fields = fields
		return nil
	})
	if err != nil {
		return XorCashierLaunch{}, err
	}
	result.Action = "https://xorpay.com/api/cashier/" + cfg.AID
	return result, nil
}
