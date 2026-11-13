package model

import (
	"crypto/hmac"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"math/big"
	"strings"
	"time"
)

var ErrOmniEmailRequired = errors.New("请先验证邮箱；旧账号可自愿补绑，违规恢复还需人工审核")
var ErrOmniEmailLimit = errors.New("邮件发送限额已达到，请稍后重试；找回密码保留优先额度")

type OmniEmailPolicy struct {
	Enabled           bool  `json:"enabled"`
	MonthlyLimit      int64 `json:"monthly_limit"`
	DailyLimit        int64 `json:"daily_limit"`
	ResetReserve      int64 `json:"reset_reserve"`
	DeliveryAccepted  bool  `json:"delivery_accepted"`
	SenderDNSAccepted bool  `json:"sender_dns_accepted"`
}

func OmniEmailSettings() OmniEmailPolicy {
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap["OmniAPIEmailV3"]
	common.OptionMapRWMutex.RUnlock()
	p := OmniEmailPolicy{MonthlyLimit: 3000, DailyLimit: 100, ResetReserve: 20}
	_ = json.Unmarshal([]byte(raw), &p)
	// Hard ceiling: Resend free quota; never silently buy extra messages.
	if p.MonthlyLimit <= 0 || p.MonthlyLimit > 3000 {
		p.MonthlyLimit = 3000
	}
	if p.DailyLimit <= 0 || p.DailyLimit > 100 {
		p.DailyLimit = 100
	}
	if p.ResetReserve < 0 || p.ResetReserve >= p.DailyLimit {
		p.ResetReserve = 20
	}
	return p
}

type OmniEmailChallenge struct {
	Key         string `gorm:"primaryKey;size:64"`
	Purpose     string `gorm:"size:24;index"`
	Email       string `gorm:"size:254"`
	UserID      int    `gorm:"index"`
	AuthVersion int64
	Salt        string `gorm:"size:64"`
	CodeHash    string `gorm:"size:64"`
	Failed      int
	ExpiresAt   int64 `gorm:"index"`
	ResendAt    int64
	ConsumedAt  int64
}
type OmniEmailCounter struct {
	Key   string `gorm:"primaryKey;size:128"`
	Count int64
}

func OmniEmailMigrate(tx *gorm.DB) error {
	return tx.AutoMigrate(&OmniEmailChallenge{}, &OmniEmailCounter{})
}
func init() {
	common.EmailSendAdmission = func(email, purpose string) error {
		for _, recipient := range strings.Split(email, ";") {
			if err := OmniEmailAdmit(strings.TrimSpace(recipient), "", 0, purpose); err != nil {
				return err
			}
		}
		return nil
	}
}

