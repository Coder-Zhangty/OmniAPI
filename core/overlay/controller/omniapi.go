package controller

import (
	"encoding/json"
	"errors"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net/http"
	"strings"
	"time"
)

func omniReply(c *gin.Context, value any, err error) {
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": model.OmniUserMessage(err.Error())})
		return
	}
	c.JSON(200, gin.H{"success": true, "data": value})
}
func OmniCatalog(c *gin.Context) {
	p := model.GetOmniPolicy()

	omniReply(c, gin.H{"topup_promotion": p.TopupPromotion, "server_time": time.Now().Unix(), "commerce_v3": OmniCommerceCatalog(), "plans": model.OmniPlans, "sales_enabled": !model.OmniCommerceSettings().Enabled && model.OmniSalesReady() && model.OmniUserInRollout(c.GetInt("id")), "rollout_active": len(p.RolloutUserIDs) > 0, "referrals_enabled": p.ReferralsEnabled && p.ReferralStartID > 0, "model_scope": p.ModelScope, "payment": p.Payment, "billing_version": 2, "quota_version": model.OmniQuotaVersion, "allowance_basis_days": 30}, nil)
}
func OmniCapabilities(c *gin.Context) { omniReply(c, model.OmniPublicCapabilities(), nil) }
func OmniAnnouncements(c *gin.Context) {
	rows, err := model.OmniAnnouncements(c.GetInt("id"))
	omniReply(c, rows, err)
}
func OmniEnableReferrals(c *gin.Context) { omniReply(c, true, model.OmniEnableReferrals()) }
func OmniImportReceipt(c *gin.Context) {
	var b struct {
		Key    string `json:"key"`
		UserID int    `json:"user_id"`
		Cents  int64  `json:"cents"`
		Quota  int64  `json:"quota"`
	}
	if c.ShouldBindJSON(&b) != nil || !strings.HasPrefix(b.Key, "legacy:") {
		omniReply(c, nil, errors.New("invalid legacy receipt"))
		return
	}
	omniReply(c, true, model.OmniImportReceipt(b.Key, b.UserID, b.Cents, b.Quota))
}
func OmniPricing(c *gin.Context) {
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap["OmniAPIPricing"]
	common.OptionMapRWMutex.RUnlock()
	var snapshot map[string]any
	if json.Unmarshal([]byte(raw), &snapshot) != nil {
		snapshot = map[string]any{}
	}
	public := map[string]any{}
	if rows, ok := snapshot["models"].(map[string]any); ok {
		for name, v := range rows {
			state := model.GetOmniModelRegistry().Models[name].Lifecycle
			if state == "retired" || state == "unavailable" {
				continue
			}
			if row, ok := v.(map[string]any); ok {
				clean := map[string]any{}
				for _, key := range []string{"billing_expr", "retail", "tiers", "restrictions", "common", "protocols", "billing_unit", "image_specs", "image_price_usd", "image_price_cny", "quality_pricing", "request_options", "availability"} {
					clean[key] = row[key]
				}
				clean["eligible"] = model.OmniEligible(name)
				public[name] = clean
			}
		}
	}
	publicWaiting := map[string]string{}
	for name := range model.GetOmniPrices().Waiting {
		state := model.GetOmniModelRegistry().Models[name].Lifecycle
		if state == "retired" || state == "unavailable" {
			continue
		}
		publicWaiting[name] = model.OmniPausedReason(name)
	}
	omniReply(c, map[string]any{"models": public, "waiting": publicWaiting, "verified_at": snapshot["verified_at"], "version": model.OmniPublicPriceVersion(model.GetOmniPrices().Version), "currency": snapshot["currency"], "cny_per_usd": snapshot["cny_per_usd"]}, nil)
}
func OmniSelf(c *gin.Context) {
	uid := c.GetInt("id")
	if err := model.OmniReconcile(uid); err != nil {
		omniReply(c, nil, err)
		return
	}
	p := model.GetOmniPolicy()
	if p.ReferralsEnabled && p.ReferralStartID > 0 {
		if err := model.OmniMigrateReferral(uid); err != nil {
			omniReply(c, nil, err)
			return
		}
	}
	var a model.OmniAccount
	err := model.DB.First(&a, "user_id = ?", uid).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		a = model.OmniAccount{UserID: uid, WalletOverflow: true}
		err = nil
	}
	if err != nil {
		omniReply(c, nil, err)
		return
	}
	var s model.OmniSubscription
	err = model.DB.First(&s, "user_id = ?", uid).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		omniReply(c, nil, err)
		return
	}
	var cards []model.OmniCard
	var notices []model.OmniNotice
	var receipts []model.OmniLedger
	if err = model.DB.Where("user_id = ?", uid).Order("created_at DESC").Limit(100).Find(&cards).Error; err != nil {
		omniReply(c, nil, err)
		return
	}
	if err = model.DB.Where("user_id = ? AND read = ? AND key NOT LIKE ?", uid, false, "announcement:%").Order("created_at DESC").Limit(30).Find(&notices).Error; err != nil {
		omniReply(c, nil, err)
		return
	}
	if err = model.DB.Where("user_id = ?", uid).Order("created_at DESC").Limit(100).Find(&receipts).Error; err != nil {
		omniReply(c, nil, err)
		return
	}
	level, percent := model.OmniLevel(a.NetRechargeCents)
	now := time.Now().Unix()
	if !p.MembershipEnabled {
		percent = 0
	}
	if promo := p.TopupPromotion; promo.Valid() && now >= promo.StartsAt && now < promo.EndsAt {
		percent = promo.BonusBPS / 100
	}
	// Read-only projection; the reservation transaction remains authoritative.
	if s.Start > 0 {
		if err = s.Project(now); err != nil {
			omniReply(c, nil, err)
			return
		}
	}
	var oldActive []model.UserSubscription
	if err := model.DB.Where("user_id = ? AND status = ? AND end_time > ?", uid, "active", now).Find(&oldActive).Error; err != nil {
		omniReply(c, nil, err)
		return
	}
	activeLegacy := []gin.H{}
	for _, row := range oldActive {
		var plan model.SubscriptionPlan
		if err := model.DB.Select("title").First(&plan, row.PlanId).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			omniReply(c, nil, err)
			return
		}
		if plan.Title == "" {
			plan.Title = "历史套餐"
		}
		activeLegacy = append(activeLegacy, gin.H{"title": model.OmniUserMessage(plan.Title), "end": row.EndTime})
	}
	status := "none"
	if s.ExitApplicationID != "" {
		status = "paused"
	} else if s.ActivationPending || s.NativeLegacyAwait {
		status = "processing"
	} else if s.End > now && s.Start <= now {
		status = "active"
	} else if s.Start > now {
		status = "processing"
	} else if s.End > 0 {
		status = "expired"
	}
	var pauses []model.OmniLegacyPause
	if err := model.DB.Where("user_id = ? AND state = ?", uid, "paused").Find(&pauses).Error; err != nil {
		omniReply(c, nil, err)
		return
	}
	legacy := []gin.H{}
	for _, row := range pauses {
		var plan model.SubscriptionPlan
		if err := model.DB.Select("title").First(&plan, row.PlanID).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			omniReply(c, nil, err)
			return
		}
		if plan.Title == "" {
			plan.Title = "历史套餐"
		}
		legacy = append(legacy, gin.H{"title": model.OmniUserMessage(plan.Title), "remaining_quota": row.Remaining, "remaining_seconds": row.RemainingSeconds})
	}
	windows := []gin.H{{"key": "five", "enabled": s.FiveEnabled(), "limit": s.FiveLimit, "used": s.FiveUsed, "remaining": max(int64(0), s.FiveLimit-s.FiveUsed), "reset_at": s.Start + (s.FiveEpoch+1)*18000}, {"key": "week", "enabled": true, "limit": s.WeekLimit, "used": s.WeekUsed, "remaining": max(int64(0), s.WeekLimit-s.WeekUsed), "reset_at": s.Start + (s.WeekEpoch+1)*604800}}
	if !s.FiveEnabled() {
		windows = windows[1:]
	}
	publicCards := []gin.H{}
	for _, card := range cards {
		publicCards = append(publicCards, gin.H{"id": card.ID, "kind": card.Kind, "state": card.State, "expires": card.Expires})
	}
	omniReply(c, gin.H{"billing_policy": model.OmniCustomerPublicPolicy(uid), "active_legacy": activeLegacy, "status": status, "windows": windows, "paused_legacy": legacy, "membership_enabled": p.BenefitsEnabled(), "topup_bonus_enabled": p.MembershipEnabled, "topup_promotion": p.TopupPromotion, "account": a, "subscription": s, "queued_terms": model.OmniCommerceQueueView(s), "model_scope": model.GetOmniPolicy().ModelScope, "included_models": s.ModelNames(), "level": level, "bonus_percent": percent, "cards": publicCards, "notices": notices, "receipts": receipts, "server_time": now, "quota_per_unit": common.QuotaPerUnit, "five_reset_at": s.Start + (s.FiveEpoch+1)*18000, "week_reset_at": s.Start + (s.WeekEpoch+1)*604800}, nil)
}
func OmniPreference(c *gin.Context) {
	var body struct {
		WalletOverflow *bool `json:"wallet_overflow"`
	}
	if c.ShouldBindJSON(&body) != nil || body.WalletOverflow == nil {
		omniReply(c, nil, errors.New("wallet_overflow is required"))
		return
	}
	uid := c.GetInt("id")
	enabled, err := model.OmniSetWalletOverflow(uid, *body.WalletOverflow)
	omniReply(c, gin.H{"wallet_overflow": enabled}, err)
}
func OmniRedeemCard(c *gin.Context) {
	var b struct {
		Code string `json:"code"`
	}
	if c.ShouldBindJSON(&b) != nil || len(b.Code) > 100 {
		omniReply(c, nil, errors.New("invalid reset code"))
		return
	}
	omniReply(c, true, model.OmniRedeem(c.GetInt("id"), b.Code))
}
func OmniClaimBenefit(c *gin.Context) {
	codes, err := model.OmniClaimCards(c.GetInt("id"), c.GetInt64("auth_version"))
	omniReply(c, gin.H{"codes": codes}, err)
}
func OmniReadNotice(c *gin.Context) {
	var b struct {
		Key string `json:"key"`
	}
	if c.ShouldBindJSON(&b) != nil {
		omniReply(c, nil, errors.New("invalid notice"))
		return
	}
	err := model.DB.Model(&model.OmniNotice{}).Where("key = ? AND user_id = ?", b.Key, c.GetInt("id")).Update("read", true).Error
	omniReply(c, true, err)
}
func OmniVerifiedCredit(c *gin.Context) {
	var b struct {
		PaidAt       int64  `json:"paid_at"`
		PaidAtSource string `json:"paid_at_source"`
		Key          string `json:"key"`
		UserID       int    `json:"user_id"`
		Cents        int64  `json:"cents"`
		Quota        int64  `json:"quota"`
	}
	if c.ShouldBindJSON(&b) != nil {
		omniReply(c, nil, errors.New("invalid receipt"))
		return
	}
	r, err := model.OmniCredit(b.Key, b.UserID, b.Cents, b.Quota, model.OmniPaymentEvidence{PaidAt: b.PaidAt, Source: b.PaidAtSource})
	omniReply(c, r, err)
}
func OmniRefundReceipt(c *gin.Context) {
	var b struct {
		Key string `json:"key"`
	}
	if c.ShouldBindJSON(&b) != nil {
		omniReply(c, nil, errors.New("invalid receipt"))
		return
	}
	omniReply(c, true, model.OmniRefundCredit(b.Key))
}
func OmniActivateSubscription(c *gin.Context) {
	if model.GetOmniPolicy().Payment.Mode == "wallet_balance" {
		omniReply(c, nil, errors.New("订阅只支持钱包余额购买"))
		return
	}
	var b struct {
		Key       string `json:"key"`
		UserID    int    `json:"user_id"`
		Plan      string `json:"plan"`
		Months    int    `json:"months"`
		PaidCents int64  `json:"paid_cents"`
		FxMicros  int64  `json:"fx_micros"`
		OrderID   string `json:"order_id"`
		FxSource  string `json:"fx_source"`
		FxAsOf    string `json:"fx_as_of"`
	}
	if c.ShouldBindJSON(&b) != nil {
		omniReply(c, nil, errors.New("invalid receipt"))
		return
	}
	omniReply(c, true, model.OmniActivate(b.Key, b.UserID, b.Plan, b.Months, b.PaidCents, b.FxMicros, b.OrderID, b.FxSource, b.FxAsOf))
}
func OmniCreateOrder(c *gin.Context) {
	var b struct {
		Plan   string `json:"plan"`
		Months int    `json:"months"`
	}
	if c.ShouldBindJSON(&b) != nil {
		omniReply(c, nil, errors.New("invalid order"))
		return
	}
	r, err := model.OmniCreateOrder(c.GetInt("id"), b.Plan, b.Months)
	omniReply(c, r, err)
}
func OmniPaidOrder(c *gin.Context) {
	if model.GetOmniPolicy().Payment.Mode == "wallet_balance" {
		omniReply(c, nil, errors.New("扫码支付仅用于充值钱包"))
		return
	}
	var b struct {
		ID        string `json:"id"`
		Reference string `json:"reference"`
		Method    string `json:"method"`
	}
	if c.ShouldBindJSON(&b) != nil || len(strings.TrimSpace(b.Reference)) < 1 || len(b.Reference) > 100 || (b.Method != "wechat" && b.Method != "alipay") {
		omniReply(c, nil, errors.New("请填写付款方式和交易单号"))
		return
	}
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var order model.OmniOrder
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&order, "id = ? AND user_id = ?", b.ID, c.GetInt("id")).Error; err != nil {
			return err
		}
		ref := strings.TrimSpace(b.Reference)
		if (order.State == "pending" || order.State == "fulfilled") && order.PaymentReference == ref && order.PaymentMethod == b.Method {
			return nil
		}
		if order.State != "awaiting_payment" {
			return errors.New("订单状态已变更，请查看订单记录")
		}
		order.State = "pending"
		order.PaymentReference = ref
		order.PaymentMethod = b.Method
		return tx.Save(&order).Error
	})
	omniReply(c, true, err)
}
func OmniOrders(c *gin.Context) {
	var orders []model.OmniOrder
	q := model.DB.Order("created_at DESC").Limit(100)
	if !strings.Contains(c.FullPath(), "/admin/") {
		q = q.Where("user_id = ?", c.GetInt("id"))
	}
	omniReply(c, &orders, q.Find(&orders).Error)
}
func OmniGrantReset(c *gin.Context) {
	var b struct {
		UserID int    `json:"user_id"`
		Kind   string `json:"kind"`
		Count  int    `json:"count"`
	}
	if c.ShouldBindJSON(&b) != nil {
		omniReply(c, nil, errors.New("invalid card batch"))
		return
	}
	codes, err := model.OmniGrantCards(b.UserID, b.Kind, b.Count)
	omniReply(c, gin.H{"codes": codes}, err)
}
func OmniRevokeReset(c *gin.Context) {
	var b struct {
		ID string `json:"id"`
	}
	if c.ShouldBindJSON(&b) != nil {
		omniReply(c, nil, errors.New("invalid card"))
		return
	}
	r := model.DB.Model(&model.OmniCard{}).Where("id = ? AND state = ?", b.ID, "unused").Update("state", "revoked")
	omniReply(c, r.RowsAffected, r.Error)
}

func OmniPurchaseWallet(c *gin.Context) {
	var b struct {
		Plan   string `json:"plan"`
		Months int    `json:"months"`
		Key    string `json:"key"`
	}
	if c.ShouldBindJSON(&b) != nil {
		omniReply(c, nil, errors.New("invalid purchase"))
		return
	}
	omniReply(c, true, model.OmniPurchaseWallet(c.GetInt("id"), b.Plan, b.Months, b.Key, c.GetInt64("auth_version")))
}
