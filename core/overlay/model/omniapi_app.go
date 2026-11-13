package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"math"
	"time"
)

type OmniAppLogin struct {
	ID              string `gorm:"primaryKey;size:80"`
	Challenge       string `gorm:"size:64"`
	Device          string `gorm:"size:80"`
	Platform        string `gorm:"size:16"`
	UserID          int
	SessionID       string
	UserAuthVersion int64
	SessionVersion  int64
	End             int64 `gorm:"index"`
	Consumed        bool
}
type OmniSyncSubscription struct {
	UserID     int   `gorm:"primaryKey" json:"user_id"`
	End        int64 `json:"end"`
	QuotaBytes int64 `json:"quota_bytes"`
}
type OmniAppChildSession struct {
	SID       string `gorm:"column:sid;primaryKey;size:64"`
	ParentSID string `gorm:"column:parent_sid;size:64;index"`
	UserID    int    `gorm:"index"`
}

func OmniAppParentSessionActive(identity AuthSessionIdentity) error {
	sid := identity.SessionID
	seen := map[string]bool{}
	for depth := 0; depth < 8; depth++ {
		if seen[sid] {
			return errors.New("cyclic native session")
		}
		seen[sid] = true
		var child OmniAppChildSession
		err := DB.First(&child, "sid = ?", sid).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		var parent UserSession
		if err := DB.First(&parent, "sid = ?", child.ParentSID).Error; err != nil {
			return err
		}
		if child.UserID != identity.UserID || parent.UserID != identity.UserID || parent.Status != UserSessionStatusActive || parent.RevokedAt != 0 || parent.ExpiresAt <= time.Now().Unix() || parent.UserAuthVersion != identity.UserAuthVersion {
			return ErrUserSessionInactive
		}
		sid = parent.SID
	}
	return errors.New("native session chain too deep")
}

type OmniSyncPolicy struct {
	Enabled      bool  `json:"enabled"`
	MonthlyCents int64 `json:"monthly_cents"`
	QuotaBytes   int64 `json:"quota_bytes"`
}

func OmniSyncConfig() OmniSyncPolicy {
	p := OmniSyncPolicy{QuotaBytes: 1073741824}
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap["OmniAPISync"]
	common.OptionMapRWMutex.RUnlock()
	if raw != "" {
		if json.Unmarshal([]byte(raw), &p) != nil {
			return OmniSyncPolicy{QuotaBytes: 1073741824}
		}
	}
	// First release has one fixed 1 GiB product. Fail closed on invalid policy.
	if p.MonthlyCents <= 0 || p.MonthlyCents > 100000000 || p.QuotaBytes != 1073741824 {
		p.Enabled = false
	}
	return p
}
func OmniSyncEntitlement(uid int) (OmniSyncSubscription, error) {
	var s OmniSyncSubscription
	err := DB.First(&s, "user_id = ?", uid).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return OmniSyncSubscription{UserID: uid, QuotaBytes: 1073741824}, nil
	}
	return s, err
}
func OmniSyncPurchase(uid int, key string) (OmniSyncSubscription, error) {
	defer invalidateUserCache(uid)
	p := OmniSyncConfig()
	var result OmniSyncSubscription
	if !p.Enabled || len(key) < 16 || len(key) > 80 {
		return result, errors.New("同步套餐暂不可购买")
	}
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap["OmniAPIPricing"]
	common.OptionMapRWMutex.RUnlock()
	var snapshot struct {
		FX string `json:"cny_per_usd"`
	}
	var rate float64
	if json.Unmarshal([]byte(raw), &snapshot) != nil {
		return result, errors.New("汇率暂不可用")
	}
	if _, err := fmt.Sscan(snapshot.FX, &rate); err != nil || math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 1 || rate > 20 {
		return result, errors.New("汇率暂不可用")
	}
	fx := int64(math.Round(rate * 1000000))
	quota := int64(math.Ceil(float64(p.MonthlyCents) * 10000 * common.QuotaPerUnit / float64(fx)))
	businessKey := fmt.Sprintf("wallet-sync:%d:%s", uid, key)
	err := DB.Transaction(func(tx *gorm.DB) error {
		if _, err := omniAccount(tx, uid); err != nil {
			return err
		}
		var ledger OmniLedger
		err := tx.First(&ledger, "key = ?", businessKey).Error
		if err == nil {
			if ledger.UserID != uid || ledger.Kind != "sync:monthly" {
				return errors.New("purchase key conflict")
			}
			return tx.First(&result, "user_id = ?", uid).Error
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		debit := tx.Model(&User{}).Where("id = ? AND status = ? AND quota >= ?", uid, common.UserStatusEnabled, quota).UpdateColumn("quota", gorm.Expr("quota - ?", quota))
		if debit.Error != nil {
			return debit.Error
		}
		if debit.RowsAffected != 1 {
			return errors.New("钱包余额不足，请先充值")
		}
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&result, "user_id = ?", uid).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		now := time.Now().Unix()
		start := max(now, result.End)
		result = OmniSyncSubscription{UserID: uid, End: OmniCalendarEnd(start, 1), QuotaBytes: p.QuotaBytes}
		if err := tx.Save(&result).Error; err != nil {
			return err
		}
		audit, _ := json.Marshal(map[string]any{"sync_end": result.End, "quota_bytes": result.QuotaBytes})
		return tx.Create(&OmniLedger{Key: businessKey, UserID: uid, Kind: "sync:monthly", Cents: p.MonthlyCents, Quota: quota, FxMicros: fx, FxSource: "wallet:sync:monthly", AuditJSON: string(audit), CreatedAt: now}).Error
	})
	return result, err
}
