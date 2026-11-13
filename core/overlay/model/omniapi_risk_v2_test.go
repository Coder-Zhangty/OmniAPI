package model

import (
	"context"
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func riskV2Fixture(t *testing.T) *gorm.DB {
	db := riskDB(t)
	require.NoError(t, db.AutoMigrate(&User{}, &Token{}, &UserSession{}, &OmniAccount{}, &OmniLedger{}, &Log{}, &AuditLog{}))
	return db
}

func TestRiskV2HoldRollbackIncludesNoticeAndKeys(t *testing.T) {
	db := riskV2Fixture(t)
	riskV2User(t, db, 2310)
	key := &Token{UserId: 2310, Key: "rollback-fixture", Status: common.TokenStatusEnabled}
	require.NoError(t, key.Insert())
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("risk_notice_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "omni_risk_notice_v2" {
			tx.AddError(errors.New("simulated notice storage failure"))
		}
	}))
	e := OmniRiskApplyV2(2310, "rollback-case", "registration_sequence", true, 0, "network")
	require.NoError(t, db.Callback().Create().Remove("risk_notice_failure"))
	require.Error(t, e)
	var u User
	require.NoError(t, db.First(&u, 2310).Error)
	require.Equal(t, common.UserStatusEnabled, u.Status)
	require.Equal(t, int64(1), u.AuthVersion)
	require.Equal(t, 371000, u.Quota)
	var token Token
	require.NoError(t, db.First(&token, key.Id).Error)
	require.Equal(t, common.TokenStatusEnabled, token.Status)
	r, e := OmniRiskReadControl(db, 2310)
	require.NoError(t, e)
	require.Zero(t, r.UserID)
	var cases int64
	require.NoError(t, db.Model(&OmniRiskCase{}).Where("user_id = ?", 2310).Count(&cases).Error)
	require.Zero(t, cases)
}

