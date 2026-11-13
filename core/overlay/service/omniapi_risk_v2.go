package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"io"
	"net/http"
 "os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

var riskV2BurstCheck = model.OmniRiskV2Burst
var riskV2ConsumerCheck = model.OmniRiskConsumptionSignalsV2
var riskV2Start sync.Once
var riskV2EventCursor uint64
var riskV2CursorLoaded bool
var riskV2LastReconcile int64
var riskV2LastSlow int64
var riskV2LastCache int64

func StartOmniRiskV2() {
	if !common.IsMasterNode && os.Getenv("OMNI_BLUE_GREEN_WORKER") == "1" {return}
	riskV2Start.Do(func() {
		if model.OmniRiskV2Ready() {
			_ = model.DB.Model(&model.OmniRiskExposure{}).Where("owner <> ? AND state IN ?", model.OmniRiskProcessID, []string{"sending", "reserved"}).Updates(map[string]any{"state": "unknown", "finished_at": time.Now().Unix()}).Error
		}
		_ = model.OmniSharedHeartbeat()
		_ = model.OmniSharedRecoverOrphans()
		model.RiskV2AfterControl = func(uid int) {
			r, e := model.OmniRiskReadControl(model.DB, uid)
			if e == nil && (r.State == "hold" || r.State == "confirmed") {
				model.OmniRiskCancel(uid)
			}
		}
		go func() {
			lastHealth := int64(0)
			lastFailed := false
			lastCommerceHeartbeat := int64(0)
			ticker := time.NewTicker(250 * time.Millisecond)
			defer ticker.Stop()
			for range ticker.C {
				if now := time.Now().Unix(); now-lastCommerceHeartbeat >= 10 {
					_ = model.OmniSharedHeartbeat()
					_ = model.OmniSharedRecoverOrphans()
					lastCommerceHeartbeat = now
				}
				if model.OmniRiskV2Ready() {
					e := riskV2Maintenance()
					failed := e != nil
					now := time.Now().Unix()
					if failed != lastFailed || now-lastHealth >= 60 {
						if !failed {
							if heartbeatErr := model.DB.Clauses(clause.OnConflict{UpdateAll: true}).Create(&model.OmniRiskCursorV2{Key: "worker", Value: now, UpdatedAt: now}).Error; heartbeatErr != nil {
								failed = true
							}
						}
						_ = model.OmniRiskOperational("risk_v2_incomplete", failed)
						_, se := model.LoadOmniRiskSecrets()
						_ = model.OmniRiskOperational("risk_source_configuration", se != nil)
						lastHealth = now
						lastFailed = failed
					}
				}
			}
		}()
		go func() {
			for {
				if model.OmniRiskV2Ready() {
					s, e := model.GetOmniRiskSettings()
					if e == nil && s.Notifications {
						secrets, e := model.LoadOmniRiskSecrets()
						if e == nil {
							ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
							OmniRiskDeliver(ctx, secrets, "", 0)
							cancel()
						}
					}
				}
				time.Sleep(time.Second)
			}
		}()
	})
}
func riskDeliveryClaim() (string, error) {
	owner := uuid.NewString()
	row := model.OmniRiskLease{Key: "delivery_v2"}
	if e := model.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; e != nil {
		return "", e
	}
	q := model.DB.Model(&model.OmniRiskLease{}).Where("key = ? AND expires <= ?", "delivery_v2", time.Now().Unix()).Updates(map[string]any{"owner": owner, "expires": time.Now().Unix() + 30})
	if q.Error != nil {
		return "", q.Error
	}
	if q.RowsAffected != 1 {
		return "", errors.New("risk_delivery_busy")
	}
	return owner, nil
}
func riskDeliveryRenew(owner string) bool {
	q := model.DB.Model(&model.OmniRiskLease{}).Where("key = ? AND owner = ? AND expires > ?", "delivery_v2", owner, time.Now().Unix()).Update("expires", time.Now().Unix()+30)
	return q.Error == nil && q.RowsAffected == 1
}
func riskV2Maintenance() error {
	// Idempotent bounded reconciliation. Operational calls, not model requests.
	var events []model.OmniRiskEventV2
	now := time.Now().Unix()
	if !riskV2CursorLoaded {
		var saved model.OmniRiskCursorV2
		if e := model.DB.Where("key = ?", "events").Find(&saved).Error; e != nil {
			return e
		}
		riskV2EventCursor = uint64(max(int64(0), saved.Value))
		riskV2CursorLoaded = true
	}
	if e := model.DB.Where("id > ? AND created_at >= ?", riskV2EventCursor, now-1800).Order("id").Limit(500).Find(&events).Error; e != nil {
		return e
	}
	consumers := map[int]bool{}
	bursts := map[string]int{}
	lastEvent := riskV2EventCursor
	for _, event := range events {
		if event.Network != "" && event.Browser != "" && (event.Kind == "registration.success" || event.Kind == "login.success" || event.Kind == "token.create" || event.Kind == "token.view") {
			bursts[event.Network+":"+event.Browser] = event.UserID
		}
		if event.Kind == "model.settled" || event.Kind == "model.admission" {
			consumers[event.UserID] = true
		}
		lastEvent = event.ID
	}

	for _, uid := range bursts {
		if e := riskV2BurstCheck(uid); e != nil {
			if saved := model.OmniRiskScheduleV2(uid, "burst"); saved != nil {
				return saved
			}
		}
	}
	for uid := range consumers {
		if e := riskV2ConsumerCheck(uid); e != nil {
			if saved := model.OmniRiskScheduleV2(uid, "consumption"); saved != nil {
				return saved
			}
		}
	}

	if lastEvent > riskV2EventCursor {
		if e := model.DB.Clauses(clause.OnConflict{UpdateAll: true}).Create(&model.OmniRiskCursorV2{Key: "events", Value: int64(lastEvent), UpdatedAt: now}).Error; e != nil {
			return e
		}
		riskV2EventCursor = lastEvent
	}
	var retryJobs []model.OmniRiskRetryV2
	if e := model.DB.Where("next_at <= ?", now).Order("next_at,key").Limit(25).Find(&retryJobs).Error; e != nil {
		return e
	}
	for _, job := range retryJobs {
		check := riskV2ConsumerCheck
		if job.Kind == "burst" {
			check = riskV2BurstCheck
		}
		if e := check(job.UserID); e != nil {
			delay := min(int64(300), int64(5)<<min(job.Attempts, 6))
			if e = model.DB.Model(&model.OmniRiskRetryV2{}).Where("key = ?", job.Key).Updates(map[string]any{"attempts": gorm.Expr("attempts + 1"), "next_at": now + delay, "error_code": "detector_failed", "updated_at": now}).Error; e != nil {
				return e
			}
		} else {
			if e = model.DB.Where("key = ? AND updated_at = ?", job.Key, job.UpdatedAt).Delete(&model.OmniRiskRetryV2{}).Error; e != nil {
				return e
			}
		}
	}
	if now == riskV2LastSlow {
		return nil
	}
	riskV2LastSlow = now
	refreshCaches := now-riskV2LastCache >= 60
	if refreshCaches {
		riskV2LastCache = now
	}

	if now-riskV2LastReconcile >= 60 {
		if e := riskV2ReconcileAudit(); e != nil {
			return e
		}
		riskV2LastReconcile = now
	}
	p, e := model.OmniRiskV2Policy()
	if e != nil {
		return e
	}
	if p.TemporaryIP && !p.Shadow {
		var groups []struct {
			Network string
			N       int64
			LastAt  int64
		}
		e = model.DB.Model(&model.OmniRiskControl{}).Select("network,COUNT(DISTINCT user_id) AS n,MAX(started_at) AS last_at").Where("state = ? AND reason = ? AND started_at >= ? AND network <> ''", "hold", "registration_sequence", now-600).Group("network").Having("COUNT(DISTINCT user_id) >= 3").Find(&groups).Error
		// GORM's numeric suffix table is explicitly fixed below in migrations.
		if e != nil {
			return e
		}
		for _, g := range groups {
			row := model.OmniRiskIP{Tag: g.Network, Source: "automatic", Version: 1, CreatedAt: now, ExpiresAt: now + 900}
			var old model.OmniRiskIP
			if e = model.DB.Where("tag = ?", g.Network).Find(&old).Error; e != nil {
				return e
			}
			if old.Tag != "" {
				if (old.Source == "manual" && old.RevokedAt == 0 && (old.ExpiresAt == 0 || old.ExpiresAt > now)) || (old.RevokedAt == 0 && old.ExpiresAt > now) || g.LastAt <= max(old.ExpiresAt, old.RevokedAt) {
					continue
				}
				e = model.DB.Model(&model.OmniRiskIP{}).Where("tag = ? AND expires_at = ? AND revoked_at = ?", old.Tag, old.ExpiresAt, old.RevokedAt).Updates(map[string]any{"source": "automatic", "case_id": "", "created_at": now, "expires_at": now + 900, "revoked_at": 0, "version": gorm.Expr("version + 1")}).Error
			} else {
				e = model.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
			}
			if e != nil {
				return e
			}
		}
	}
	var controls []model.OmniRiskControl
	if e = model.DB.Where("state IN ?", []string{"hold", "confirmed", "budget", "guard"}).Limit(1000).Find(&controls).Error; e != nil {
		return e
	}
	for _, r := range controls {
		if r.State == "hold" || r.State == "confirmed" {
			model.OmniRiskCancel(r.UserID)
			if refreshCaches {
				_ = model.PublishUserAuthCache(r.UserID)
				_ = model.InvalidateUserTokensCache(r.UserID)
			}
		}
		if r.ReviewedAt == 0 && now >= r.StartedAt+1800 {
			slot := (now - r.StartedAt - 1800) / 7200
			_ = model.OmniRiskEnqueue(model.DB, "v2-reminder:"+r.CaseID+":"+strconv.FormatInt(slot, 10), r.CaseID, "reminder", "OmniAPI 风控待复核\n账号 #"+strconv.Itoa(r.UserID)+"\nhttps://omniapi.biz/risk-control?case="+r.CaseID)
		}
	}
	// A dead sender never proves zero cost. Sending rows become unknown only
	// after request completion; on restart old rows are conservatively retained.
	return nil
}
func riskV2ReconcileAudit() error {
	var retryCount int64
	if e := model.DB.Model(&model.OmniRiskRetryV2{}).Count(&retryCount).Error; e != nil {
		return e
	}
	if e := model.OmniRiskOperational("detector_retry_pending", retryCount > 0); e != nil {
		return e
	}
	if e := riskV2RegistrationBackfill(); e != nil {
		return e
	}
	if e := riskV2LegacyNotices(); e != nil {
		return e
	}
	var cursor model.OmniRiskCursorV2
	if e := model.DB.Where("key = ?", "audit").Find(&cursor).Error; e != nil {
		return e
	}
	var rows []model.AuditLog
	if e := model.DB.Select("id", "user_id", "action", "success", "created_at", "ip", "user_agent", "event_id").Where("id > ? AND created_at >= ?", cursor.Value, time.Now().Unix()-30*86400).Order("id").Limit(500).Find(&rows).Error; e != nil {
		return e
	}
	secrets, e := model.LoadOmniRiskSecrets()
	if e != nil {
		return e
	}
	for _, a := range rows {
		kind := ""
		if a.Success {
			switch a.Action {
			case "login":
				kind = "login.success"
			case "token.create":
				kind = "token.create"
			case "token.key_view", "token.key_view_batch":
				kind = "token.view"
			}
		}
		if kind != "" {
			r := model.OmniRiskEventV2{Key: kind + ":" + a.EventId, UserID: a.UserId, Kind: kind, CreatedAt: a.CreatedAt, Network: model.OmniRiskNetworkTag(a.Ip), Browser: model.OmniRiskTag(secrets.HMACKey, "browser", a.UserAgent)}
			if e = model.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&r).Error; e != nil {
				return e
			}
		}
		cursor.Value = int64(a.Id)
	}
	if len(rows) > 0 {
		cursor.Key = "audit"
		cursor.UpdatedAt = time.Now().Unix()
		if e = model.DB.Clauses(clause.OnConflict{UpdateAll: true}).Create(&cursor).Error; e != nil {
			return e
		}
	}
	var released []model.OmniRiskRewardHold
	if e = model.DB.Where("state = ?", "released").Limit(100).Find(&released).Error; e != nil {
		return e
	}
	rewardFailed := false
	for _, h := range released {
		if h.FundingMode == "legacy" {
			e = model.OmniRiskLegacyReferral(h.Inviter, h.Invitee)
		} else {
			e = model.OmniRewardReferral(h.Inviter, h.Invitee)
		}
		if e != nil {
			rewardFailed = true
			continue
		}
	}
	if e = model.OmniRiskOperational("reward_compensation_pending", rewardFailed); e != nil {
		return e
	}

	// Existing invitation/AI cases can request reversible protection, never an
	// AI-directed account suspension. Closed false-positive episodes are excluded.
	p, e := model.OmniRiskV2Policy()
	if e != nil {
		return e
	}
	if p.Shadow || !p.AutoHold {
		return nil
	}
	var cases []model.OmniRiskCase
	if e = model.DB.Where("status <> ? AND severity IN ? AND id NOT LIKE ? AND id NOT LIKE ?", "closed", []string{"medium", "high"}, "RV2-%", "RB-%").Where("user_id IN (?)", model.DB.Model(&model.User{}).Select("id").Where("status = ?", common.UserStatusEnabled)).Order("updated_at DESC").Limit(100).Find(&cases).Error; e != nil {
		return e
	}
	for _, c := range cases {
		fresh, e := riskV2LegacyCaseHasFreshEvidence(c)
		if e != nil {
			return e
		}
		if !fresh {
			continue
		}
		if e = model.OmniRiskApplyV2(c.UserID, "case:"+c.ID+":"+c.Fingerprint, "observe:"+c.ID, true, 0, ""); e != nil {
			return e
		}
	}
	return nil
}
func OmniRiskRequestGuard(c *gin.Context, info *relaycommon.RelayInfo) error {
	if info == nil {
		return nil
	}
	if err := model.OmniEmailConsumptionAllowed(model.DB, info.UserId); err != nil {
		return err
	}
	// The controller checks admission before channel selection. Only validate
	// channel scope and mark dispatch once the outbound guard has ChannelMeta.
	if info.HasChannelMeta() {
		verifiedPath := ""
		if binding, path, ok := omniRequestBinding(info, info.ChannelId); ok && binding.VerifiedPathsOnly {
			if rejection := OmniDeclaredRequestReject(info, info.ChannelId); rejection != nil {
				return rejection
			}
			verifiedPath = path
		}
		if err := model.OmniCommerceCheckChannelPath(info.UserId, info.OmniPoolID, info.RequestId, info.GetBillingModelName(), info.ChannelId, verifiedPath, info.OmniRoutingSnapshot); err != nil {
			return err
		}
		if info.OmniPoolID != "" {
			if record, err := model.OmniRequestRecord(info.RequestId); err != nil {
				return err
			} else if record.CommerceProfile != "" {
				var p model.OmniCommerceProfile
				if err = json.Unmarshal([]byte(record.CommerceProfile), &p); err != nil {
					return err
				}
				if p.CapabilityMatrix != "" {
					// Re-admission must retain the amount frozen when funding was
					// reserved. Recomputing certificate bounds must not replace a
					// dynamic request's smaller, fully funded wire budget.
					reservedUpper := info.OmniSharedUpperQuota
					if err = omniValidateCertifiedShared(info, p); err != nil {
						return err
					}
					if record.BillingPolicy != model.OmniCustomerBillingV1 && info.OmniSharedDynamic {
						if reservedUpper <= 0 || reservedUpper > info.OmniSharedUpperQuota || reservedUpper > record.Reserved {
							return errors.New("共享动态请求预留与原资金记录不一致")
						}
						info.OmniSharedUpperQuota = reservedUpper
					}
					if record.BillingPolicy != model.OmniCustomerBillingV1 && record.Source == "omni_shared" && record.Reserved < info.OmniSharedUpperQuota {
						return errors.New("共享请求上界超过原预留，不会扩扣")
					}
				}
			}
			if err := model.OmniSharedMarkDispatch(info.RequestId); err != nil {
				return err
			}
		}
	}
	if !model.OmniRiskV2Ready() || info == nil || info.UserId <= 0 {
		return nil
	}
	if e := model.OmniRiskAdmission(model.DB, info.UserId); e != nil {
		return e
	}
	paid, e := model.OmniRiskPaid(model.DB, info.UserId)
	if e != nil {
		return e
	}
	return model.OmniRiskIPCheck(model.DB, model.OmniRiskNetworkTag(c.ClientIP()), paid)
}
func OmniRiskAPIError(err error) *types.NewAPIError {
	var apiError *types.NewAPIError
	if errors.As(err, &apiError) {
		return apiError
	}
	var trial *model.OmniTrialRejection
	if errors.As(err, &trial) {
		status := http.StatusTooManyRequests
		var message string
		switch trial.Code {
		case "trial_quote_unavailable":
			status = http.StatusUnprocessableEntity
			message = "此请求暂无法核算试用成本，请尝试控制台推荐的文本模型，或关闭当前工具及媒体功能。"
		case "trial_request_budget":
			status = http.StatusUnprocessableEntity
			message = "该请求预计超过单次试用预算，请缩短上下文或降低输出上限后重试。"
		case "trial_account_budget":
			message = fmt.Sprintf("本次试用剩余 ¥%.6f，试用结束时间为北京时间 %s。请缩短请求或等待在途请求完成。", float64(trial.RemainingMicros)/1000000, time.Unix(trial.EndsAt, 0).In(time.FixedZone("UTC+8", 28800)).Format("01-02 15:04"))
		case "trial_concurrency_limit":
			message = "当前试用请求并发已满，请等待一个在途请求结束后重试。"
		}
		return types.NewError(fmt.Errorf("%w: %s", trial, message), types.ErrorCode(trial.Code), types.ErrOptionWithStatusCode(status), types.ErrOptionWithSkipRetry())
	}
	code, public := http.StatusForbidden, "risk_review_required"
	if errors.Is(err, model.ErrOmniRiskLimit) || strings.Contains(err.Error(), "risk_protection_limit") {
		code, public = http.StatusTooManyRequests, "risk_protection_limit"
	} else if errors.Is(err, model.ErrOmniRiskQuote) || strings.Contains(err.Error(), "risk_quote_unavailable") {
		public = "risk_quote_unavailable"
	} else if !errors.Is(err, model.ErrOmniRiskHold) && !strings.Contains(err.Error(), "risk_review_required") {
		code, public = http.StatusServiceUnavailable, "risk_check_unavailable"
	}
	return types.NewError(errors.New(public+": 安全保护中，请登录 /brand/risk-review.html 查看复核指引"), types.ErrorCode(public), types.ErrOptionWithStatusCode(code), types.ErrOptionWithSkipRetry())
}
func OmniIsRiskError(err error) bool {
	if err == nil {
		return false
	}
	var trial *model.OmniTrialRejection
	if errors.As(err, &trial) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "risk_check_unavailable") || strings.Contains(s, "risk_review_required") || strings.Contains(s, "risk_protection_limit") || strings.Contains(s, "risk_quote_unavailable")
}

