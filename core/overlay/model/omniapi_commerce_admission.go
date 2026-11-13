package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"sync"
)

var omniCommerceReadyMu sync.RWMutex
var omniCommerceDB *gorm.DB

func OmniCommerceReady() bool {
	omniCommerceReadyMu.RLock()
	defer omniCommerceReadyMu.RUnlock()
	return DB != nil && DB == omniCommerceDB
}

func OmniValidateSharedToken(tx *gorm.DB, t *Token) error {
	if t.OmniSharedRevoked {
		return errors.New("共享Key权限已被撤销，请创建新Key")
	}
	if t.Id > 0 {
		var old Token
		if err := tx.Select("id", "omni_shared_revoked", "omni_pool_id", "user_id").First(&old, t.Id).Error; err != nil {
			return err
		}
		if old.OmniPoolID != t.OmniPoolID || old.UserId != t.UserId {
			return errors.New("现有Key不能改绑成员或共享池")
		}
		if old.OmniSharedRevoked {
			return errors.New("共享Key权限已被撤销")
		}
	}
	if t.OmniFallback == "" {
		t.OmniFallback = "off"
	}
	switch t.OmniFallback {
	case "off", "subscription_only", "wallet_only", "subscription_then_wallet":
	default:
		return errors.New("未知个人续用模式")
	}
	if t.OmniPoolID == "" {
		if t.OmniFallback != "off" {
			return errors.New("个人Key不支持共享续用设置")
		}
		return nil
	}
	if _, err := omniCommercePool(tx, t.OmniPoolID); err != nil {
		return err
	}
	if _, err := omniSharedMember(tx, t.OmniPoolID, t.UserId); err != nil {
		return err
	}
	return nil
}

// Always read membership and token permission from the database, never from a
// stale token cache. A removed member cannot gain personal fallback admission.
func OmniSharedAuthorization(uid, tokenID int, poolID, modelName string, snapshots ...map[string]string) (Token, OmniCommerceProfile, error) {
	var t Token
	var profile OmniCommerceProfile
	err := omniRequestDB(snapshots...).Transaction(func(tx *gorm.DB) error {
		if _, err := omniAccount(tx, uid); err != nil {
			return err
		}
		p, err := omniCommercePool(tx, poolID)
		if err != nil {
			return err
		}
		if p.Frozen {
			return errors.New("共享池需要管理员核对")
		}
		if _, err = omniSharedMember(tx, poolID, uid); err != nil {
			return err
		}
		if err = tx.First(&t, "id = ? AND user_id = ?", tokenID, uid).Error; err != nil {
			return err
		}
		if t.Status != common.TokenStatusEnabled || t.OmniSharedRevoked || t.OmniPoolID != poolID {
			return errors.New("共享Key已失效")
		}
		if err = OmniRiskAdmission(tx, uid); err != nil {
			return err
		}
		if err = omniSharedAdvance(tx, &p, common.GetTimestamp()); err != nil {
			return err
		}
		var period OmniSharedPeriod
		if err = tx.First(&period, "id = ?", p.CurrentPeriod).Error; err != nil {
			return err
		}
		if json.Unmarshal([]byte(period.ProfileJSON), &profile) != nil || !profile.Allows(modelName, 0) {
			return errors.New("该模型不在共享套餐核价范围，不会转扣个人资金")
		}
		profile = profile.effectiveCapabilitiesFrom(omniRequestOptionsFor(tx))
		return nil
	})
	return t, profile, err
}