func TestRiskV2Batch88ReplayAndPropagationGate(t *testing.T) {
	db := riskV2Fixture(t)
	now := time.Now().Unix()
	require.NoError(t, db.Model(&OmniRiskPolicyV2{}).Where("id = 1").Updates(map[string]any{"auto_hold": true, "shadow": false}).Error)
	events := []OmniRiskEventV2{}
	for i := 0; i < 88; i++ {
		uid := 3000 + i
		riskV2User(t, db, uid)
		for j, kind := range []string{"registration.success", "login.success", "token.create", "token.view"} {
			events = append(events, OmniRiskEventV2{Key: fmt.Sprint(uid, ":", j), UserID: uid, Kind: kind, Network: "trusted-public-source", Browser: "exact-browser", CreatedAt: now - 45 + int64(j*10), Quota: 10})
		}
	}
	// Shared exits and normal fast onboarding do not establish abuse when any
	// required action or benefit evidence is missing.
	riskV2User(t, db, 3131)
	events = append(events, OmniRiskEventV2{Key: "131-counterexample", UserID: 3131, Kind: "registration.success", Network: "trusted-public-source", Browser: "exact-browser", CreatedAt: now - 45, Quota: 10})
	require.NoError(t, db.Create(&events).Error)
	started := time.Now()
	require.NoError(t, OmniRiskV2Burst(3087))
	duration := time.Since(started)
	var held int64
	require.NoError(t, db.Model(&OmniRiskControl{}).Where("state = ?", "hold").Count(&held).Error)
	require.Equal(t, int64(88), held)
	var untouched User
	require.NoError(t, db.First(&untouched, 3131).Error)
	require.Equal(t, common.UserStatusEnabled, untouched.Status)
	var total int64
	require.NoError(t, db.Model(&User{}).Select("SUM(quota)").Scan(&total).Error)
	require.Equal(t, int64(89*371000), total)
	t.Logf("88-account propagation including 250ms event worker upper bound: %s", duration+250*time.Millisecond)
	if os.Getenv("OMNI_RISK_ENFORCEMENT_GATE") == "1" {
		require.Less(t, duration+250*time.Millisecond, 2*time.Second, "automatic enforcement must remain disabled if propagation gate fails")
	}
}
func riskV2User(t *testing.T, db *gorm.DB, id int) *User {
	u := &User{Id: id, Username: fmt.Sprint("risk-fixture-", id), AffCode: fmt.Sprint("rf", id), Role: common.RoleCommonUser, Status: common.UserStatusEnabled, AuthVersion: 1, Quota: 371000, CreatedAt: time.Now().Unix()}
	require.NoError(t, db.Create(u).Error)
	return u
}
func TestRiskV2BurstCounterexamples(t *testing.T) {
	m := OmniRiskBurstMember{UserID: 1, RegisteredAt: 100, Gift: true, LoginAt: 105, CreateAt: 120, ViewAt: 130, Network: "network", Browser: "browser"}
	require.True(t, OmniRiskBurstMatch(m))
	m.CreateAt = 220
	m.ViewAt = 240
	require.False(t, OmniRiskBurstMatch(m), "restored account131 old onboarding must not match")
	m.CreateAt = 120
	m.ViewAt = 130
	m.Network = ""
	require.False(t, OmniRiskBurstMatch(m))
	require.Equal(t, "", OmniRiskCanonicalIP("172.30.0.2"))
	require.Equal(t, "1.1.1.1", OmniRiskCanonicalIP("::ffff:1.1.1.1"))
}
func TestRiskV2AtomicBudgetAndUnknown(t *testing.T) {
	db := riskV2Fixture(t)
	riskV2User(t, db, 2001)
	require.NoError(t, db.Model(&OmniRiskPolicyV2{}).Where("id = 1").Updates(map[string]any{"shadow": false, "new_user_protection": true}).Error)
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, e := OmniRiskReserveV2(2001, fmt.Sprint("r", i), 14, 600000, `{"input":"1","output":"2","fx":"7"}`)
			if e == nil && r != nil {
				wins.Add(1)
			}
		}(i)
	}
	wg.Wait()
	require.Equal(t, int32(1), wins.Load())
	var held OmniRiskExposure
	require.NoError(t, db.First(&held).Error)
	require.NoError(t, OmniRiskFinishV2(held.ID, 0, false, false))
	_, e := OmniRiskReserveV2(2001, "next", 14, 500000, "quote")
	require.ErrorIs(t, e, ErrOmniRiskLimit)
	var u User
	db.First(&u, 2001)
	require.Equal(t, 371000, u.Quota)
	r, e := OmniRiskReadControl(db, 2001)
	require.NoError(t, e)
	require.Equal(t, "budget", r.State)
	require.NoError(t, OmniRiskFinishV2(held.ID, 100000, true, false))
	require.NoError(t, OmniRiskFinishV2(held.ID, 0, false, true))
	db.First(&held, "id = ?", held.ID)
	require.Equal(t, int64(100000), held.Actual)
	require.Equal(t, "settled", held.State)
}
func TestRiskV2PaidAndRestoreKeepMoney(t *testing.T) {
	db := riskV2Fixture(t)
	riskV2User(t, db, 2002)
	require.NoError(t, db.Create(&OmniLedger{Key: "historical-paid", UserID: 2002, Kind: "topup", Cents: 100, Refunded: true}).Error)
	require.NoError(t, OmniRiskApplyV2(2002, "episode-a", "registration_sequence", true, 0, "network"))
	r, e := OmniRiskReadControl(db, 2002)
	require.NoError(t, e)
	require.Equal(t, "guard", r.State)
	var u User
	db.First(&u, 2002)
	require.Equal(t, common.UserStatusEnabled, u.Status)
	riskV2User(t, db, 2003)
	require.NoError(t, OmniRiskApplyV2(2003, "episode-b", "registration_sequence", true, 0, "network"))
	r, e = OmniRiskReadControl(db, 2003)
	require.NoError(t, e)
	require.Equal(t, "hold", r.State)
	u = User{}
	db.First(&u, 2003)
	require.Equal(t, 371000, u.Quota)
	require.Equal(t, common.UserStatusDisabled, u.Status)
	require.NoError(t, OmniRiskRestoreV2(2003, 1, r.Version, "confirmed false positive", false))
	require.NoError(t, OmniRiskApplyV2(2003, "episode-b", "registration_sequence", true, 0, "network"))
	r, e = OmniRiskReadControl(db, 2003)
	require.NoError(t, e)
	require.Equal(t, "restored", r.State)
	var events int64
	db.Model(&OmniRiskNoticeV2{}).Where("user_id = ?", 2003).Count(&events)
	require.Equal(t, int64(2), events)
}
func TestRiskV2IPExpiryAndPaidException(t *testing.T) {
	db := riskV2Fixture(t)
	require.NoError(t, db.Create(&OmniRiskIP{Tag: "temp", Source: "automatic", ExpiresAt: time.Now().Unix() + 900}).Error)
	require.ErrorIs(t, OmniRiskIPCheck(db, "temp", false), ErrOmniRiskHold)
	require.NoError(t, OmniRiskIPCheck(db, "temp", true))
	require.NoError(t, db.Model(&OmniRiskIP{}).Where("tag = ?", "temp").Update("expires_at", time.Now().Unix()-1).Error)
	require.NoError(t, OmniRiskIPCheck(db, "temp", false))
	require.NoError(t, db.Create(&OmniRiskIP{Tag: "manual", Source: "manual"}).Error)
	require.ErrorIs(t, OmniRiskIPCheck(db, "manual", true), ErrOmniRiskHold)
}
func TestRiskV2CancelIsScoped(t *testing.T) {
	a, ca := context.WithCancel(context.Background())
	b, cb := context.WithCancel(context.Background())
	defer cb()
	da := OmniRiskTrack(1, "a", ca)
	defer da()
	db := OmniRiskTrack(2, "b", cb)
	defer db()
	OmniRiskCancel(1)
	require.Error(t, a.Err())
	require.NoError(t, b.Err())
}