func OmniTrialBackupError(err error) bool {
	var trial *model.OmniTrialRejection
	return errors.As(err, &trial) && trial.TryBackup
}

func OmniRiskUnquotableWebSocket(uid int) *types.NewAPIError {
	if model.OmniTrialLimitsFor(uid).Enabled {
		return OmniRiskAPIError(&model.OmniTrialRejection{Code: "trial_quote_unavailable"})
	}
	return OmniRiskAPIError(model.ErrOmniRiskQuote)
}

type OmniRiskCostQuote struct {
	Input  string `json:"input"`
	Output string `json:"output"`
	FX     string `json:"fx"`
	Fixed  int64  `json:"fixed"`
}

func riskDecimal(raw json.RawMessage) (decimal.Decimal, error) {
	s := strings.Trim(string(raw), "\"")
	d, e := decimal.NewFromString(s)
	if e != nil || d.IsNegative() {
		return decimal.Zero, model.ErrOmniRiskQuote
	}
	return d, nil
}
func riskCostUnits(raw json.RawMessage) (decimal.Decimal, decimal.Decimal, error) {
	var row map[string]json.RawMessage
	if json.Unmarshal(raw, &row) != nil {
		return decimal.Zero, decimal.Zero, model.ErrOmniRiskQuote
	}
	var verified bool
	_ = json.Unmarshal(row["complete_prices_verified"], &verified)
	if !verified {
		return decimal.Zero, decimal.Zero, model.ErrOmniRiskQuote
	}
	in, out := decimal.Zero, decimal.Zero
	for _, key := range []string{"input_usd_per_million", "cache_read_usd_per_million", "cache_write_usd_per_million", "cache_write_1h_usd_per_million"} {
		v, e := riskDecimal(row[key])
		if e != nil {
			return in, out, e
		}
		if v.GreaterThan(in) {
			in = v
		}
	}
	var e error
	out, e = riskDecimal(row["output_usd_per_million"])
	if e != nil || !in.IsPositive() || !out.IsPositive() {
		return in, out, model.ErrOmniRiskQuote
	}
	var tiers []struct {
		Prices json.RawMessage `json:"prices"`
	}
	if len(row["tiers"]) > 0 && json.Unmarshal(row["tiers"], &tiers) != nil {
		return in, out, model.ErrOmniRiskQuote
	}
	for _, t := range tiers {
		var nested map[string]json.RawMessage
		if json.Unmarshal(t.Prices, &nested) != nil {
			return in, out, model.ErrOmniRiskQuote
		}
		nested["complete_prices_verified"] = json.RawMessage("true")
		data, _ := json.Marshal(nested)
		a, b, e := riskCostUnits(data)
		if e != nil {
			return in, out, e
		}
		in = decimal.Max(in, a)
		out = decimal.Max(out, b)
	}
	return in, out, nil
}