// Caller already holds the actor account lock; lock ordering is account, pool,
// then subscription. This closes removal/disable races at funding selection.
func omniSharedFallbackTx(tx *gorm.DB, uid, tokenID int, poolID, modelName string) (string, string, error) {
	p, err := omniCommercePool(tx, poolID)
	if err != nil {
		return "", "", err
	}
	if p.Frozen {
		return "", "", errors.New("共享池需要管理员核对")
	}
	if _, err = omniSharedMember(tx, poolID, uid); err != nil {
		return "", "", err
	}
	var token Token
	if err = tx.First(&token, "id = ? AND user_id = ?", tokenID, uid).Error; err != nil {
		return "", "", err
	}
	if token.OmniPoolID != poolID || token.OmniSharedRevoked || token.Status != common.TokenStatusEnabled {
		return "", "", errors.New("共享Key已失效")
	}
	if token.OmniFallback == "off" || token.OmniFallback == "" {
		return "", "", ErrOmniSharedUnavailable
	}
	if err = omniSharedAdvance(tx, &p, common.GetTimestamp()); err != nil {
		return "", "", err
	}
	var period OmniSharedPeriod
	if err = tx.First(&period, "id = ?", p.CurrentPeriod).Error; err != nil {
		return "", "", err
	}
	var profile OmniCommerceProfile
	if json.Unmarshal([]byte(period.ProfileJSON), &profile) != nil || !profile.Allows(modelName, 0) {
		return "", "", errors.New("该模型不在共享套餐核价范围")
	}
	return token.OmniFallback, omniFreezeCapabilityProfileFrom(period.ProfileJSON, omniRequestOptionsFor(tx)), nil
}
func OmniCommerceCheckChannel(uid int, poolID, requestID, modelName string, channelID int) error {
	return omniCommerceCheckChannel(uid, poolID, requestID, modelName, channelID, "")
}

func OmniCommerceCheckChannelPath(uid int, poolID, requestID, modelName string, channelID int, path string, snapshots ...map[string]string) error {
	return omniCommerceCheckChannel(uid, poolID, requestID, modelName, channelID, path, snapshots...)
}

func omniCommerceCheckChannel(uid int, poolID, requestID, modelName string, channelID int, path string, snapshots ...map[string]string) error {
	if DB == nil || poolID == "" && !OmniCommerceSettings().Enabled {
		return nil
	}
	var raw string
	var record OmniReservation
	err := DB.First(&record, "request_id = ?", requestID).Error
	if err == nil {
		if record.UserID != uid || record.SharedPoolID != poolID || record.State != "pending" {
			return errors.New("资金归属不一致")
		}
		raw = record.CommerceProfile
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if poolID != "" {
		if _, err := omniSharedMember(DB, poolID, uid); err != nil {
			return err
		}
		if record.SharedTokenID > 0 {
			var t Token
			if err := DB.First(&t, "id = ? AND user_id = ?", record.SharedTokenID, uid).Error; err != nil {
				return err
			}
			if t.Status != common.TokenStatusEnabled || t.OmniSharedRevoked || t.OmniPoolID != poolID {
				return errors.New("共享Key已失效")
			}
		}
		if raw == "" {
			var p OmniSharedPool
			if err := DB.First(&p, "id = ?", poolID).Error; err != nil {
				return err
			}
			if p.Frozen {
				return errors.New("共享池需要管理员核对")
			}
			var period OmniSharedPeriod
			if err := DB.First(&period, "id = ?", p.CurrentPeriod).Error; err != nil {
				return err
			}
			raw = period.ProfileJSON
		}
	}
	// An independent personal wallet request does not inherit expired plan scope.
	if raw == "" {
		return nil
	}
	var p OmniCommerceProfile
	if json.Unmarshal([]byte(raw), &p) != nil {
		return errors.New("套餐通道配置无效")
	}
	snapshot := omniRequestOptionsFor(omniRequestDB(snapshots...))
	p = p.effectiveCapabilitiesFrom(snapshot)
	if !p.allowsSnapshot(modelName, channelID) {
		return errors.New("该模型或实际通道未在套餐核价范围内")
	}
	if !omniCapabilityProfileCurrentFrom(p, snapshot) {
		return errors.New("套餐通道配置已变化，待完成成本复核")
	}
	if path != "" {
		_, err = p.capabilityFromSnapshot(modelName, channelID, path)
		return err
	}
	return nil
}

// Restart recovery never invents provider usage or releases an unknown charge.
// Confirmed async tasks retain their pinned hold for their terminal poll result.
func OmniSharedRecoverOrphans() error {
	if DB == nil {
		return nil
	}
	var rows []OmniSharedHold
	if err := DB.Where("owner <> ? AND state = ? AND dispatch_state <> ?", OmniRiskProcessID, "pending", "task").Find(&rows).Error; err != nil {
		return err
	}
	for _, h := range rows {
		var process OmniSharedProcess
		if err := DB.Where("id = ?", h.Owner).Find(&process).Error; err != nil {
			return err
		}
		if process.HeartbeatAt > common.GetTimestamp()-120 {
			continue
		}

		if h.DispatchState == "reserved" {
			if _, err := omniSharedSettleAudit(h.RequestID, 0, &OmniSharedRecovery{Key: "auto-recover:" + h.RequestID, RequestID: h.RequestID, Evidence: "Expired process lease; no dispatch recorded", CreatedAt: common.GetTimestamp(), automatic: true}); err != nil {
				return err
			}
			continue
		}
		if err := DB.Transaction(func(tx *gorm.DB) error {
			p, err := omniCommercePool(tx, h.PoolID)
			if err != nil {
				return err
			}
			p.Frozen = true
			if err = tx.Save(&p).Error; err != nil {
				return err
			}
			return tx.Model(&OmniSharedHold{}).Where("request_id = ? AND state = ?", h.RequestID, "pending").Update("dispatch_state", "unknown").Error
		}); err != nil {
			return err
		}
	}
	return nil
}
func OmniSharedMarkDispatch(requestID string) error {
	if !OmniCommerceReady() {
		return errors.New("共享账本暂不可用")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var h OmniSharedHold
		if err := lockForUpdate(tx).First(&h, "request_id = ?", requestID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				var r OmniReservation
				if e := tx.First(&r, "request_id = ?", requestID).Error; e == nil && r.SharedPoolID != "" && r.State == "pending" {
					return nil
				}
			}
			return err
		}
		if h.State != "pending" || h.DispatchState == "unknown" {
			return errors.New("共享请求已关闭或等待核对")
		}
		if h.DispatchState == "reserved" {
			return tx.Model(&h).Update("dispatch_state", "sending").Error
		}
		return nil
	})
}