func TestRiskV2LatestSignalsSurviveHighVolume(t *testing.T) {
	db := riskV2Fixture(t)
	riskV2User(t, db, 2400)
	require.NoError(t, db.Create(&OmniLedger{Key: "paid-high-volume", UserID: 2400, Kind: "topup", Cents: 100}).Error)
	require.NoError(t, db.Model(&OmniRiskPolicyV2{}).Where("id = 1").Updates(map[string]any{"shadow": false, "auto_hold": true}).Error)
	now := time.Now().Unix()
	events := []OmniRiskEventV2{}
	for i := 0; i < 2100; i++ {
		events = append(events, OmniRiskEventV2{Key: fmt.Sprint("old-", i), UserID: 2400, Kind: "model.admission", CreatedAt: now - 550, Network: "old-source", TokenID: 1})
	}
	events = append(events, OmniRiskEventV2{Key: "recent-key-view", UserID: 2400, Kind: "token.view", CreatedAt: now - 30})
	for i := 0; i < 6; i++ {
		events = append(events, OmniRiskEventV2{Key: fmt.Sprint("fresh-", i), UserID: 2400, Kind: "model.admission", CreatedAt: now - 10 + int64(i), Network: fmt.Sprint("new-source-", i/3), TokenID: 1})
	}
	require.NoError(t, db.CreateInBatches(events, 100).Error)
	require.NoError(t, OmniRiskConsumptionSignalsV2(2400))
	r, e := OmniRiskReadControl(db, 2400)
	require.NoError(t, e)
	require.Equal(t, "guard", r.State, "old event volume must not conceal current combined evidence")
	var user User
	require.NoError(t, db.First(&user, 2400).Error)
	require.Equal(t, common.UserStatusEnabled, user.Status, "paid accounts are not automatically suspended")
}

func TestRiskV2OldEvidenceNeverReapplied(t *testing.T) {
	db := riskV2Fixture(t)
	riskV2User(t, db, 2301)
	for _, fp := range []string{"evidence-A", "evidence-B"} {
		require.NoError(t, OmniRiskApplyV2(2301, fp, "registration_sequence", true, 0, "network"))
		r, e := OmniRiskReadControl(db, 2301)
		require.NoError(t, e)
		require.Equal(t, "hold", r.State)
		require.NoError(t, OmniRiskRestoreV2(2301, 1, r.Version, "误报", false))
	}
	require.NoError(t, OmniRiskApplyV2(2301, "evidence-A", "registration_sequence", true, 0, "network"))
	r, e := OmniRiskReadControl(db, 2301)
	require.NoError(t, e)
	require.Equal(t, "restored", r.State)
	require.Equal(t, "evidence-B", r.Fingerprint)
	var n int64
	require.NoError(t, db.Model(&OmniRiskCase{}).Where("user_id = ?", 2301).Count(&n).Error)
	require.Equal(t, int64(2), n)
}
func TestRiskV2LateKeyAndIndependentDisable(t *testing.T) {
	db := riskV2Fixture(t)
	riskV2User(t, db, 2302)
	key := &Token{UserId: 2302, Key: "risk-v2-fixture-key", Status: common.TokenStatusEnabled}
	require.NoError(t, key.Insert())
	require.NoError(t, OmniRiskApplyV2(2302, "key-race", "registration_sequence", true, 0, "network"))
	late := &Token{UserId: 2302, Key: "risk-v2-fixture-late", Status: common.TokenStatusEnabled}
	require.ErrorIs(t, late.Insert(), ErrOmniRiskHold)
	key.Status = common.TokenStatusDisabled
	require.NoError(t, key.SelectUpdate())
	r, e := OmniRiskReadControl(db, 2302)
	require.NoError(t, e)
	require.Error(t, OmniRiskRestoreV2(2302, 1, r.Version, "other administrator disabled key", false))
	var current Token
	require.NoError(t, db.First(&current, key.Id).Error)
	require.Equal(t, common.TokenStatusDisabled, current.Status)
	var user User
	require.NoError(t, db.First(&user, 2302).Error)
	require.Equal(t, common.UserStatusDisabled, user.Status)
}

