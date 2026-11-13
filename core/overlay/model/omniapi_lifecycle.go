package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

type OmniLegacyPause struct {
	ID               uint   `json:"-" gorm:"primaryKey"`
	UserID           int    `json:"-" gorm:"index"`
	SubscriptionID   int    `json:"-" gorm:"uniqueIndex"`
	PlanID           int    `json:"-"`
	Remaining        int64  `json:"remaining_quota"`
	RemainingSeconds int64  `json:"remaining_seconds"`
	PausedAt         int64  `json:"paused_at"`
	ResumedAt        int64  `json:"resumed_at"`
	State            string `json:"state"`
}
type OmniLegacyFlight struct {
	RequestID string `gorm:"primaryKey;size:128"`
	UserID    int    `gorm:"index"`
	State     string
	CreatedAt int64
}

func omniBusy(tx *gorm.DB, uid int) (bool, error) {
	var n, m int64
	if err := tx.Model(&OmniReservation{}).Where("user_id = ? AND state = ?", uid, "pending").Count(&n).Error; err != nil {
		return false, err
	}
	if err := tx.Model(&OmniLegacyFlight{}).Where("user_id = ? AND state = ?", uid, "pending").Count(&m).Error; err != nil {
		return false, err
	}
	return n+m > 0, nil
}
func omniPauseLegacy(tx *gorm.DB, uid int, now int64) error {
	var rows []UserSubscription
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ? AND status = ? AND end_time > ?", uid, "active", now).Find(&rows).Error; err != nil {
		return err
	}
	for _, s := range rows {
		var plan SubscriptionPlan
		if tx.First(&plan, s.PlanId).Error == nil {
			if err := maybeResetUserSubscriptionWithPlanTx(tx, &s, &plan, now); err != nil {
				return err
			}
		}
		pause := OmniLegacyPause{UserID: uid, SubscriptionID: s.Id, PlanID: s.PlanId, Remaining: max(0, s.AmountTotal-s.AmountUsed), RemainingSeconds: s.EndTime - now, PausedAt: now, State: "paused"}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "subscription_id"}}, DoUpdates: clause.AssignmentColumns([]string{"remaining", "remaining_seconds", "paused_at", "resumed_at", "state"})}).Create(&pause).Error; err != nil {
			return err
		}
		if err := tx.Model(&s).Update("status", "paused").Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&OmniLedger{Key: fmt.Sprintf("legacy-pause:%d:%d", s.Id, now), UserID: uid, Kind: "legacy_paused", CreatedAt: now}).Error; err != nil {
			return err
		}
	}
	return nil
}
func omniResumeLegacy(tx *gorm.DB, uid int, now int64) error {
	var rows []OmniLegacyPause
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ? AND state = ?", uid, "paused").Find(&rows).Error; err != nil {
		return err
	}
	for _, p := range rows {
		var s UserSubscription
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&s, p.SubscriptionID).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			p.State = "cancelled"
		} else if err != nil {
			return err
		} else if s.Status != "paused" {
			p.State = "cancelled"
		} else {
			delta := now - p.PausedAt
			s.StartTime += delta
			s.EndTime += delta
			if s.LastResetTime > 0 {
				s.LastResetTime += delta
			}
			if s.NextResetTime > 0 {
				s.NextResetTime += delta
			}
			s.Status = "active"
			if err := tx.Save(&s).Error; err != nil {
				return err
			}
			p.State = "resumed"
		}
		p.ResumedAt = now
		if err := tx.Save(&p).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&OmniLedger{Key: fmt.Sprintf("legacy-resume:%d:%d", p.SubscriptionID, p.PausedAt), UserID: uid, Kind: "legacy_" + p.State, CreatedAt: now}).Error; err != nil {
			return err
		}
	}
	return nil
}