// Inputs are inspected in memory only. No request body is persisted in risk.
var errOmniSupplierQuote = fmt.Errorf("%w: supplier pricing incomplete", model.ErrOmniRiskQuote)

func OmniRiskUpperBound(info *relaycommon.RelayInfo, body []byte, fx string) (int64, string, error) {
	var pricing struct {
		Costs map[string]json.RawMessage `json:"costs"`
	}
	if json.Unmarshal(info.OmniPriceQuote, &pricing) != nil {
		return 0, "", model.ErrOmniRiskQuote
	}
	channelKey := strconv.Itoa(info.GetChannelID())
	raw := pricing.Costs[channelKey]
	fixedImageBinding := false
	// Existing entitlement financial snapshots remain immutable. The risk
	// exposure uses the current verified procurement price for this exact
	// image channel, including newly accepted edit protocol evidence.
	if _, ok := info.Request.(*dto.ImageRequest); ok {
		registry := model.OmniRegistryAt(info.OmniRegistryVersion)
		name := registry.Canonical(info.OriginModelName)
		for _, binding := range registry.Models[name].Bindings {
			if binding.ChannelID == info.GetChannelID() && binding.Status == "verified" &&
				slices.Contains(binding.Supports, "image_fixed_risk_verified") {
				fixedImageBinding = true
				break
			}
		}
		var current struct {
			Costs map[string]json.RawMessage `json:"costs"`
		}
		if fixedImageBinding && json.Unmarshal(model.GetOmniPrices().Models[name], &current) == nil {
			candidate := current.Costs[channelKey]
			var bound struct {
				Unit json.RawMessage `json:"per_image_usd"`
			}
			if json.Unmarshal(candidate, &bound) == nil && len(bound.Unit) > 0 {
				raw = candidate
			} else {
				fixedImageBinding = false
			}
		} else {
			fixedImageBinding = false
		}
	}
	if len(raw) == 0 {
		return 0, "", errOmniSupplierQuote
	}
	rate, e := decimal.NewFromString(fx)
	if e != nil || rate.LessThan(decimal.NewFromInt(1)) || rate.GreaterThan(decimal.NewFromInt(20)) {
		return 0, "", model.ErrOmniRiskQuote
	}
	var price map[string]json.RawMessage
	if json.Unmarshal(raw, &price) != nil {
		return 0, "", model.ErrOmniRiskQuote
	}
	var verified bool
	_ = json.Unmarshal(price["complete_prices_verified"], &verified)
	if !verified && !fixedImageBinding {
		return 0, "", errOmniSupplierQuote
	}
	quote := OmniRiskCostQuote{FX: rate.String()}
	if len(price["per_image_usd"]) > 0 {
		var fixedImageUpper bool
		_ = json.Unmarshal(price["risk_fixed_image_upper_verified"], &fixedImageUpper)
		if !fixedImageUpper && !fixedImageBinding {
			return 0, "", model.ErrOmniRiskQuote
		}
		unit, e := riskDecimal(price["per_image_usd"])
		if e != nil || !unit.IsPositive() {
			return 0, "", model.ErrOmniRiskQuote
		}
		finalN, e := omniRiskFinalImageCount(body)
		if e != nil {
			return 0, "", e
		}
		n := max(info.RequestedImageCount(), finalN)
		if n < 1 || n > 128 {
			return 0, "", model.ErrOmniRiskQuote
		}
		bound := unit.Mul(decimal.NewFromInt(int64(n))).Mul(rate).Mul(decimal.NewFromInt(1000000)).Ceil()
		if !bound.IsInteger() || bound.GreaterThan(decimal.NewFromInt(1000000000000)) {
			return 0, "", model.ErrOmniRiskQuote
		}
		quote.Fixed = bound.IntPart()
		data, _ := json.Marshal(quote)
		return quote.Fixed, string(data), nil
	}
	if !OmniSafeTextRequest(info) || len(body) == 0 || len(body) > 16*1024*1024 {
		return 0, "", model.ErrOmniRiskQuote
	}
	var final map[string]any
	if json.Unmarshal(body, &final) != nil || riskUnsupported(final) {
		return 0, "", model.ErrOmniRiskQuote
	}
	if m, ok := final["model"].(string); ok {
		expected := info.GetUpstreamModelName()
		if expected == "" {
			expected = info.OriginModelName
		}
		if expected != "" && m != expected {
			return 0, "", model.ErrOmniRiskQuote
		}
	}
	output := int64(OmniReservedOutput(info))
	finalOutput := int64(0)
	for _, k := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
		if n, ok := final[k].(float64); ok {
			if n < 1 || n > 1<<30 || n != float64(int64(n)) {
				return 0, "", model.ErrOmniRiskQuote
			}
			finalOutput = max(finalOutput, int64(n))
		}
	}
	if g, ok := final["generationConfig"].(map[string]any); ok {
		if n, ok := g["maxOutputTokens"].(float64); ok {
			if n < 1 || n > 1<<30 || n != float64(int64(n)) {
				return 0, "", model.ErrOmniRiskQuote
			}
			finalOutput = max(finalOutput, int64(n))
		}
	}
	candidates := int64(1)
	if n, ok := final["n"].(float64); ok {
		if n < 1 || n > 128 || n != float64(int64(n)) {
			return 0, "", model.ErrOmniRiskQuote
		}
		candidates = int64(n)
	}
	if g, ok := final["generationConfig"].(map[string]any); ok {
		if n, exists := g["candidateCount"]; exists {
			v, ok := n.(float64)
			if !ok || v < 1 || v > 128 || v != float64(int64(v)) {
				return 0, "", model.ErrOmniRiskQuote
			}
			candidates = max(candidates, int64(v))
		}
	}
	// The final provider payload must retain an explicit cap. The original DTO
	// cannot bound a channel override that removed it.
	if candidates < 1 || candidates > 128 || finalOutput <= 0 || output > 1<<30 {
		return 0, "", model.ErrOmniRiskQuote
	}
	output = max(output, finalOutput*candidates)
	in, out, e := riskCostUnits(raw)
	if e != nil {
		return 0, "", e
	}
	quote.Input = in.String()
	quote.Output = out.String()
	tokens := int64(max(len(body)+1024, info.GetEstimatePromptTokens()))
	upper := in.Mul(decimal.NewFromInt(tokens)).Add(out.Mul(decimal.NewFromInt(output))).Mul(rate).Ceil()
	if upper.GreaterThan(decimal.NewFromInt(1000000000000)) {
		return 0, "", model.ErrOmniRiskQuote
	}
	data, _ := json.Marshal(quote)
	return upper.IntPart(), string(data), nil
}
func riskUnsupported(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		for k, x := range v {
			switch k {
			case "previous_response_id", "conversation", "container", "audio", "cachedContent", "mcp_servers", "context_management", "inlineData", "fileData", "inline_data", "file_data", "image_url", "input_audio", "speechConfig", "imageConfig", "service_tier", "speed", "googleSearch", "googleSearchRetrieval", "codeExecution", "urlContext":
				if x != nil {
					return true
				}
			}
			if k == "modalities" || k == "responseModalities" {
				a, ok := x.([]any)
				if !ok {
					return true
				}
				for _, m := range a {
					s, ok := m.(string)
					if !ok || !strings.EqualFold(s, "text") {
						return true
					}
				}
			}
			if k == "tools" {
				a, ok := x.([]any)
				if !ok {
					return true
				}
				for _, entry := range a {
					t, ok := entry.(map[string]any)
					if !ok {
						return true
					}
					if t["type"] == "function" {
						continue
					}
					if len(t) == 1 && t["functionDeclarations"] != nil {
						continue
					}
					if t["type"] == nil && t["name"] != nil && t["input_schema"] != nil {
						continue
					}
					return true
				}
			}
			if k == "type" {
				if s, ok := x.(string); ok && (strings.Contains(s, "image") || strings.Contains(s, "audio") || strings.Contains(s, "video") || strings.Contains(s, "file") || strings.Contains(s, "web_search") || strings.Contains(s, "computer")) {
					return true
				}
			}
			if riskUnsupported(x) {
				return true
			}
		}
	case []any:
		for _, x := range v {
			if riskUnsupported(x) {
				return true
			}
		}
	}
	return false
}
func riskFrozenFX(c *gin.Context) string {
	if s := c.GetString("risk_v2_fx"); s != "" {
		return s
	}
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap["OmniAPIPricing"]
	common.OptionMapRWMutex.RUnlock()
	var p struct {
		FX json.RawMessage `json:"cny_per_usd"`
	}
	_ = json.Unmarshal([]byte(raw), &p)
	s := strings.Trim(string(p.FX), "\"")
	c.Set("risk_v2_fx", s)
	return s
}
func OmniRiskNeedsBudget(uid int) (bool, error) {
	if !model.OmniRiskV2Ready() {
		return false, nil
	}
	p, e := model.OmniRiskV2Policy()
	if e != nil {
		return false, e
	}
	r, e := model.OmniRiskReadControl(model.DB, uid)
	if e != nil {
		return false, e
	}
	if r.State == "hold" || r.State == "budget" || r.State == "confirmed" {
		return false, model.ErrOmniRiskHold
	}
	if r.State == "guard" {
		return true, nil
	}
	if p.Shadow || !p.NewUserProtection {
		return false, nil
	}
	var u model.User
	if e = model.DB.Select("id", "role", "created_at").First(&u, uid).Error; e != nil {
		return false, e
	}
	requestScoped := model.OmniTrialLimitsFor(uid).Enabled
	if !model.OmniNewUserProtected(p, u, r, false, requestScoped) {
		return false, nil
	}
	paid, e := model.OmniRiskPaid(model.DB, uid)
	return !paid, e
}
func OmniRiskBeforeDispatch(c *gin.Context, info *relaycommon.RelayInfo, req *http.Request) (func(), error) {
	if info != nil && info.OmniHostedToolUsage != nil {
		info.OmniNativeToolAttempt = info.OmniHostedToolUsage.Begin()
	}
	if e := OmniRiskRequestGuard(c, info); e != nil {
		return nil, OmniRiskAPIError(e)
	}
	if info == nil {
		return func() {}, nil
	}
	if info.OmniPoolID != "" || len(info.OmniNativeToolQuote) > 0 || OmniHasDeclaredRequestLimits(info, info.GetChannelID()) {
		if req == nil || req.GetBody == nil {
			return nil, errors.New("无法核验最终发送请求")
		}
		reader, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(io.LimitReader(reader, 128*1024*1024+1))
		_ = reader.Close()
		if err != nil || len(body) > 128*1024*1024 {
			return nil, errors.New("最终发送请求超过代理限制")
		}
		if err = OmniValidateDispatchBody(info, body); err != nil {
			return nil, err
		}
	}
	needed, e := OmniRiskNeedsBudget(info.UserId)
	if e != nil {
		return nil, OmniRiskAPIError(e)
	}
	if e := model.OmniRiskRecordV2(info.UserId, "model.admission", info.RequestId, c.ClientIP(), c.Request.UserAgent(), 0, info.TokenId); e != nil {
		if needed {
			return nil, OmniRiskAPIError(e)
		}
		common.SysError("risk admission metadata unavailable; no active account budget")
	}
	if !needed {
		if e := OmniRiskRequestGuard(c, info); e != nil {
			return nil, OmniRiskAPIError(e)
		}
		return func() {}, nil
	}
	if req == nil || req.GetBody == nil {
		return nil, riskV2QuoteRejected(info.UserId, false)
	}
	r, e := req.GetBody()
	if e != nil {
		return nil, riskV2QuoteRejected(info.UserId, false)
	}
	body, e := io.ReadAll(io.LimitReader(r, 32*1024*1024+1))
	_ = r.Close()
	if e != nil {
		return nil, e
	}
	if len(body) > 32*1024*1024 {
		return nil, riskV2QuoteRejected(info.UserId, false)
	}
	if len(info.OmniPriceQuote) == 0 {
		p := model.GetOmniPrices()
		info.OmniPriceQuote = p.Models[model.OmniCanonicalModel(info.OriginModelName)]
		if c.GetString("risk_v2_fx") == "" {
			c.Set("risk_v2_fx", strings.Trim(string(p.RiskFX), "\""))
		}
	}
	bound, quote, e := OmniRiskUpperBound(info, body, riskFrozenFX(c))
	if e != nil {
		return nil, riskV2QuoteRejected(info.UserId, errors.Is(e, errOmniSupplierQuote))
	}
	exposure, e := model.OmniRiskReserveV2(info.UserId, info.RequestId, info.GetChannelID(), bound, quote)
	if e != nil {
		return nil, OmniRiskAPIError(e)
	}
	if exposure != nil {
		if ids, ok := c.Get("risk_v2_exposures"); ok {
			c.Set("risk_v2_exposures", append(ids.([]string), exposure.ID))
		} else {
			c.Set("risk_v2_exposures", []string{exposure.ID})
		}
	}
	if e = OmniRiskRequestGuard(c, info); e != nil {
		if exposure != nil {
			_ = model.OmniRiskFinishV2(exposure.ID, 0, false, true)
		}
		return nil, OmniRiskAPIError(e)
	}
	if err := req.Context().Err(); err != nil {
		if exposure != nil {
			_ = model.OmniRiskFinishV2(exposure.ID, 0, false, true)
		}
		return nil, err
	}
	return func() {}, nil
}
func OmniRiskTrackRequest(c *gin.Context, uid int) func() {
	ctx, cancel := context.WithCancelCause(c.Request.Context())
	c.Request = c.Request.WithContext(ctx)
	done := model.OmniRiskTrack(uid, c.GetString(common.RequestIdKey)+":"+uuid.NewString(), func() { cancel(model.ErrOmniRiskHold) })
	return func() {
		done()
		_ = model.OmniRiskEndRequestV2(uid, c.GetString(common.RequestIdKey))
		cancel(context.Canceled)
		if value, ok := c.Get("risk_v2_closers"); ok {
			for _, done := range value.([]func()) {
				done()
			}
		}
		if value, ok := c.Get("risk_v2_exposures"); ok {
			for _, id := range value.([]string) {
				_ = model.OmniRiskFinishV2(id, 0, false, false)
			}
		}
	}
}
func OmniRiskTrackCloser(c *gin.Context, uid int, closeFn func()) {
	done := model.OmniRiskTrack(uid, "connection:"+uuid.NewString(), closeFn)
	if v, ok := c.Get("risk_v2_closers"); ok {
		c.Set("risk_v2_closers", append(v.([]func()), done))
	} else {
		c.Set("risk_v2_closers", []func(){done})
	}
}
func OmniRiskBeforeBytes(c *gin.Context, info *relaycommon.RelayInfo, body []byte) (func(), error) {
	req, e := http.NewRequestWithContext(c.Request.Context(), "POST", "http://risk.invalid", bytes.NewReader(body))
	if e != nil {
		return nil, e
	}
	return OmniRiskBeforeDispatch(c, info, req)
}