func TestRiskV2RestorationDoesNotAuthorizeOldKeyMutation(t *testing.T) {
	db := riskV2Fixture(t)
	riskV2User(t, db, 2402)
	require.NoError(t, OmniRiskApplyV2(2402, "old-auth-fence", "registration_sequence", true, 0, "network"))
	r, e := OmniRiskReadControl(db, 2402)
	require.NoError(t, e)
	require.NoError(t, OmniRiskRestoreV2(2402, 1, r.Version, "误报恢复", false))
	old := &Token{UserId: 2402, Key: "stale-session-key", Status: common.TokenStatusEnabled, RiskExpectedAuthVersion: 1}
	require.ErrorIs(t, old.Insert(), ErrOmniRiskHold)
	var u User
	require.NoError(t, db.First(&u, 2402).Error)
	fresh := &Token{UserId: 2402, Key: "fresh-session-key", Status: common.TokenStatusEnabled, RiskExpectedAuthVersion: u.AuthVersion}
	require.NoError(t, fresh.Insert())
	_, e = BatchDeleteTokens([]int{fresh.Id}, 2402, 1)
	require.ErrorIs(t, e, ErrOmniRiskHold)
	require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error {
		if _, e := omniAccount(tx, 2402); e != nil {
			return e
		}
		return OmniRiskBusinessMutation(tx, 2402, 1)
	}), ErrOmniRiskHold)
	t.Setenv("OMNI_RISK_ACTIVATION_GATE", "")
	require.Error(t, OmniRiskActivationReady())
}

func TestRiskV2RepeatedFailureSchedulingIsIdempotent(t *testing.T) {
	db := riskV2Fixture(t)
	require.NoError(t, OmniRiskScheduleV2(1, "burst"))
	require.NoError(t, OmniRiskScheduleV2(1, "burst"))
	var rows []OmniRiskRetryV2
	require.NoError(t, db.Find(&rows).Error)
	require.Len(t, rows, 1)
	require.LessOrEqual(t, rows[0].NextAt, time.Now().Unix()+5)
}

func TestRiskV2PaymentAndHoldRaceKeepsSingleCredit(t *testing.T) {
	db := riskV2Fixture(t)
	riskV2User(t, db, 91)
	require.NoError(t, db.AutoMigrate(&OmniTopup{}, &OmniNotice{}))
	common.OptionMapRWMutex.Lock()
	prior := common.OptionMap["OmniAPICommercial"]
	if common.OptionMap == nil {
		common.OptionMap = map[string]string{}
	}
	common.OptionMap["OmniAPICommercial"] = `{"membership_enabled":false}`
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap["OmniAPICommercial"] = prior
		common.OptionMapRWMutex.Unlock()
	})
	o := topupFixture(t, "xp_risk_race", 100)
	var wg sync.WaitGroup
	failures := make(chan error, 5)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := OmniFulfillTopup(o.ID, "123", "risk_race_receipt", 100, true)
			failures <- e
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		failures <- OmniRiskApplyV2(91, "risk-credit-race", "registration_sequence", true, 0, "network")
	}()
	wg.Wait()
	close(failures)
	for e := range failures {
		require.NoError(t, e)
	}
	var ledgerCount int64
	require.NoError(t, db.Model(&OmniLedger{}).Where("key = ?", "xorpay:123:"+o.ID).Count(&ledgerCount).Error)
	require.Equal(t, int64(1), ledgerCount)
	var user User
	require.NoError(t, db.First(&user, 91).Error)
	require.Equal(t, 471000, user.Quota)
	r, e := OmniRiskReadControl(db, 91)
	require.NoError(t, e)
	require.Contains(t, []string{"hold", "guard"}, r.State)
	if r.State == "hold" {
		require.Equal(t, common.UserStatusDisabled, user.Status)
	} else {
		require.Equal(t, common.UserStatusEnabled, user.Status)
	}
}

func TestRiskV2BrowserNormalizationMatchesAuthProxy(t *testing.T) {
	key := strings.Repeat("k", 40)
	ua := strings.Repeat("a", 2048)
	require.Equal(t, OmniRiskTag(key, "browser", ua), OmniRiskTag(key, "browser", ua+"suffix"))
}

func TestRiskV2MissingSourceConfigurationCannotBypassLiveRules(t *testing.T) {
	db := riskV2Fixture(t)
	require.NoError(t, OmniRiskIPCheck(db, "risk_source_unavailable", false))
	require.NoError(t, db.Create(&OmniRiskIP{Tag: "live-manual", Source: "manual"}).Error)
	require.Error(t, OmniRiskIPCheck(db, "risk_source_unavailable", true))
	require.NoError(t, db.Model(&OmniRiskIP{}).Where("tag = ?", "live-manual").Update("revoked_at", time.Now().Unix()).Error)
	require.NoError(t, db.Model(&OmniRiskPolicyV2{}).Where("id = 1").Updates(map[string]any{"shadow": false, "auto_hold": true}).Error)
	require.Error(t, OmniRiskIPCheck(db, "risk_source_unavailable", true))
}
