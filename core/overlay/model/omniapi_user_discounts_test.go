package model

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func omniDiscountRuleFixture() OmniUserDiscountRule {
	return OmniUserDiscountRule{Enabled: true, Basis: "procurement_margin", TargetMargin: .4, FundingScope: "wallet", ModelScope: "text", PublicLabel: "价格优惠"}
}
func TestOmniUserDiscountCASInitialInsertAndStaleVersion(t *testing.T) {
	oldDB, oldOptions := DB, common.OptionMap
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Option{}))
	DB = db
	common.OptionMap = map[string]string{}
	t.Cleanup(func() { DB = oldDB; common.OptionMap = oldOptions; sql, _ := db.DB(); sql.Close() })
	first := OmniUserDiscountPolicy{Version: "test-v1", Users: map[string]OmniUserDiscountRule{"16": omniDiscountRuleFixture()}}
	require.NoError(t, OmniUpdateUserDiscounts("", first))
	require.Equal(t, first, GetOmniUserDiscounts())
	stale := first
	stale.Version = "test-stale"
	require.ErrorContains(t, OmniUpdateUserDiscounts("", stale), "version conflict")
	next := first
	next.Version = "test-v2"
	next.Users = map[string]OmniUserDiscountRule{"16": omniDiscountRuleFixture()}
	rule := next.Users["16"]
	rule.Enabled = false
	next.Users["16"] = rule
	require.NoError(t, OmniUpdateUserDiscounts("test-v1", next))
	require.Equal(t, "test-v2", GetOmniUserDiscounts().Version)
	require.ErrorContains(t, OmniUpdateUserDiscounts("test-v1", stale), "version conflict")
	var stored Option
	require.NoError(t, db.First(&stored, "key = ?", OmniUserDiscountOptionKey).Error)
	var actual OmniUserDiscountPolicy
	require.NoError(t, json.Unmarshal([]byte(stored.Value), &actual))
	require.Equal(t, next, actual)
}
func TestOmniUserDiscountRejectsLegacyRuleConflictAndInvalidScope(t *testing.T) {
	oldDB, oldOptions := DB, common.OptionMap
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Option{}))
	DB = db
	legacy := `{"1009":{"enabled":true,"multiplier":1.1}}`
	require.NoError(t, db.Create(&Option{Key: "OmniAPIPrivateBilling", Value: legacy}).Error)
	common.OptionMap = map[string]string{"OmniAPIPrivateBilling": legacy}
	t.Cleanup(func() { DB = oldDB; common.OptionMap = oldOptions; sql, _ := db.DB(); sql.Close() })
	require.True(t, OmniUserHasLegacyPrivateBilling(1009))
	require.False(t, OmniUserHasLegacyPrivateBilling(16))
	p := OmniUserDiscountPolicy{Version: "conflict", Users: map[string]OmniUserDiscountRule{"1009": omniDiscountRuleFixture()}}
	require.ErrorContains(t, OmniUpdateUserDiscounts("", p), "conflicts")
	for _, margin := range []float64{0, .4, .9} {
		rule := omniDiscountRuleFixture()
		rule.TargetMargin = margin
		require.NoError(t, OmniValidateUserDiscounts(OmniUserDiscountPolicy{Version: "valid", Users: map[string]OmniUserDiscountRule{"16": rule}}))
	}
	for _, scope := range []string{"all", "subscription"} {
		rule := omniDiscountRuleFixture()
		rule.FundingScope = scope
		require.Error(t, OmniValidateUserDiscounts(OmniUserDiscountPolicy{Version: "bad", Users: map[string]OmniUserDiscountRule{"16": rule}}))
	}
	rule := omniDiscountRuleFixture()
	rule.TargetMargin = .91
	require.Error(t, OmniValidateUserDiscounts(OmniUserDiscountPolicy{Version: "bad", Users: map[string]OmniUserDiscountRule{"16": rule}}))
	require.Equal(t, "", GetOmniUserDiscounts().Version)
}
