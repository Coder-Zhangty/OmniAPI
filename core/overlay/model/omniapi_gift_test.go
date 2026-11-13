package model

import (
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func giftFixture(t *testing.T) {
	walletFixture(t)
	require.NoError(t, DB.Migrator().DropTable(&OmniGiftClaim{}, &OmniGiftAction{}, &OmniGiftCampaign{}))
	require.NoError(t, OmniGiftMigrate(DB))
	require.NoError(t, DB.AutoMigrate(&Log{}))
	require.NoError(t, DB.Create(&User{Id: 20001, Username: "gift-recipient", AffCode: "gift20001", Status: common.UserStatusEnabled, AuthVersion: 1, Quota: 100, CreatedAt: time.Now().Unix()}).Error)
	require.NoError(t, DB.Where("user_id >= ?", 20001).Delete(&OmniRiskControl{}).Error)
	require.NoError(t, DB.Where("user_id >= ?", 20001).Delete(&OmniRiskFundingEvidence{}).Error)
	require.NoError(t, DB.Where("user_id >= ?", 20001).Delete(&Log{}).Error)
}
func TestOmniGiftAtomicCreditHistoryAndReplay(t *testing.T) {
	giftFixture(t)
	campaign, e := OmniGiftCreate(1, "gift-create-request-00001", "周末福利", "1.000000", 2, 0)
	require.NoError(t, e)
	same, e := OmniGiftCreate(1, "gift-create-request-00001", "周末福利", "1", 2, 0)
	require.NoError(t, e)
	require.Equal(t, campaign.Code, same.Code)
	_, e = OmniGiftCreate(1, "gift-create-request-00001", "周末福利", "2", 2, 0)
	require.Error(t, e)
	receipt, e := OmniGiftRedeem(campaign.Code, 20001, 1)
	require.NoError(t, e)
	require.False(t, receipt.AlreadyClaimed)
	require.NoError(t, OmniGiftSetEnabled(campaign.ID, 1, false, 1))
	again, e := OmniGiftRedeem(campaign.Code, 20001, 1)
	require.NoError(t, e)
	require.True(t, again.AlreadyClaimed)
	var u User
	require.NoError(t, DB.First(&u, 20001).Error)
	require.Equal(t, 500100, u.Quota)
	var n int64
	for _, m := range []any{&OmniGiftClaim{}, &OmniLedger{}, &OmniNotice{}} {
		require.NoError(t, DB.Model(m).Where("user_id = ?", 20001).Count(&n).Error)
		require.Equal(t, int64(1), n)
	}
	var account OmniAccount
	require.NoError(t, DB.First(&account, "user_id = ?", 20001).Error)
	require.Zero(t, account.NetRechargeCents)
	paid, e := OmniRiskPaid(DB, 20001)
	require.NoError(t, e)
	require.False(t, paid, "a gift must never make an unpaid account appear paid")
	require.NoError(t, DB.Model(&Log{}).Where("user_id = ? AND type = ?", 20001, LogTypeSystem).Count(&n).Error)
	require.Equal(t, int64(1), n)
	require.NoError(t, DB.First(&campaign, "id = ?", campaign.ID).Error)
	require.Equal(t, 1, campaign.Claimed)
	require.Error(t, OmniGiftSetEnabled(campaign.ID, 1, true, 1), "stale administration write must not overwrite a newer decision")
}
func TestOmniGiftFailureRollsBackEveryFinancialEffect(t *testing.T) {
	giftFixture(t)
	campaign, e := OmniGiftCreate(1, "gift-rollback-request01", "事务回滚", "0.25", 1, 0)
	require.NoError(t, e)
	require.NoError(t, DB.Callback().Create().Before("gorm:create").Register("gift-notice-fail", func(tx *gorm.DB) {
		if tx.Statement.Table == "omni_notices" {
			tx.AddError(errors.New("synthetic storage failure"))
		}
	}))
	_, e = OmniGiftRedeem(campaign.Code, 20001, 1)
	require.Error(t, e)
	require.NoError(t, DB.Callback().Create().Remove("gift-notice-fail"))
	var u User
	require.NoError(t, DB.First(&u, 20001).Error)
	require.Equal(t, 100, u.Quota)
	var n int64
	for _, m := range []any{&OmniGiftClaim{}, &OmniLedger{}, &OmniNotice{}} {
		require.NoError(t, DB.Model(m).Where("user_id = ?", 20001).Count(&n).Error)
		require.Zero(t, n)
	}
	require.NoError(t, DB.First(&campaign, "id = ?", campaign.ID).Error)
	require.Zero(t, campaign.Claimed)
	_, e = OmniGiftRedeem(campaign.Code, 20001, 1)
	require.NoError(t, e)
}
func TestOmniGiftConcurrencyNeverOverIssues(t *testing.T) {
	giftFixture(t)
	campaign, e := OmniGiftCreate(1, "gift-concurrent-request01", "限量五份", "0.1", 5, 0)
	require.NoError(t, e)
	for i := 1; i < 24; i++ {
		require.NoError(t, DB.Create(&User{Id: 20001 + i, Username: fmt.Sprintf("gift-%d", i), AffCode: fmt.Sprintf("ga-%d", i), Status: 1, AuthVersion: 1}).Error)
	}
	var winners atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(uid int) {
			defer wg.Done()
			if _, e := OmniGiftRedeem(campaign.Code, uid, 1); e == nil {
				winners.Add(1)
			}
		}(20001 + i)
	}
	wg.Wait()
	require.Equal(t, int64(5), winners.Load())
	var claims []OmniGiftClaim
	require.NoError(t, DB.Where("campaign_id = ?", campaign.ID).Find(&claims).Error)
	require.Len(t, claims, 5)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = OmniGiftRedeem(campaign.Code, claims[0].UserID, 1) }()
	}
	wg.Wait()
	var total int64
	require.NoError(t, DB.Model(&OmniLedger{}).Where("kind = ?", "gift").Select("COALESCE(SUM(quota),0)").Scan(&total).Error)
	require.Equal(t, int64(250000), total)
	require.NoError(t, DB.First(&campaign, "id = ?", campaign.ID).Error)
	require.Equal(t, 5, campaign.Claimed)
}
func TestOmniGiftBoundsExpiryAndRestrictions(t *testing.T) {
	giftFixture(t)
	for i, amount := range []string{"0", "-1", "1e9", "1e99999999", "0.000001", "0.0000001", "10001"} {
		_, e := OmniGiftCreate(1, fmt.Sprintf("invalid-amount-%020d", i), "错误额度", amount, 1, 0)
		require.Error(t, e)
	}
	campaign, e := OmniGiftCreate(1, "gift-expiry-request-0001", "限时", "0.000002", 1, time.Now().Unix()+100)
	require.NoError(t, e)
	_, e = OmniGiftRedeem(campaign.Code, 20001, 2)
	require.ErrorIs(t, e, ErrOmniRiskHold)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 20001).Update("status", common.UserStatusDisabled).Error)
	_, e = OmniGiftRedeem(campaign.Code, 20001, 1)
	require.ErrorIs(t, e, ErrOmniRiskHold)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 20001).Update("status", common.UserStatusEnabled).Error)
	require.NoError(t, DB.Model(&OmniGiftCampaign{}).Where("id = ?", campaign.ID).Update("expires_at", time.Now().Unix()-1).Error)
	_, e = OmniGiftRedeem(campaign.Code, 20001, 1)
	require.ErrorIs(t, e, ErrOmniGiftUnavailable)
	require.NoError(t, DB.Model(&OmniGiftCampaign{}).Where("id = ?", campaign.ID).Update("expires_at", 0).Error)
	require.NoError(t, DB.Create(&OmniRiskControl{UserID: 20001, State: "hold"}).Error)
	_, e = OmniGiftRedeem(campaign.Code, 20001, 1)
	require.ErrorIs(t, e, ErrOmniRiskHold)
}
