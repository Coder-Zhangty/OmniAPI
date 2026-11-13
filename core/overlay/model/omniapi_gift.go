package model

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

type OmniGiftInputError struct{ Message string }

func (e *OmniGiftInputError) Error() string { return e.Message }
func giftInput(s string) error              { return &OmniGiftInputError{Message: s} }

var ErrOmniGiftUnavailable = errors.New("福利码无效、已停用、过期或名额已领完")

type OmniGiftCampaign struct {
	ID        string `json:"id" gorm:"primaryKey;size:64"`
	RequestID string `json:"-" gorm:"size:80"`
	Code      string `json:"code" gorm:"uniqueIndex;size:32"`
	Title     string `json:"title" gorm:"size:120"`
	AmountUSD string `json:"amount_usd" gorm:"size:32"`
	Quota     int64  `json:"quota"`
	MaxClaims int    `json:"max_claims"`
	Claimed   int    `json:"claimed"`
	Enabled   bool   `json:"enabled"`
	ExpiresAt int64  `json:"expires_at"`
	CreatedAt int64  `json:"created_at"`
	CreatedBy int    `json:"created_by"`
	Version   int64  `json:"version"`
}
type OmniGiftClaim struct {
	CampaignID string `json:"campaign_id" gorm:"primaryKey;size:64"`
	UserID     int    `json:"user_id" gorm:"primaryKey;autoIncrement:false"`
	Title      string `json:"title"`
	AmountUSD  string `json:"amount_usd"`
	Quota      int64  `json:"quota"`
	CreatedAt  int64  `json:"created_at" gorm:"index"`
}
type OmniGiftAction struct {
	ID         string `json:"id" gorm:"primaryKey;size:100"`
	CampaignID string `json:"campaign_id" gorm:"index;size:64"`
	ActorID    int    `json:"actor_id"`
	Action     string `json:"action"`
	CreatedAt  int64  `json:"created_at"`
}
type OmniGiftReceipt struct {
	OmniGiftClaim
	AlreadyClaimed bool `json:"already_claimed"`
}

func OmniGiftMigrate(db *gorm.DB) error {
	return db.AutoMigrate(&OmniGiftCampaign{}, &OmniGiftClaim{}, &OmniGiftAction{})
}

func OmniGiftCreate(actor int, requestID, title, amount string, limit int, expires int64) (OmniGiftCampaign, error) {
	out := OmniGiftCampaign{}
	title = strings.TrimSpace(title)
	if actor <= 0 || len(requestID) < 16 || len(requestID) > 80 || strings.TrimSpace(requestID) != requestID || utf8.RuneCountInString(title) < 1 || utf8.RuneCountInString(title) > 60 || limit < 1 || limit > 100000 || (expires != 0 && expires <= time.Now().Unix()) {
		return out, giftInput("请填写活动名称、有效请求编号、1–100000个名额及未来截止时间")
	}
	if !regexp.MustCompile(`^[0-9]{1,5}(\.[0-9]{1,6})?$`).MatchString(amount) {
		return out, giftInput("赠送额度无效")
	}
	value, e := decimal.NewFromString(amount)
	if e != nil || value.Exponent() < -6 || !value.IsPositive() || value.GreaterThan(decimal.NewFromInt(10000)) {
		return out, giftInput("每人额度须大于0且不超过 US$10000，最多六位小数")
	}
	quota := value.Mul(decimal.NewFromFloat(common.QuotaPerUnit))
	if !quota.Equal(quota.Truncate(0)) || quota.LessThan(decimal.NewFromInt(1)) {
		return out, giftInput("额度不能精确换算为平台配额，请调整末位小数")
	}
	if e = common.ValidateWalletQuota(int(quota.IntPart())); e != nil {
		return out, e
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", actor, requestID)))
	id := "gift_" + hex.EncodeToString(sum[:])[:40]
	raw := make([]byte, 10)
	if _, e = rand.Read(raw); e != nil {
		return out, e
	}
	candidate := OmniGiftCampaign{ID: id, RequestID: requestID, Code: "GIFT-" + strings.ToUpper(hex.EncodeToString(raw)), Title: title, AmountUSD: value.String(), Quota: quota.IntPart(), MaxClaims: limit, Enabled: true, ExpiresAt: expires, CreatedAt: time.Now().Unix(), CreatedBy: actor, Version: 1}
	e = DB.Session(&gorm.Session{Logger: logger.Discard}).Transaction(func(tx *gorm.DB) error {
		result := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoNothing: true}).Create(&candidate)
		if result.Error != nil {
			return result.Error
		}
		if e := tx.First(&out, "id = ?", id).Error; e != nil {
			return e
		}
		if out.Title != candidate.Title || out.AmountUSD != candidate.AmountUSD || out.MaxClaims != limit || out.ExpiresAt != expires {
			return giftInput("同一下单编号的福利参数不一致，请刷新后重试")
		}
		if result.RowsAffected == 1 {
			return tx.Create(&OmniGiftAction{ID: id + ":1", CampaignID: id, ActorID: actor, Action: "create", CreatedAt: candidate.CreatedAt}).Error
		}
		return nil
	})
	return out, e
}
func OmniGiftSetEnabled(id string, version int64, enabled bool, actor int) error {
	return DB.Session(&gorm.Session{Logger: logger.Discard}).Transaction(func(tx *gorm.DB) error {
		q := tx.Model(&OmniGiftCampaign{}).Where("id = ? AND version = ?", id, version).Updates(map[string]any{"enabled": enabled, "version": gorm.Expr("version + 1")})
		if q.Error != nil {
			return q.Error
		}
		if q.RowsAffected != 1 {
			return giftInput("福利码已被更新，请刷新后重试")
		}
		action := "disable"
		if enabled {
			action = "enable"
		}
		return tx.Create(&OmniGiftAction{ID: fmt.Sprintf("%s:%d", id, version+1), CampaignID: id, ActorID: actor, Action: action, CreatedAt: time.Now().Unix()}).Error
	})
}