func omniEmailInitialQuota(u *User) int {
	if u.EmailVerificationRequired && !OmniEmailVerified(*u) {
		u.PendingSignupGift = int64(common.QuotaForNewUser)
		u.PendingInviteeGift = int64(common.QuotaForInvitee)
		u.PendingInviterReward = u.InviterId > 0
		return 0
	}
	return common.QuotaForNewUser
}
func OmniEmailReleaseSignup(uid int) error {
	if !OmniEmailSettings().Enabled {
		return nil
	}
	var inviter int
	err := DB.Transaction(func(tx *gorm.DB) error {
		if _, err := omniAccount(tx, uid); err != nil {
			return err
		}
		var u User
		if err := lockForUpdate(tx).First(&u, uid).Error; err != nil {
			return err
		}
		if !u.EmailVerificationRequired || !OmniEmailVerified(u) {
			return nil
		}
		if err := OmniRiskAdmission(tx, uid); err != nil {
			return err
		}
		if u.Status != common.UserStatusEnabled {
			return ErrOmniRiskHold
		}
		gift := u.PendingSignupGift
		if u.InviterId > 0 && operation_setting.IsPaymentComplianceConfirmed() {
			gift += u.PendingInviteeGift
		}
		if u.PendingInviterReward && operation_setting.IsPaymentComplianceConfirmed() {
			inviter = u.InviterId
		}
		if gift > 0 {
			if err := tx.Model(&u).Updates(map[string]any{"quota": gorm.Expr("quota + ?", gift), "pending_signup_gift": 0, "pending_invitee_gift": 0}).Error; err != nil {
				return err
			}
			if err := tx.Create(&OmniLedger{Key: fmt.Sprintf("email-signup:%d", uid), UserID: uid, Kind: "registration:verified", Quota: gift, CreatedAt: time.Now().Unix()}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if inviter > 0 {
		if err = OmniRegistrationReward(inviter, uid); err != nil {
			return err
		}
		if err = DB.Model(&User{}).Where("id = ?", uid).Update("pending_inviter_reward", false).Error; err != nil {
			return err
		}
	}
	invalidateUserCache(uid)
	return nil
}
func emailChallengeKey(purpose, email string, uid int) string {
	return authFlowTokenHash(fmt.Sprintf("mail:%s:%d:%s", purpose, uid, NormalizeEmail(email)))
}
func emailChallengeHash(c OmniEmailChallenge, code string) string {
	return authFlowTokenHash(c.Key + ":" + c.Salt + ":" + code)
}
func emailSerial(tx *gorm.DB) error {
	row := OmniEmailCounter{Key: "serialization"}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return err
	}
	return tx.Model(&OmniEmailCounter{}).Where("key = ?", row.Key).UpdateColumn("count", gorm.Expr("count")).Error
}
func OmniEmailAbuseAdmit(email, ip string, uid int, purpose string) error {
	if !OmniEmailSettings().Enabled {
		return nil
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := emailSerial(tx); err != nil {
			return err
		}
		hour := time.Now().UTC().Format("2006-01-02-15")
		caps := map[string]int64{"source:" + authFlowTokenHash(ip) + ":" + hour: 15}
		if uid > 0 {
			caps[fmt.Sprintf("actor:%d:%s", uid, hour)] = 5
		}
		for key, limit := range caps {
			var row OmniEmailCounter
			if err := tx.Where("key = ?", key).Find(&row).Error; err != nil {
				return err
			}
			if row.Count >= limit {
				return ErrOmniEmailLimit
			}
		}
		for key := range caps {
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.Assignments(map[string]any{"count": gorm.Expr("omni_email_counters.count + 1")})}).Create(&OmniEmailCounter{Key: key, Count: 1}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// Account/email/IP ceilings and provider quota share one durable serialization
// lock. Failed SMTP attempts still count, since provider acceptance is unknown.
func OmniEmailAdmit(email, ip string, uid int, purpose string) error {
	p := OmniEmailSettings()
	if !p.Enabled {
		return nil
	}
	if !p.DeliveryAccepted || !p.SenderDNSAccepted {
		return errors.New("邮件服务尚未完成投递验收")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := emailSerial(tx); err != nil {
			return err
		}
		now := time.Now().In(time.FixedZone("CST", 28800))
		day := now.Format("2006-01-02")
		hour := now.Format("2006-01-02-15")
		caps := map[string]int64{"month:" + now.Format("2006-01"): p.MonthlyLimit, "day:" + day: p.DailyLimit, "email:" + authFlowTokenHash(NormalizeEmail(email)) + ":" + hour: 5}
		if purpose != "reset" {
			caps["month:"+now.Format("2006-01")] -= p.ResetReserve
			caps["day:"+day] -= p.ResetReserve
		}
		if ip != "" {
			caps["ip:"+authFlowTokenHash(ip)+":"+hour] = 15
		}
		if uid > 0 {
			caps[fmt.Sprintf("account:%d:%s", uid, hour)] = 5
		}
		for key, limit := range caps {
			var row OmniEmailCounter
			if err := tx.Where("key = ?", key).Find(&row).Error; err != nil {
				return err
			}
			if row.Count >= limit {
				return ErrOmniEmailLimit
			}
		}
		for key := range caps {
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.Assignments(map[string]any{"count": gorm.Expr("omni_email_counters.count + 1")})}).Create(&OmniEmailCounter{Key: key, Count: 1}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
func OmniEmailIssue(purpose, email string, uid int, code string) error {
	if purpose != "register" && purpose != "reset" && purpose != "risk_bind" {
		return errors.New("invalid email purpose")
	}
	email = NormalizeEmail(email)
	if email == "" || code == "" {
		return errors.New("invalid email challenge")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := emailSerial(tx); err != nil {
			return err
		}
		key := emailChallengeKey(purpose, email, uid)
		var c OmniEmailChallenge
		err := tx.First(&c, "key = ?", key).Error
		now := time.Now().Unix()
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if c.ResendAt > now {
			return ErrEmailBindingResendWait
		}
		expires, failed := now+600, 0
		if c.ConsumedAt == 0 && c.ExpiresAt > now {
			if c.Failed >= 5 {
				return ErrEmailBindingLocked
			}
			expires = c.ExpiresAt
			failed = c.Failed
		}
		c = OmniEmailChallenge{Key: key, Purpose: purpose, Email: email, UserID: uid, Salt: omniCommerceID(""), Failed: failed, ExpiresAt: expires, ResendAt: now + 60}
		if purpose == "reset" {
			var u User
			if err = tx.Select("id", "auth_version").First(&u, uid).Error; err != nil {
				return err
			}
			c.AuthVersion = u.AuthVersion
		}
		c.CodeHash = emailChallengeHash(c, code)
		return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&c).Error
	})
}
func omniEmailConsumeTx(tx *gorm.DB, purpose, email string, uid int, code string, consume bool) error {
	var c OmniEmailChallenge
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&c, "key = ?", emailChallengeKey(purpose, email, uid)).Error; err != nil {
		return ErrEmailBindingCodeInvalid
	}
	if c.ConsumedAt != 0 || c.ExpiresAt <= time.Now().Unix() {
		return ErrEmailBindingCodeInvalid
	}
	if c.Failed >= 5 {
		return ErrEmailBindingLocked
	}
	if purpose == "reset" {
		var u User
		if err := tx.Select("id", "auth_version").First(&u, uid).Error; err != nil {
			return err
		}
		if c.AuthVersion != u.AuthVersion {
			return ErrEmailBindingCodeInvalid
		}
	}
	if !hmac.Equal([]byte(c.CodeHash), []byte(emailChallengeHash(c, code))) {
		if err := tx.Model(&c).UpdateColumn("failed", gorm.Expr("failed + 1")).Error; err != nil {
			return err
		}
		return ErrEmailBindingCodeInvalid
	}
	if consume {
		return tx.Model(&c).Update("consumed_at", time.Now().Unix()).Error
	}
	return nil
}

// Commit failure counters even when returning an incorrect-code error.
func OmniEmailCheck(purpose, email string, uid int, code string) error {
	var validation error
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := emailSerial(tx); err != nil {
			return err
		}
		validation = omniEmailConsumeTx(tx, purpose, email, uid, code, false)
		if errors.Is(validation, ErrEmailBindingCodeInvalid) || errors.Is(validation, ErrEmailBindingLocked) {
			return nil
		}
		return validation
	})
	if err != nil {
		return err
	}
	return validation
}
func OmniEmailVerified(u User) bool {
	return u.EmailVerifiedAt > 0 && u.VerifiedEmail != "" && NormalizeEmail(u.Email) == u.VerifiedEmail
}
func OmniEmailConsumptionAllowed(tx *gorm.DB, uid int) error {
	if !OmniEmailSettings().Enabled && !OmniCommerceReady() {
		return nil
	}
	var u User
	if err := tx.Select("id", "email", "verified_email", "email_verified_at", "email_verification_required").First(&u, uid).Error; err != nil {
		return err
	}
	if u.EmailVerificationRequired && !OmniEmailVerified(u) {
		return ErrOmniEmailRequired
	}
	return nil
}
func omniEmailPrepareUser(tx *gorm.DB, u *User) error {
	if OmniEmailSettings().Enabled {
		u.EmailVerificationRequired = true
		u.EmailVerifiedAt = 0
		u.VerifiedEmail = ""
	}
	if u.VerificationCode != "" {
		if err := omniEmailConsumeTx(tx, "register", u.Email, 0, u.VerificationCode, true); err != nil {
			return err
		}
		u.VerifiedEmail = NormalizeEmail(u.Email)
		u.EmailVerifiedAt = time.Now().Unix()
	}
	return nil
}
func OmniEmailRiskBind(uid int, email, code string) error {
	if err := OmniEmailCheck("risk_bind", email, uid, code); err != nil {
		return err
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := lockNormalizedEmail(tx, email); err != nil {
			return err
		}
		var u User
		if err := lockForUpdate(tx).First(&u, uid).Error; err != nil {
			return err
		}
		if u.Email != "" && NormalizeEmail(u.Email) != NormalizeEmail(email) {
			return errors.New("已有邮箱需原邮箱验证或人工核验")
		}
		if err := ensureEmailAvailableWithTx(tx, email, uid); err != nil {
			return err
		}
		if err := omniEmailConsumeTx(tx, "risk_bind", email, uid, code, true); err != nil {
			return err
		}
		// Deliberately preserve status and the risk auth-version fence.
		return tx.Model(&u).Updates(map[string]any{"email": NormalizeEmail(email), "verified_email": NormalizeEmail(email), "email_verified_at": time.Now().Unix()}).Error
	})
}
func OmniEmailReset(email, token, password string, revokeKeys bool) error {
	if len(password) < 8 || len(password) > 128 {
		return errors.New("密码需为8至128字节")
	}
	u, err := GetUniqueUserByEmail(email)
	if err != nil {
		return ErrEmailBindingCodeInvalid
	}
	if !OmniEmailVerified(*u) {
		return ErrEmailBindingCodeInvalid
	}
	if u.Status != common.UserStatusEnabled {
		return errors.New("停用账号请先完成安全复核，不会通过找回密码自动恢复")
	}
	if err = OmniEmailCheck("reset", email, u.Id, token); err != nil {
		return err
	}
	hashed, err := common.HashAccountPassword(password)
	if err != nil {
		return err
	}
	err = DB.Transaction(func(tx *gorm.DB) error {
		if _, err := omniAccount(tx, u.Id); err != nil {
			return err
		}
		var current User
		if err := lockForUpdate(tx).First(&current, u.Id).Error; err != nil {
			return err
		}
		if current.Status != common.UserStatusEnabled || current.AuthVersion != u.AuthVersion || !OmniEmailVerified(current) {
			return ErrUserSessionInactive
		}
		if err := omniEmailConsumeTx(tx, "reset", email, u.Id, token, true); err != nil {
			return err
		}
		if _, err := IncrementUserAuthVersionWithTx(tx, u.Id); err != nil {
			return err
		}
		if err := tx.Model(&User{}).Where("id = ?", u.Id).Updates(map[string]any{"password": hashed, "access_token": nil, "access_token_created_at": nil}).Error; err != nil {
			return err
		}
		if err := tx.Model(&UserSession{}).Where("user_id = ? AND status = ?", u.Id, UserSessionStatusActive).Updates(map[string]any{"status": UserSessionStatusRevoked, "revoked_at": time.Now().Unix(), "revoked_reason": "password_reset"}).Error; err != nil {
			return err
		}
		if revokeKeys {
			var keys []Token
			if err := tx.Where("user_id = ?", u.Id).Find(&keys).Error; err != nil {
				return err
			}
			for _, key := range keys {
				if err := invalidateTokenCacheForMutation(key.Key); err != nil {
					return err
				}
			}
			if err := tx.Model(&Token{}).Where("user_id = ?", u.Id).Update("status", common.TokenStatusDisabled).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return PublishUserAuthCache(u.Id)
}

// Registration/risk codes are uniformly numeric; reset links use 256-bit
// opaque secrets. UUID prefixes and math/rand strings are not mail challenges.
func OmniEmailNewCode(link bool) (string, error) {
	if link {
		var b [32]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", err
		}
		return hex.EncodeToString(b[:]), nil
	}
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func OmniEmailCanEnable(tx *gorm.DB, u User) error {
	if !OmniEmailSettings().Enabled || u.Role != common.RoleCommonUser {
		return nil
	}
	if !OmniEmailVerified(u) {
		return ErrOmniEmailRequired
	}
	if OmniRiskV2Ready() {
		r, err := OmniRiskReadControl(tx, u.Id)
		if err != nil {
			return err
		}
		if riskV2Held(r.State) {
			return errors.New("请通过原安全复核流程恢复，不可直接修改启用状态")
		}
	}
	return nil
}