// RiskNotice credentials have an independent opaque purpose. They are never
// access JWTs, normal sessions, refresh credentials, or API keys.
const OmniRiskNoticePurpose = "risk_notice_v2"
const OmniRiskLoginPurpose = "risk_login_v2"

func IssueOmniRiskNotice(uid int, version int64) (*AuthBundle, error) {
	r, e := model.OmniRiskReadControl(model.DB, uid)
	if e != nil {
		return nil, e
	}
	payload := strconv.FormatInt(version, 10) + ":" + strconv.FormatInt(r.Version, 10) + ":" + r.CaseID
	token, _, e := model.CreateAuthFlow(model.AuthFlowCreate{Purpose: OmniRiskNoticePurpose, UserId: uid, Payload: payload, ExpiresAt: time.Now().Add(15 * time.Minute)})
	if e != nil {
		return nil, e
	}
	return &AuthBundle{Restricted: true, RiskNoticeToken: token, RiskUserID: uid}, nil
}
func ValidateOmniRiskNotice(token string) (int, error) {
	flow, e := model.GetAuthFlow(token, model.AuthFlowMatch{Purpose: OmniRiskNoticePurpose})
	if e != nil {
		return 0, e
	}
	var u model.User
	if e = model.DB.Select("id", "auth_version").First(&u, flow.UserId).Error; e != nil {
		return 0, e
	}
	r, e := model.OmniRiskReadControl(model.DB, u.Id)
	if e != nil {
		return 0, e
	}
	if flow.Payload != strconv.FormatInt(u.AuthVersion, 10)+":"+strconv.FormatInt(r.Version, 10)+":"+r.CaseID {
		return 0, ErrAuthTokenInvalid
	}
	return u.Id, nil
}