// Lock order: campaign -> account -> user. A claim, slot, wallet increment,
// gift ledger, visible system log and personal notice commit together.
// Gifts never pass through real-payment credit or membership/reward accrual.
func OmniGiftRedeem(code string, uid int, expectedAuth ...int64) (OmniGiftReceipt, error) {
	var out OmniGiftReceipt
	code = strings.ToUpper(strings.TrimSpace(code))
	if uid <= 0 || len(code) != 25 || !strings.HasPrefix(code, "GIFT-") {
		return out, ErrOmniGiftUnavailable
	}
	e := DB.Session(&gorm.Session{Logger: logger.Discard}).Transaction(func(tx *gorm.DB) error {
		var campaign OmniGiftCampaign
		if e := lockForUpdate(tx).Where("code = ?", code).First(&campaign).Error; e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return ErrOmniGiftUnavailable
			}
			return e
		}
		if _, e := omniAccount(tx, uid); e != nil {
			return e
		}
		var user User
		if e := lockForUpdate(tx).Select("id", "username", "status", "auth_version").First(&user, uid).Error; e != nil {
			return e
		}
		if user.Status != common.UserStatusEnabled {
			return ErrOmniRiskHold
		}
		if len(expectedAuth) > 0 && expectedAuth[0] > 0 && user.AuthVersion != expectedAuth[0] {
			return ErrOmniRiskHold
		}
		if e := OmniRiskBusinessMutation(tx, uid, expectedAuth...); e != nil {
			return e
		}
		err := tx.Where("campaign_id = ? AND user_id = ?", campaign.ID, uid).First(&out.OmniGiftClaim).Error
		if err == nil {
			out.AlreadyClaimed = true
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		now := time.Now().Unix()
		if !campaign.Enabled || campaign.Claimed >= campaign.MaxClaims || (campaign.ExpiresAt != 0 && campaign.ExpiresAt <= now) {
			return ErrOmniGiftUnavailable
		}
		slot := tx.Model(&OmniGiftCampaign{}).Where("id = ? AND enabled = ? AND claimed < max_claims AND (expires_at = 0 OR expires_at > ?)", campaign.ID, true, now).UpdateColumn("claimed", gorm.Expr("claimed + 1"))
		if slot.Error != nil {
			return slot.Error
		}
		if slot.RowsAffected != 1 {
			return ErrOmniGiftUnavailable
		}
		out.OmniGiftClaim = OmniGiftClaim{CampaignID: campaign.ID, UserID: uid, Title: campaign.Title, AmountUSD: campaign.AmountUSD, Quota: campaign.Quota, CreatedAt: now}
		if e := tx.Create(&out.OmniGiftClaim).Error; e != nil {
			return e
		}
		if e := creditTopUpQuota(tx, uid, int(campaign.Quota), nil); e != nil {
			return e
		}
		key := fmt.Sprintf("gift:%s:%d", campaign.ID, uid)
		if e := tx.Create(&OmniLedger{Key: key, UserID: uid, Kind: "gift", Quota: campaign.Quota, CreatedAt: now}).Error; e != nil {
			return e
		}
		content := fmt.Sprintf("多人福利码赠额 US$%s · %s · 活动 %s", campaign.AmountUSD, campaign.Title, campaign.ID)
		if e := tx.Create(&Log{UserId: uid, Username: user.Username, CreatedAt: now, Type: LogTypeSystem, Quota: int(campaign.Quota), Content: content}).Error; e != nil {
			return e
		}
		return omniNotice(tx, key, uid, "福利已到账", fmt.Sprintf("%s：赠送 US$%s 已存入钱包，每个账号限领一次。", campaign.Title, campaign.AmountUSD))
	})
	if e == nil {
		invalidateUserCache(uid)
	}
	return out, e
}
