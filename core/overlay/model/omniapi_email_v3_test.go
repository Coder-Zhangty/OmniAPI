package model

import (
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"sync"
	"testing"
	"time"
)

func emailV3Fixture(t *testing.T) {
	t.Helper()
	commerceFixture(t)
	commerceOption(t, "OmniAPIEmailV3", OmniEmailPolicy{Enabled: true, DailyLimit: 100, MonthlyLimit: 3000, ResetReserve: 20, DeliveryAccepted: true, SenderDNSAccepted: true})
}
func TestOmniCommerceEmailDurableAttemptsResendAndExpiry(t *testing.T) {
	emailV3Fixture(t)
	email := "valid+alias.name@example.com"
	require.NoError(t, OmniEmailIssue("register", email, 0, "123456"))
	require.ErrorIs(t, OmniEmailIssue("register", email, 0, "654321"), ErrEmailBindingResendWait)
	key := emailChallengeKey("register", email, 0)
	var before OmniEmailChallenge
	require.NoError(t, DB.First(&before, "key = ?", key).Error)
	require.NotEqual(t, "123456", before.CodeHash)
	for i := 0; i < 2; i++ {
		require.ErrorIs(t, OmniEmailCheck("register", email, 0, "000000"), ErrEmailBindingCodeInvalid)
	}
	require.NoError(t, DB.Model(&OmniEmailChallenge{}).Where("key = ?", key).Update("resend_at", 0).Error)
	require.NoError(t, OmniEmailIssue("register", email, 0, "654321"))
	var after OmniEmailChallenge
	require.NoError(t, DB.First(&after, "key = ?", key).Error)
	require.Equal(t, before.ExpiresAt, after.ExpiresAt)
	require.Equal(t, 2, after.Failed)
	for i := 0; i < 3; i++ {
		require.ErrorIs(t, OmniEmailCheck("register", email, 0, "000000"), ErrEmailBindingCodeInvalid)
	}
	require.ErrorIs(t, OmniEmailCheck("register", email, 0, "654321"), ErrEmailBindingLocked)
	require.NoError(t, DB.Model(&OmniEmailChallenge{}).Where("key = ?", key).Updates(map[string]any{"expires_at": time.Now().Unix() - 1, "resend_at": 0}).Error)
	require.Error(t, OmniEmailCheck("register", email, 0, "654321"))
	require.NoError(t, OmniEmailIssue("register", email, 0, "111111"))
	require.NoError(t, OmniEmailCheck("register", email, 0, "111111"))
}
func TestOmniCommerceEmailNewLegacyAndOneTimeSignup(t *testing.T) {
	emailV3Fixture(t)
	oldGift := common.QuotaForNewUser
	common.QuotaForNewUser = 371000
	t.Cleanup(func() { common.QuotaForNewUser = oldGift })
	require.NoError(t, OmniEmailConsumptionAllowed(DB, 91), "old account remains exempt")
	email := "register+tag@example.com"
	require.NoError(t, OmniEmailIssue("register", email, 0, "123456"))
	u := User{Username: "new-verified", Password: "test-password-123", Email: email, VerificationCode: "123456", Status: 1, Role: 1}
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error { return u.InsertWithTx(tx, 0) }))
	require.True(t, u.EmailVerificationRequired)
	require.True(t, OmniEmailVerified(u))
	require.Equal(t, 371000, u.Quota)
	require.NoError(t, OmniEmailConsumptionAllowed(DB, u.Id))
	require.Error(t, DB.Transaction(func(tx *gorm.DB) error { return omniEmailConsumeTx(tx, "register", email, 0, "123456", true) }))
	oauthUser := User{Username: "new-unverified", Email: "unproven@example.com", VerifiedEmail: "unproven@example.com", EmailVerifiedAt: time.Now().Unix(), Status: 1, Role: 1}
	require.NoError(t, DB.Transaction(func(tx *gorm.DB) error { return oauthUser.InsertWithTx(tx, 0) }))
	require.False(t, OmniEmailVerified(oauthUser))
	require.Zero(t, oauthUser.Quota)
	require.EqualValues(t, 371000, oauthUser.PendingSignupGift)
	require.ErrorIs(t, OmniEmailConsumptionAllowed(DB, oauthUser.Id), ErrOmniEmailRequired)
	require.NoError(t, DB.Model(&oauthUser).Updates(map[string]any{"verified_email": oauthUser.Email, "email_verified_at": time.Now().Unix()}).Error)
	var wg sync.WaitGroup
	errs := make(chan error, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- OmniEmailReleaseSignup(oauthUser.Id) }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
	require.NoError(t, DB.First(&oauthUser, oauthUser.Id).Error)
	require.Equal(t, 371000, oauthUser.Quota)
	// Legacy binding never creates a pending signup grant.
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 91).Updates(map[string]any{"email": "old@example.com", "verified_email": "old@example.com", "email_verified_at": time.Now().Unix()}).Error)
	var old User
	require.NoError(t, DB.First(&old, 91).Error)
	q := old.Quota
	require.NoError(t, OmniEmailReleaseSignup(91))
	require.NoError(t, DB.First(&old, 91).Error)
	require.Equal(t, q, old.Quota)
}
func TestOmniCommerceEmailProviderBudgetPrioritizesReset(t *testing.T) {
	emailV3Fixture(t)
	commerceOption(t, "OmniAPIEmailV3", OmniEmailPolicy{Enabled: true, DailyLimit: 4, MonthlyLimit: 10, ResetReserve: 2, DeliveryAccepted: true, SenderDNSAccepted: true})
	for i := 0; i < 2; i++ {
		require.NoError(t, OmniEmailAdmit(fmt.Sprintf("person%d@example.com", i), "", 0, "register"))
	}
	require.ErrorIs(t, OmniEmailAdmit("third@example.com", "", 0, "register"), ErrOmniEmailLimit)
	for i := 0; i < 2; i++ {
		require.NoError(t, OmniEmailAdmit(fmt.Sprintf("reset%d@example.com", i), "", 0, "reset"))
	}
	require.ErrorIs(t, OmniEmailAdmit("extra@example.com", "", 0, "reset"), ErrOmniEmailLimit)
}
func TestOmniCommerceEmailResetRevokesCredentialsAndOptionalKeys(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(fmt.Sprint(revoke), func(t *testing.T) {
			emailV3Fixture(t)
			email := "verified@example.com"
			pat := "old-dashboard-token-123456789012"
			require.NoError(t, DB.Model(&User{}).Where("id = ?", 91).Updates(map[string]any{"email": email, "verified_email": email, "email_verified_at": time.Now().Unix(), "access_token": pat, "auth_version": 1}).Error)
			session := newTestUserSession("password-reset-session", 91, time.Now().Unix())
			require.NoError(t, DB.Create(session).Error)
			key := Token{UserId: 91, Key: "preserve-or-revoke-model-key", Status: 1, ExpiredTime: -1, UnlimitedQuota: true}
			require.NoError(t, DB.Create(&key).Error)
			require.NoError(t, OmniEmailIssue("reset", email, 91, "opaque-reset-token"))
			require.NoError(t, OmniEmailReset(email, "opaque-reset-token", "a-new-password-123", revoke))
			require.Error(t, OmniEmailReset(email, "opaque-reset-token", "second-password-123", revoke))
			var u User
			require.NoError(t, DB.First(&u, 91).Error)
			require.EqualValues(t, 2, u.AuthVersion)
			require.Nil(t, u.AccessToken)
			require.True(t, common.ValidatePasswordAndHash("a-new-password-123", u.Password))
			require.NoError(t, DB.First(session, "sid = ?", session.SID).Error)
			require.Equal(t, UserSessionStatusRevoked, session.Status)
			require.NoError(t, DB.First(&key, key.Id).Error)
			if revoke {
				require.Equal(t, common.TokenStatusDisabled, key.Status)
			} else {
				require.Equal(t, common.TokenStatusEnabled, key.Status)
			}
		})
	}
}
func TestOmniCommerceRiskEmailVerificationPreservesHold(t *testing.T) {
	emailV3Fixture(t)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 91).Updates(map[string]any{"status": 2, "auth_version": 7}).Error)
	require.NoError(t, OmniEmailIssue("risk_bind", "appeal@example.com", 91, "123456"))
	require.NoError(t, OmniEmailRiskBind(91, "appeal@example.com", "123456"))
	var u User
	require.NoError(t, DB.First(&u, 91).Error)
	require.Equal(t, 2, u.Status)
	require.EqualValues(t, 7, u.AuthVersion)
	require.True(t, OmniEmailVerified(u))
	require.Error(t, OmniEmailRiskBind(91, "appeal@example.com", "123456"))
	require.Error(t, OmniEmailReset(u.Email, "anything", "a-new-password-123", false))
}

func TestOmniCommerceEmailPasswordChangeInvalidatesOldResetLink(t *testing.T) {
	emailV3Fixture(t)
	email := "change-bound@example.com"
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 91).Updates(map[string]any{"email": email, "verified_email": email, "email_verified_at": time.Now().Unix()}).Error)
	require.NoError(t, OmniEmailIssue("reset", email, 91, "old-password-reset-token"))
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 91).UpdateColumn("auth_version", gorm.Expr("auth_version + 1")).Error)
	require.Error(t, OmniEmailReset(email, "old-password-reset-token", "chosen-new-password", false))
}