// Snapshot-only funding import never calls a credit or changes account money.
func ImportOmniRiskFundingSnapshot(rows map[int]string, sourceHash string) error {
	return model.DB.Transaction(func(tx *gorm.DB) error {
		for uid, classification := range rows {
			if uid <= 0 || (classification != "paid" && classification != "funding_unknown") {
				return errors.New("invalid risk funding evidence")
			}
			if e := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&model.OmniRiskFundingEvidence{UserID: uid, SourceHash: sourceHash, Classification: classification, CreatedAt: time.Now().Unix()}).Error; e != nil {
				return e
			}
		}
		return nil
	})
}

func riskV2QuoteRejected(uid int, supplierQuote bool) *types.NewAPIError {
	if limits := model.OmniTrialLimitsFor(uid); limits.Enabled {
		control, err := model.OmniRiskReadControl(model.DB, uid)
		if err != nil {
			return OmniRiskAPIError(err)
		}
		if control.State != "guard" {
			return OmniRiskAPIError(&model.OmniTrialRejection{Code: "trial_quote_unavailable", TryBackup: supplierQuote && limits.Fallback})
		}
	}
	paid, e := model.OmniRiskPaid(model.DB, uid)
	if e != nil {
		return OmniRiskAPIError(e)
	}
	if !paid {
		if e = model.OmniRiskBudgetPause(uid, "quote_unavailable"); e != nil {
			return OmniRiskAPIError(e)
		}
	}
	return OmniRiskAPIError(model.ErrOmniRiskQuote)
}