// Increase limits under the same account/subscription lock as requests. Never
// reset used quota, anchors, expiry, source binding, or wallet during upgrades.
func omniUpgradeQuotaTx(tx *gorm.DB, s *OmniSubscription, now int64) error {
	if s.EntitlementVersion >= 3 || s.QuotaVersion >= OmniQuotaVersion || s.End <= now {
		return nil
	}
	p, err := omniPlan(s.PlanKey)
	if err != nil {
		return err
	}
	five, week := OmniQuota(p.FiveHourCents, s.FxMicros), OmniQuota(p.WeekCents, s.FxMicros)
	if five <= 0 || week <= 0 {
		return errors.New("invalid subscription allowance")
	}
	audit, _ := json.Marshal(map[string]any{"plan": s.PlanKey, "start": s.Start, "end": s.End, "five_epoch": s.FiveEpoch, "week_epoch": s.WeekEpoch, "five_used": s.FiveUsed, "week_used": s.WeekUsed, "old_five": s.FiveLimit, "old_week": s.WeekLimit, "new_five": max(s.FiveLimit, five), "new_week": max(s.WeekLimit, week), "quota_version": OmniQuotaVersion})
	if err = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&OmniLedger{Key: fmt.Sprintf("quota-upgrade:%d:%d:%d", OmniQuotaVersion, s.UserID, s.Start), UserID: s.UserID, Kind: "subscription_quota_upgrade", AuditJSON: string(audit), CreatedAt: now}).Error; err != nil {
		return err
	}
	s.FiveLimit = max(s.FiveLimit, five)
	s.WeekLimit = max(s.WeekLimit, week)
	s.QuotaVersion = OmniQuotaVersion
	return tx.Save(s).Error
}
func omniLifecycleTx(tx *gorm.DB, uid int, now int64) error {
	var s OmniSubscription
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&s, "user_id = ?", uid).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = omniUpgradeQuotaTx(tx, &s, now); err != nil {
		return err
	}
	busy, err := omniBusy(tx, uid)
	if err != nil {
		return err
	}
	if s.Start > now || s.ActivationPending {
		if busy {
			s.ActivationPending = true
			return tx.Save(&s).Error
		}
		if s.MonthsPurchased <= 0 || s.FiveUsed != 0 || s.WeekUsed != 0 {
			return errors.New("subscription activation requires audit")
		}
		oldStart := s.Start
		if err := omniPauseLegacy(tx, uid, now); err != nil {
			return err
		}
		s.Start = now
		s.End = OmniCalendarEnd(now, s.MonthsPurchased)
		s.FiveEpoch = 0
		s.WeekEpoch = 0
		s.ActivationPending = false
		if err := tx.Save(&s).Error; err != nil {
			return err
		}
		key := fmt.Sprintf("activation:%d:%d", uid, oldStart)
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&OmniLedger{Key: key, UserID: uid, Kind: "subscription_activation", CreatedAt: now}).Error
	}
	if err := s.advance(now); err != nil {
		return err
	}
	if err := tx.Save(&s).Error; err != nil {
		return err
	}
	if s.End <= now && !busy {
		return omniResumeLegacy(tx, uid, now)
	}
	return nil
}
func OmniReconcile(uid int) error {
	defer invalidateUserCache(uid)
	return DB.Transaction(func(tx *gorm.DB) error {
		if _, err := omniAccount(tx, uid); err != nil {
			return err
		}
		return omniLifecycleTx(tx, uid, time.Now().Unix())
	})
}
func omniLifecycleConfigured() bool {
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	return DB != nil && common.OptionMap["OmniAPICommercial"] != ""
}
func OmniLegacyBegin(uid int, id string) error {
	if !omniLifecycleConfigured() {
		return nil
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		if _, err := omniAccount(tx, uid); err != nil {
			return err
		}
		if err := omniLifecycleTx(tx, uid, time.Now().Unix()); err != nil {
			return err
		}
		var s OmniSubscription
		if tx.First(&s, "user_id = ?", uid).Error == nil && !s.NativeLegacyAwait && (s.ActivationPending || s.End > time.Now().Unix()) {
			return errors.New("订阅生效处理中，请稍后重试")
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&OmniLegacyFlight{RequestID: id, UserID: uid, State: "pending", CreatedAt: time.Now().Unix()}).Error
	})
}
func OmniLegacyFinish(uid int, id string) {
	if !omniLifecycleConfigured() {
		return
	}
	if DB.Model(&OmniLegacyFlight{}).Where("request_id = ? AND user_id = ?", id, uid).Update("state", "settled").Error == nil {
		_ = OmniReconcile(uid)
	}
}
