package model

import (
	"encoding/json"
	"errors"
	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Lock both registration inserts and the local policy reader during cutover.
// Every pre-cutover user keeps the legacy reward; post-cutover recovery cannot
// replay rewards that were already credited under the old system.
func OmniEnableReferrals() error {
	common.OptionMapRWMutex.Lock()
	defer common.OptionMapRWMutex.Unlock()
	previous := common.OptionMap["OmniAPICommercial"]
	var p OmniPolicy
	if err := json.Unmarshal([]byte(previous), &p); err != nil {
		return errors.New("commercial policy must be configured first")
	}
	if len(p.RolloutUserIDs) > 0 {
		return errors.New("referral cutover requires general rollout")
	}
	if p.ReferralsEnabled && p.ReferralStartID > 0 {
		return nil
	}
	err := DB.Transaction(func(tx *gorm.DB) error {
		if common.MainDatabaseType() == common.DatabaseTypePostgreSQL {
			if err := tx.Exec("LOCK TABLE users IN SHARE MODE").Error; err != nil {
				return err
			}
		} else if common.MainDatabaseType() != common.DatabaseTypeSQLite {
			return errors.New("referral cutover requires PostgreSQL")
		}
		var maxID int
		if err := tx.Model(&User{}).Select("COALESCE(MAX(id),0)").Scan(&maxID).Error; err != nil {
			return err
		}
		p.ReferralStartID = maxID + 1
		p.ReferralsEnabled = true
		encoded, err := json.Marshal(p)
		if err != nil {
			return err
		}
		if err = tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"value"})}).Create(&Option{Key: "OmniAPICommercial", Value: string(encoded)}).Error; err != nil {
			return err
		}
		common.OptionMap["OmniAPICommercial"] = string(encoded)
		return nil
	})
	if err != nil {
		common.OptionMap["OmniAPICommercial"] = previous
	}
	return err
}
func OmniRegistrationReward(inviter, invitee int) error {
	p := GetOmniPolicy()
	if p.ReferralsEnabled && p.ReferralStartID > 0 && invitee >= p.ReferralStartID {
		return OmniRewardReferral(inviter, invitee)
	}
	return OmniRiskLegacyReferral(inviter, invitee)
}
