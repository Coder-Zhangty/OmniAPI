package model

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const OmniUserDiscountOptionKey = "OmniAPIUserDiscounts"
const OmniUserDiscountOption = OmniUserDiscountOptionKey

type OmniUserDiscountRule struct {
	Enabled      bool    `json:"enabled"`
	Basis        string  `json:"basis"`
	TargetMargin float64 `json:"target_margin"`
	FundingScope string  `json:"funding_scope"`
	ModelScope   string  `json:"model_scope"`
	PublicLabel  string  `json:"public_label"`
}
type OmniUserDiscountPolicy struct {
	Version string                          `json:"version"`
	Users   map[string]OmniUserDiscountRule `json:"users"`
}

var omniUserDiscountWrite sync.Mutex

func GetOmniUserDiscounts() OmniUserDiscountPolicy {
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap[OmniUserDiscountOption]
	common.OptionMapRWMutex.RUnlock()
	p := OmniUserDiscountPolicy{Users: map[string]OmniUserDiscountRule{}}
	if raw == "" {
		return p
	}
	if json.Unmarshal([]byte(raw), &p) != nil || OmniValidateUserDiscounts(p) != nil {
		return OmniUserDiscountPolicy{Version: "invalid", Users: map[string]OmniUserDiscountRule{}}
	}
	return p
}

func OmniValidateUserDiscounts(p OmniUserDiscountPolicy) error {
	if p.Version == "" || len(p.Version) > 128 || len(p.Users) > 10000 {
		return errors.New("invalid user discount policy")
	}
	for key, rule := range p.Users {
		id, err := strconv.Atoi(key)
		if err != nil || id <= 0 || strconv.Itoa(id) != key || rule.Basis != "procurement_margin" || rule.FundingScope != "wallet" || rule.ModelScope != "text" || rule.PublicLabel != "价格优惠" || math.IsNaN(rule.TargetMargin) || math.IsInf(rule.TargetMargin, 0) || rule.TargetMargin < 0 || rule.TargetMargin > .9 {
			return errors.New("invalid user discount rule")
		}
	}
	return nil
}

func omniUserDiscountLegacyActive(raw string, uid int) bool {
	var rules map[string]struct {
		Enabled    bool    `json:"enabled"`
		Multiplier float64 `json:"multiplier"`
	}
	if json.Unmarshal([]byte(raw), &rules) != nil {
		return false
	}
	rule := rules[strconv.Itoa(uid)]
	return rule.Enabled && rule.Multiplier >= 1 && rule.Multiplier <= 10 && !math.IsNaN(rule.Multiplier) && !math.IsInf(rule.Multiplier, 0)
}
func OmniUserHasLegacyPrivateBilling(uid int) bool {
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap["OmniAPIPrivateBilling"]
	common.OptionMapRWMutex.RUnlock()
	return omniUserDiscountLegacyActive(raw, uid)
}

// Updates compare the whole stored value as well as its version. Absent policy
// insertion is guarded by the option primary key; concurrent creators conflict.
func OmniUpdateUserDiscounts(expected string, next OmniUserDiscountPolicy) error {
	if err := OmniValidateUserDiscounts(next); err != nil {
		return err
	}
	if next.Version == expected {
		return errors.New("discount policy version must change")
	}
	omniUserDiscountWrite.Lock()
	defer omniUserDiscountWrite.Unlock()
	data, err := json.Marshal(next)
	if err != nil {
		return err
	}
	err = DB.Transaction(func(tx *gorm.DB) error {
		var legacy Option
		legacyErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("key = ?", "OmniAPIPrivateBilling").First(&legacy).Error
		if legacyErr != nil && !errors.Is(legacyErr, gorm.ErrRecordNotFound) {
			return legacyErr
		}
		for uid, rule := range next.Users {
			if rule.Enabled {
				id, _ := strconv.Atoi(uid)
				if omniUserDiscountLegacyActive(legacy.Value, id) {
					return errors.New("legacy private billing conflicts with user discount")
				}
			}
		}
		var stored Option
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("key = ?", OmniUserDiscountOption).First(&stored).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if expected != "" {
				return errors.New("user discount version conflict")
			}
			result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&Option{Key: OmniUserDiscountOption, Value: string(data)})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return errors.New("user discount version conflict")
			}
			return nil
		}
		if err != nil {
			return err
		}
		var current OmniUserDiscountPolicy
		if json.Unmarshal([]byte(stored.Value), &current) != nil || current.Version != expected {
			return errors.New("user discount version conflict")
		}
		result := tx.Model(&Option{}).Where("key = ? AND value = ?", OmniUserDiscountOption, stored.Value).Update("value", string(data))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("user discount version conflict")
		}
		return nil
	})
	if err != nil {
		return err
	}
	return updateOptionMap(OmniUserDiscountOption, string(data))
}