type OmniSharedProcess struct {
	ID          string `gorm:"primaryKey;size:64"`
	HeartbeatAt int64  `gorm:"index"`
}

func OmniSharedHeartbeat() error {
	if !OmniCommerceReady() {
		return nil
	}
	return DB.Clauses(clause.OnConflict{UpdateAll: true}).Create(&OmniSharedProcess{ID: OmniRiskProcessID, HeartbeatAt: common.GetTimestamp()}).Error
}

type OmniSharedRecovery struct {
	automatic bool   `json:"-" gorm:"-"`
	Key       string `json:"key" gorm:"primaryKey;size:160"`
	RequestID string `json:"request_id" gorm:"index;size:128"`
	PoolID    string `json:"pool_id" gorm:"index;size:64"`
	ActorID   int    `json:"actor_id"`
	Actual    int64  `json:"actual"`
	Evidence  string `json:"evidence" gorm:"type:text"`
	CreatedAt int64  `json:"created_at"`
}

func OmniSharedReconcile(actor int, requestID string, actual int64, evidence, key string) error {
	if actor <= 0 || actual < 0 || len(evidence) < 10 || len(key) < 16 || len(key) > 80 {
		return errors.New("需管理员、供应商最终用量证据与幂等键")
	}
	audit := OmniSharedRecovery{Key: fmt.Sprintf("shared-recovery:%d:%s", actor, key), RequestID: requestID, ActorID: actor, Actual: actual, Evidence: evidence, CreatedAt: common.GetTimestamp()}
	_, err := omniSharedSettleAudit(requestID, actual, &audit)
	return err
}
func OmniSharedUnfreeze(actor int, poolID, evidence, key string) error {
	if actor <= 0 || len(evidence) < 10 || len(key) < 16 || len(key) > 80 {
		return errors.New("需管理员、完整核对证据与幂等键")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		p, err := omniCommercePool(tx, poolID)
		if err != nil {
			return err
		}
		var n int64
		if err = tx.Model(&OmniSharedHold{}).Where("pool_id = ? AND (dispatch_state = ? AND state = ? OR platform_quota > 0)", poolID, "unknown", "pending").Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			return errors.New("仍有未知请求或超预留差异，先核对估算器与成本证据")
		}
		p.Frozen = false
		if err = tx.Save(&p).Error; err != nil {
			return err
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&OmniSharedRecovery{Key: fmt.Sprintf("shared-unfreeze:%d:%s", actor, key), PoolID: poolID, ActorID: actor, Evidence: evidence, CreatedAt: common.GetTimestamp()}).Error
	})
}
