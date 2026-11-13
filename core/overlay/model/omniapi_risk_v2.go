package model

// Deterministic, reversible risk controls. No customer balance is changed here.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"
)

type OmniRiskPolicyV2 struct {
	ID                int   `json:"id" gorm:"primaryKey"`
	Version           int64 `json:"version"`
	AutoHold          bool  `json:"auto_hold"`
	NewUserProtection bool  `json:"new_user_protection"`
	TemporaryIP       bool  `json:"temporary_ip"`
	Shadow            bool  `json:"shadow"`
	NewBudgetMicros   int64 `json:"new_budget_micros"`
	UpdatedAt         int64 `json:"updated_at"`
}
type OmniRiskControl struct {
	UserID          int    `json:"user_id" gorm:"primaryKey"`
	Version         int64  `json:"version"`
	State           string `json:"state" gorm:"index"` // hold,guard,budget,restored,confirmed
	CaseID          string `json:"case_id" gorm:"index"`
	Fingerprint     string `json:"fingerprint"`
	Reason          string `json:"reason"`
	Network         string `json:"network"`
	BeforeStatus    int    `json:"-"`
	HeldAuthVersion int64  `json:"-"`
	TokenIDs        string `json:"-"`
	StartedAt       int64  `json:"started_at" gorm:"index"`
	ReviewedAt      int64  `json:"reviewed_at"`
	BudgetMicros    int64  `json:"budget_micros"`
	Concurrency     int    `json:"concurrency"`
	Note            string `json:"note"`
}
type OmniRiskIP struct {
	Version   int64  `json:"version"`
	Tag       string `json:"tag" gorm:"primaryKey;size:64"`
	Source    string `json:"source"`
	CaseID    string `json:"case_id"`
	ExpiresAt int64  `json:"expires_at" gorm:"index"` // 0: manual indefinite
	CreatedAt int64  `json:"created_at"`
	RevokedAt int64  `json:"revoked_at"`
}
type OmniRiskEventV2 struct {
	ID        uint64 `json:"id" gorm:"primaryKey"`
	Key       string `json:"-" gorm:"uniqueIndex;size:128"`
	UserID    int    `json:"user_id" gorm:"index:idx_risk_v2_user_time,priority:1"`
	Kind      string `json:"kind"`
	Network   string `json:"network" gorm:"index:idx_risk_v2_network_time,priority:1"`
	Browser   string `json:"browser"`
	CreatedAt int64  `json:"created_at" gorm:"index:idx_risk_v2_user_time,priority:2;index:idx_risk_v2_network_time,priority:2"`
	Quota     int64  `json:"quota"`
	TokenID   int    `json:"token_id"`
}
type OmniRiskExposure struct {
	ID         string `json:"id" gorm:"primaryKey;size:100"`
	UserID     int    `json:"user_id" gorm:"index:idx_risk_exposure_user_time,priority:1"`
	RequestID  string `json:"request_id" gorm:"index"`
	ChannelID  int    `json:"channel_id"`
	Reserved   int64  `json:"reserved_micros"`
	Actual     int64  `json:"actual_micros"`
	State      string `json:"state" gorm:"index"` // reserved,sending,unknown,settled,not_sent
	QuoteHash  string `json:"quote_hash"`
	CostQuote  string `json:"-" gorm:"type:text"`
	Ordinal    int    `json:"ordinal"`
	Owner      string `json:"-"`
	CreatedAt  int64  `json:"created_at" gorm:"index:idx_risk_exposure_user_time,priority:2"`
	FinishedAt int64  `json:"finished_at"`
}
type OmniRiskActionV2 struct {
	ID        string `json:"id" gorm:"primaryKey;size:100"`
	UserID    int    `json:"user_id" gorm:"index"`
	CaseID    string `json:"case_id" gorm:"index"`
	ActorID   int    `json:"actor_id"`
	Action    string `json:"action"`
	Note      string `json:"note"`
	CreatedAt int64  `json:"created_at"`
}
type OmniRiskNoticeV2 struct {
	LegacyRead     bool   `json:"legacy_read"`
	ID             string `json:"id" gorm:"primaryKey;size:100"`
	UserID         int    `json:"-" gorm:"index"`
	CaseID         string `json:"case_id"`
	Title          string `json:"title"`
	Body           string `json:"body"`
	CreatedAt      int64  `json:"created_at"`
	DisplayedAt    int64  `json:"displayed_at"`
	AcknowledgedAt int64  `json:"acknowledged_at"`
}
type OmniRiskAppeal struct {
	ReviewedBy int    `json:"reviewed_by"`
	ReviewedAt int64  `json:"reviewed_at"`
	Decision   string `json:"decision"`
	ID         string `json:"id" gorm:"primaryKey;size:100"`
	UserID     int    `json:"user_id" gorm:"index"`
	CaseID     string `json:"case_id"`
	Body       string `json:"body"`
	CreatedAt  int64  `json:"created_at"`
}
type OmniRiskRewardHold struct {
	Invitee     int    `json:"invitee" gorm:"primaryKey"`
	Inviter     int    `json:"inviter"`
	Quota       int64  `json:"quota"`
	State       string `json:"state"` // pending,released,cancelled
	CaseID      string `json:"case_id"`
	CreatedAt   int64  `json:"created_at"`
	FundingMode string `json:"funding_mode"`
	AppliedAt   int64  `json:"applied_at"`
}
type OmniRiskFundingEvidence struct {
	UserID         int    `json:"user_id" gorm:"primaryKey"`
	SourceHash     string `json:"source_hash"`
	Classification string `json:"classification"`
	CreatedAt      int64  `json:"created_at"`
}
type OmniRiskCursorV2 struct {
	Key       string `gorm:"primaryKey"`
	Value     int64
	Text      string
	UpdatedAt int64
}
type OmniRiskRetryV2 struct {
	Key       string `gorm:"primaryKey;size:100" json:"key"`
	UserID    int    `json:"user_id"`
	Kind      string `json:"kind"`
	NextAt    int64  `gorm:"index" json:"next_at"`
	Attempts  int    `json:"attempts"`
	ErrorCode string `json:"error_code"`
	UpdatedAt int64  `json:"updated_at"`
}

func OmniRiskScheduleV2(uid int, kind string) error {
	now := time.Now().Unix()
	r := OmniRiskRetryV2{Key: fmt.Sprint(kind, ":", uid), UserID: uid, Kind: kind, NextAt: now + 5, UpdatedAt: now, ErrorCode: "detector_failed"}
	return DB.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.Assignments(map[string]any{"updated_at": now, "error_code": "detector_failed", "next_at": gorm.Expr("CASE WHEN omni_risk_retry_v2.next_at < ? THEN omni_risk_retry_v2.next_at ELSE ? END", now+5, now+5)})}).Create(&r).Error
}

type OmniRiskTokenFence struct {
	TokenID   int    `gorm:"primaryKey"`
	CaseID    string `gorm:"index"`
	TouchedAt int64
}

var ErrOmniRiskHold = errors.New("risk_review_required")
var ErrOmniRiskLimit = errors.New("risk_protection_limit")
var ErrOmniRiskQuote = errors.New("risk_quote_unavailable")
var riskV2Ready bool
var riskV2DB *gorm.DB
var riskV2ReadyMu sync.RWMutex
var RiskV2AfterControl = func(int) {}
var OmniRiskProcessID = uuid.NewString()

func OmniRiskV2Migrate(db *gorm.DB) error {
	if err := db.AutoMigrate(&OmniRiskRetryV2{}, &OmniRiskPolicyV2{}, &OmniRiskControl{}, &OmniRiskIP{}, &OmniRiskEventV2{}, &OmniRiskExposure{}, &OmniRiskActionV2{}, &OmniRiskNoticeV2{}, &OmniRiskAppeal{}, &OmniRiskRewardHold{}, &OmniRiskFundingEvidence{}, &OmniRiskCursorV2{}, &OmniRiskTokenFence{}); err != nil {
		return err
	}
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&OmniRiskPolicyV2{ID: 1, Version: 1, Shadow: true, NewBudgetMicros: 1000000}).Error; err != nil {
		return err
	}
	riskV2ReadyMu.Lock()
	riskV2Ready = true
	riskV2DB = db
	riskV2ReadyMu.Unlock()
	return nil
}
func OmniRiskV2Ready() bool {
	riskV2ReadyMu.RLock()
	defer riskV2ReadyMu.RUnlock()
	return riskV2Ready && DB == riskV2DB
}
func OmniRiskV2Policy() (OmniRiskPolicyV2, error) {
	var p OmniRiskPolicyV2
	if !OmniRiskV2Ready() {
		return p, nil
	}
	err := DB.First(&p, 1).Error
	return p, err
}
func OmniRiskCanonicalIP(raw string) string {
	ip, e := netip.ParseAddr(strings.TrimSpace(raw))
	if e != nil {
		return ""
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return ""
	}
	return ip.String()
}
func OmniRiskNetworkTag(raw string) string {
	ip := OmniRiskCanonicalIP(raw)
	if ip == "" {
		return ""
	}
	s, e := LoadOmniRiskSecrets()
	if e != nil {
		return "risk_source_unavailable"
	}
	return OmniRiskTag(s.HMACKey, "network", ip)
}
func OmniRiskIPCheck(tx *gorm.DB, tag string, paid bool) error {
	if !OmniRiskV2Ready() {
		return nil
	}
	if tag == "risk_source_unavailable" {
		// An unconfigured, entirely inactive module must not break the existing
		// gateway. Any live strategy or existing IP measure still fails closed.
		var policy OmniRiskPolicyV2
		if e := tx.First(&policy, 1).Error; e != nil {
			return e
		}
		if !policy.Shadow && (policy.AutoHold || policy.NewUserProtection || policy.TemporaryIP) {
			return errors.New("risk_source_unavailable")
		}
		var active int64
		if e := tx.Model(&OmniRiskIP{}).Where("revoked_at = 0 AND (expires_at = 0 OR expires_at > ?)", time.Now().Unix()).Count(&active).Error; e != nil {
			return e
		}
		if active == 0 {
			return nil
		}
		return errors.New("risk_source_unavailable")
	}
	if tag == "" || !OmniRiskV2Ready() {
		return nil
	}
	var rows []OmniRiskIP
	if err := tx.Where("tag = ? AND revoked_at = 0 AND (expires_at = 0 OR expires_at > ?)", tag, time.Now().Unix()).Find(&rows).Error; err != nil {
		return err
	}
	for _, r := range rows {
		if r.Source == "manual" || !paid {
			return ErrOmniRiskHold
		}
	}
	return nil
}

// An ambiguous historical credit is protected rather than guessed to be unpaid.
func OmniRiskPaid(tx *gorm.DB, uid int) (bool, error) {
	var n int64
	if err := tx.Model(&OmniRiskFundingEvidence{}).Where("user_id = ? AND classification IN ?", uid, []string{"paid", "funding_unknown"}).Count(&n).Error; err != nil {
		return false, err
	}
	if n > 0 {
		return true, nil
	}
	if err := tx.Model(&OmniLedger{}).Where("user_id = ? AND ((kind IN ? AND cents > 0) OR imported = ?)", uid, []string{"topup", "verified_credit", "wallet_credit"}, true).Count(&n).Error; err != nil {
		return false, err
	}
	if n > 0 {
		return true, nil
	}
	var a OmniAccount
	if e := tx.Where("user_id = ?", uid).Find(&a).Error; e != nil {
		return false, e
	}
	if a.NetRechargeCents > 0 {
		return true, nil
	}
	if tx.Migrator().HasTable("top_ups") {
		if e := tx.Table("top_ups").Where("user_id = ? AND status = ? AND money > 0 AND payment_method <> ?", uid, "success", "balance").Count(&n).Error; e != nil {
			return false, e
		}
		if n > 0 {
			return true, nil
		}
	}
	if e := tx.Table("logs").Where("user_id = ? AND type = 1", uid).Count(&n).Error; e != nil {
		return false, e
	}
	return n > 0, nil
}
func OmniRiskReadControl(tx *gorm.DB, uid int) (OmniRiskControl, error) {
	var r OmniRiskControl
	if !OmniRiskV2Ready() {
		return r, nil
	}
	err := tx.Where("user_id = ?", uid).Find(&r).Error
	return r, err
}
func riskV2Held(state string) bool {
	return state == "hold" || state == "confirmed" || state == "budget"
}
func OmniRiskAdmission(tx *gorm.DB, uid int) error {
	if !OmniRiskV2Ready() {
		return nil
	}
	r, e := OmniRiskReadControl(tx, uid)
	if e != nil {
		return e
	}
	if riskV2Held(r.State) {
		return ErrOmniRiskHold
	}
	return nil
}
func OmniRiskRestrictedEligible(uid int) bool {
	if !OmniRiskV2Ready() || uid <= 0 {
		return false
	}
	var n int64
	if DB.Model(&OmniRiskNoticeV2{}).Where("user_id = ?", uid).Count(&n).Error != nil {
		return false
	}
	return n > 0
}
func OmniRiskRecordV2(uid int, kind, key, ip, ua string, quota int64, tokenIDs ...int) error {
	if !OmniRiskV2Ready() || uid <= 0 {
		return nil
	}
	s, e := LoadOmniRiskSecrets()
	if e != nil {
		return e
	}
	if len(key) > 100 {
		key = OmniRiskDigest([]byte(key))
	}
	if key == "" {
		key = uuid.NewString()
	}
	row := OmniRiskEventV2{Key: kind + ":" + key, UserID: uid, Kind: kind, CreatedAt: time.Now().Unix(), Network: OmniRiskNetworkTag(ip), Browser: OmniRiskTag(s.HMACKey, "browser", ua), Quota: quota}
	if len(tokenIDs) > 0 {
		row.TokenID = tokenIDs[0]
	}
	if row.Network == "risk_source_unavailable" {
		return errors.New("risk_source_unavailable")
	}
	return DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
}

type OmniRiskBurstMember struct {
	UserID                    int
	RegisteredAt              int64
	Gift                      bool
	LoginAt, CreateAt, ViewAt int64
	Network, Browser          string
}

func OmniRiskBurstMatch(m OmniRiskBurstMember) bool {
	return m.UserID > 0 && m.Gift && m.Network != "" && m.Browser != "" && m.LoginAt >= m.RegisteredAt && m.CreateAt >= m.LoginAt && m.ViewAt >= m.CreateAt && m.ViewAt <= m.RegisteredAt+90
}
func OmniRiskV2Burst(uid int) error {
	if !OmniRiskV2Ready() {
		return nil
	}
	p, err := OmniRiskV2Policy()
	if err != nil {
		return err
	}
	var seed OmniRiskEventV2
	if err = DB.Where("user_id = ? AND kind = ? AND network <> '' AND browser <> ''", uid, "token.view").Order("id DESC").First(&seed).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	} else if err != nil {
		return err
	}

	var summaries []OmniRiskBurstMember
	query := DB.Model(&OmniRiskEventV2{}).Select("user_id,network,browser,COALESCE(MIN(CASE WHEN kind='registration.success' THEN created_at END),0) AS registered_at,MAX(CASE WHEN kind='registration.success' AND quota>0 THEN 1 ELSE 0 END) AS gift,COALESCE(MIN(CASE WHEN kind='login.success' THEN created_at END),0) AS login_at,COALESCE(MIN(CASE WHEN kind='token.create' THEN created_at END),0) AS create_at,COALESCE(MIN(CASE WHEN kind='token.view' THEN created_at END),0) AS view_at")
	query = query.Where("network = ? AND browser = ? AND created_at >= ? AND kind IN ?", seed.Network, seed.Browser, time.Now().Unix()-1890, []string{"registration.success", "login.success", "token.create", "token.view"})
	query = query.Where("user_id IN (?)", DB.Model(&OmniRiskEventV2{}).Select("user_id").Where("network = ? AND browser = ? AND kind = ? AND created_at >= ?", seed.Network, seed.Browser, "registration.success", time.Now().Unix()-1800))
	if err = query.Group("user_id,network,browser").Order("user_id").Limit(1001).Find(&summaries).Error; err != nil {
		return err
	}
	if len(summaries) > 1000 {
		return errors.New("risk_onboarding_account_overflow")
	}
	members := map[int]*OmniRiskBurstMember{}
	for i := range summaries {
		m := &summaries[i]
		members[m.UserID] = m
	}
	ids := []int{}
	for id, m := range members {
		if OmniRiskBurstMatch(*m) && m.RegisteredAt >= time.Now().Unix()-1800 {
			ids = append(ids, id)
		}
	}
	sort.Ints(ids)
	if len(ids) < 3 {
		return nil
	}
	for _, id := range ids {
		fp := OmniRiskDigest([]byte(fmt.Sprintf("burst-v2:%s:%s:%d", seed.Network, seed.Browser, members[id].RegisteredAt)))
		if err := OmniRiskApplyV2(id, fp, "registration_sequence", p.AutoHold && !p.Shadow, 0, seed.Network); err != nil {
			return err
		}
	}
	return nil
}
func riskV2Notice(tx *gorm.DB, uid int, caseID, key, title, body string) error {
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&OmniRiskNoticeV2{ID: key, UserID: uid, CaseID: caseID, Title: title, Body: body, CreatedAt: time.Now().Unix()}).Error
}
func riskV2Action(tx *gorm.DB, uid, actor int, caseID, action, key, note string) error {
	if err := tx.Create(&OmniRiskActionV2{ID: key, UserID: uid, ActorID: actor, CaseID: caseID, Action: action, Note: note, CreatedAt: time.Now().Unix()}).Error; err != nil {
		return err
	}
	return tx.Create(&AuditLog{EventId: uuid.NewString(), UserId: uid, ActorRole: common.RoleRootUser, CreatedAt: time.Now().Unix(), Category: "security", Action: "risk.v2." + action, Success: true, Content: "风险措施 " + action + "；案件 " + caseID}).Error
}
func OmniRiskApplyV2(uid int, fp, reason string, enforce bool, actor int, network string, expectedVersion ...int64) error {
	if !OmniRiskV2Ready() {
		return nil
	}
	settings, se := GetOmniRiskSettings()
	if se != nil {
		return se
	}
	if uid == settings.ServiceUserID {
		return nil
	}
	changed := false
	err := DB.Transaction(func(tx *gorm.DB) error {
		if _, e := omniAccount(tx, uid); e != nil {
			return e
		}
		var u User
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "role", "status", "auth_version").First(&u, uid).Error; e != nil {
			return e
		}
		if u.Role != common.RoleCommonUser {
			return nil
		}

		old, e := OmniRiskReadControl(tx, uid)
		if e != nil {
			return e
		}
		if len(expectedVersion) > 0 && old.Version != expectedVersion[0] {
			return errors.New("risk_version_conflict")
		}
		if actor == 0 {
			var rejected int64
			if e := tx.Model(&OmniRiskCase{}).Where("user_id = ? AND fingerprint = ? AND status = ? AND verdict = ?", uid, fp, "closed", "false_positive").Count(&rejected).Error; e != nil {
				return e
			}
			if rejected > 0 {
				return nil
			}
			var applied int64
			if e := tx.Model(&OmniRiskActionV2{}).Where("user_id = ? AND action IN ? AND case_id IN (?)", uid, []string{"guard", "hold", "confirm", "restore"}, tx.Model(&OmniRiskCase{}).Select("id").Where("user_id = ? AND fingerprint = ?", uid, fp)).Count(&applied).Error; e != nil {
				return e
			}
			if applied > 0 {
				return nil
			}

		}
		if (old.Fingerprint == fp && (old.State != "shadow" || !enforce)) || old.State == "hold" || old.State == "confirmed" {
			return nil
		}
		paid, e := OmniRiskPaid(tx, uid)
		if e != nil {
			return e
		}
		state := "shadow"
		if enforce {
			state = "hold"
			if (paid && actor == 0) || strings.HasPrefix(reason, "observe:") {
				state = "guard"
			}
		}
		if u.Status != common.UserStatusEnabled {
			if actor > 0 {
				return errors.New("risk_existing_restriction_conflict")
			}
			return nil
		}
		caseID := "RV2-" + uuid.NewString()
		now := time.Now().Unix()
		var sourceEvents []OmniRiskEventV2
		if e = tx.Where("user_id = ? AND created_at >= ?", uid, now-1800).Order("id DESC").Limit(200).Find(&sourceEvents).Error; e != nil {
			return e
		}
		raw, _ := json.Marshal(map[string]any{"rule": reason, "policy": "v2", "fingerprint": fp, "network": network, "provisional": true, "source_events": sourceEvents, "paid_or_funding_unknown": paid})
		report, _ := json.Marshal(map[string]any{"summary": "组合风险命中，等待人工复核", "severity": "high", "suspicious_points": []string{reason}, "counterevidence": []string{"共同来源不证明同一身份"}, "uncertainties": []string{"尚待经营者复核"}, "suggested_actions": []string{}})
		if e := tx.Create(&OmniRiskCase{ID: caseID, UserID: uid, GroupKey: network, Fingerprint: fp, Severity: "high", Status: "open", Verdict: "pending", Report: string(report), Evidence: string(raw), Revision: 1, CreatedAt: now, UpdatedAt: now}).Error; e != nil {
			return e
		}
		r := OmniRiskControl{UserID: uid, Version: old.Version + 1, State: state, CaseID: caseID, Fingerprint: fp, Reason: reason, Network: network, BeforeStatus: u.Status, HeldAuthVersion: u.AuthVersion, StartedAt: now, Concurrency: 1, BudgetMicros: 2000000}
		if state == "guard" {
			if paid {
				r.BudgetMicros, r.Concurrency = OmniRiskBaseline(tx, uid, now)
			} else {
				r.BudgetMicros = 1000000
			}
		}
		if state == "hold" {
			version, e := IncrementUserAuthVersionWithTx(tx, uid)
			if e != nil {
				return e
			}
			r.HeldAuthVersion = version
			var tokens []Token
			if e = tx.Select("id").Where("user_id = ? AND status = ?", uid, common.TokenStatusEnabled).Find(&tokens).Error; e != nil {
				return e
			}
			ids := []int{}
			for _, t := range tokens {
				ids = append(ids, t.Id)
				if e = tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&OmniRiskTokenFence{TokenID: t.Id, CaseID: caseID}).Error; e != nil {
					return e
				}
			}
			b, _ := json.Marshal(ids)
			r.TokenIDs = string(b)
			if e = tx.Model(&User{}).Where("id = ?", uid).Update("status", common.UserStatusDisabled).Error; e != nil {
				return e
			}
			if e = tx.Model(&Token{}).Where("user_id = ? AND id IN ?", uid, ids).Update("status", common.TokenStatusDisabled).Error; e != nil {
				return e
			}
			if e = tx.Model(&UserSession{}).Where("user_id = ? AND status IN ?", uid, []string{"active", "revoking"}).Updates(map[string]any{"status": "revoked", "version": gorm.Expr("version + 1"), "revoked_at": now, "revoked_reason": "risk_v2"}).Error; e != nil {
				return e
			}
		}
		if e = tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&r).Error; e != nil {
			return e
		}
		if actor > 0 && old.CaseID != "" && old.CaseID != caseID {
			if e = tx.Model(&OmniRiskCase{}).Where("id = ?", old.CaseID).Updates(map[string]any{"status": "closed", "verdict": "confirmed", "reviewed_by": actor, "closed_at": now, "revision": gorm.Expr("revision + 1")}).Error; e != nil {
				return e
			}
			if e = tx.Model(&OmniRiskRewardHold{}).Where("case_id = ? AND state = ?", old.CaseID, "pending").Update("case_id", caseID).Error; e != nil {
				return e
			}
		}
		if e = riskV2Action(tx, uid, actor, caseID, state, uuid.NewString(), reason); e != nil {
			return e
		}
		if enforce {
			if e = riskV2Notice(tx, uid, caseID, "risk-v2:"+caseID, "账号安全复核通知", "账号检测到组合风险，已进入安全保护，等待人工复核。余额和已到账奖励保留。您可以在此提交说明；本通知不代表已确认违规。"); e != nil {
				return e
			}
			changed = true
		}
		if !enforce {
			return nil
		}
		return OmniRiskEnqueue(tx, "v2:"+caseID, caseID, "case", fmt.Sprintf("OmniAPI 风控\n账号 #%d\n措施：%s\n原因：%s\n等待人工复核\nhttps://omniapi.biz/risk-control?case=%s", uid, state, reason, caseID))
	})
	if err != nil {
		return err
	}
	if changed {
		_ = PublishUserAuthCache(uid)
		_ = InvalidateUserTokensCache(uid)
		RiskV2AfterControl(uid)
	}
	return nil
}
func OmniRiskBaseline(tx *gorm.DB, uid int, before int64) (int64, int) {
	type row struct {
		Bucket int64
		Cost   int64
		N      int64
	}
	var rows []row
	if e := tx.Model(&OmniRiskExposure{}).Select("CAST(created_at / 300 AS BIGINT) AS bucket,SUM(actual) AS cost,COUNT(*) AS n").Where("user_id = ? AND state = ? AND created_at >= ? AND created_at < ?", uid, "settled", before-7*86400, before-1800).Group("CAST(created_at / 300 AS BIGINT)").Find(&rows).Error; e != nil {
		return 2000000, 1
	}
	var n int64
	days := map[int64]bool{}
	values := []int64{}
	for _, r := range rows {
		n += r.N
		days[r.Bucket/288] = true
		values = append(values, r.Cost)
	}
	if n < 30 || len(days) < 3 {
		return riskV2HistoricalBaseline(tx, uid, before)
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	v := values[(len(values)*95+99)/100-1] * 2
	return min(int64(10000000), max(int64(2000000), v)), 3
}
func OmniRiskReserveV2(uid int, request string, channel int, bound int64, quoteHash string) (*OmniRiskExposure, error) {
	if !OmniRiskV2Ready() {
		return nil, nil
	}
	p, e := OmniRiskV2Policy()
	if e != nil {
		return nil, e
	}
	var result *OmniRiskExposure
	budgetHit := false
	e = DB.Transaction(func(tx *gorm.DB) error {
		if _, e := omniAccount(tx, uid); e != nil {
			return e
		}
		var u User
		if e := tx.Select("id", "status", "role", "created_at").First(&u, uid).Error; e != nil {
			return e
		}
		if u.Role != common.RoleCommonUser {
			return nil
		}
		if u.Status != common.UserStatusEnabled {
			return ErrOmniRiskHold
		}
		if e := OmniRiskAdmission(tx, uid); e != nil {
			return e
		}
		paid, e := OmniRiskPaid(tx, uid)
		if e != nil {
			return e
		}
		r, e := OmniRiskReadControl(tx, uid)
		if e != nil {
			return e
		}
		trial := OmniTrialLimitsFor(uid)
		newProtected := OmniNewUserProtected(p, u, r, paid, trial.Enabled)
		if !newProtected && r.State != "guard" {
			return nil
		}
		if bound <= 0 || quoteHash == "" {
			return ErrOmniRiskQuote
		}
		budget := p.NewBudgetMicros
		concurrency := 1
		start := u.CreatedAt
		if newProtected && trial.Enabled && r.State != "guard" {
			budget, concurrency = trial.TotalMicros, trial.Concurrency
		}
		requestScoped := newProtected && trial.Enabled && r.State != "guard"
		if r.State == "guard" {
			budget = r.BudgetMicros
			concurrency = r.Concurrency
			if paid {
				start = time.Now().Unix() - 300
			} else {
				start = min(r.StartedAt, u.CreatedAt)
			}
		}
		if concurrency < 1 {
			concurrency = 1
		}
		exposures := omniTrialExposures(tx, uid, start)
		total, e := omniTrialExposureTotal(exposures)
		if e != nil {
			return e
		}
		var active int64
		if e := tx.Model(&OmniRiskExposure{}).Where("user_id = ? AND state IN ? AND request_id <> ?", uid, []string{"reserved", "sending"}, request).Distinct("request_id").Count(&active).Error; e != nil {
			return e
		}
		if active >= int64(concurrency) {
			if requestScoped {
				return &OmniTrialRejection{Code: "trial_concurrency_limit"}
			}
			return ErrOmniRiskLimit
		}
		if requestScoped {
			requestTotal, e := omniTrialExposureTotal(exposures.Where("request_id = ?", request))
			if e != nil {
				return e
			}
			if requestTotal > trial.RequestMicros-bound {
				return &OmniTrialRejection{Code: "trial_request_budget", TryBackup: trial.Fallback}
			}
			if total > budget-bound {
				return &OmniTrialRejection{Code: "trial_account_budget", TryBackup: trial.Fallback, RemainingMicros: max(0, budget-total), EndsAt: u.CreatedAt + 86400}
			}
		} else if total > budget-bound {
			budgetHit = newProtected
			return ErrOmniRiskLimit
		}
		var ordinal int64
		if e := tx.Model(&OmniRiskExposure{}).Where("request_id = ?", request).Count(&ordinal).Error; e != nil {
			return e
		}
		row := &OmniRiskExposure{ID: uuid.NewString(), UserID: uid, RequestID: request, ChannelID: channel, Reserved: bound, QuoteHash: OmniRiskDigest([]byte(quoteHash)), CostQuote: quoteHash, Ordinal: int(ordinal) + 1, Owner: OmniRiskProcessID, State: "sending", CreatedAt: time.Now().Unix()}
		if e := tx.Create(row).Error; e != nil {
			return e
		}
		result = row
		return nil
	})
	if budgetHit {
		_ = OmniRiskBudgetPause(uid)
	}
	return result, e
}
func OmniRiskBudgetPause(uid int, reasons ...string) error {
	reason := "new_user_budget"
	if len(reasons) > 0 && reasons[0] == "quote_unavailable" {
		reason = reasons[0]
	}
	err := DB.Transaction(func(tx *gorm.DB) error {
		if _, e := omniAccount(tx, uid); e != nil {
			return e
		}
		r, e := OmniRiskReadControl(tx, uid)
		if e != nil {
			return e
		}
		if riskV2Held(r.State) {
			return nil
		}
		paid, e := OmniRiskPaid(tx, uid)
		if e != nil {
			return e
		}
		if paid {
			return nil
		}
		r.UserID = uid
		r.Version++
		r.State = "budget"
		r.Reason = reason
		r.StartedAt = time.Now().Unix()
		var user User
		if e = tx.Select("id", "status").First(&user, uid).Error; e != nil {
			return e
		}
		if user.Status != common.UserStatusEnabled {
			return nil
		}
		r.BeforeStatus = user.Status
		r.HeldAuthVersion, e = IncrementUserAuthVersionWithTx(tx, uid)
		if e != nil {
			return e
		}
		if e = tx.Model(&UserSession{}).Where("user_id = ? AND status = ?", uid, "active").Updates(map[string]any{"status": "revoked", "version": gorm.Expr("version + 1"), "revoked_at": time.Now().Unix(), "revoked_reason": "risk_v2"}).Error; e != nil {
			return e
		}
		if r.CaseID == "" {
			r.CaseID = "RB-" + uuid.NewString()
		}
		summary := "新账号安全预算触顶，等待人工复核"
		noticeBody := "本次请求超过新账号安全试用预算，已暂停新调用。赠额及余额保留，请提交说明等待人工复核；这不代表账号已确认违规。"
		if reason == "quote_unavailable" {
			summary = "本次请求预算暂无法核实，等待人工复核"
			noticeBody = "本次请求暂时无法核验是否符合安全额度，已暂停新调用。余额与赠额保留，请提交说明等待人工复核；这不代表账号已确认违规。"
		}
		evidence, _ := json.Marshal(map[string]any{"rule": reason})
		report, _ := json.Marshal(map[string]any{"summary": summary, "severity": "observe", "suspicious_points": []string{}, "counterevidence": []string{"正常试用也可能触发预算保护"}, "uncertainties": []string{"采购费用含未决预留"}, "suggested_actions": []string{}})
		if e = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&OmniRiskCase{ID: r.CaseID, UserID: uid, Severity: "observe", Status: "open", Verdict: "pending", Report: string(report), Evidence: string(evidence), Revision: 1, CreatedAt: r.StartedAt, UpdatedAt: r.StartedAt}).Error; e != nil {
			return e
		}
		if e = tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&r).Error; e != nil {
			return e
		}
		if e = riskV2Notice(tx, uid, r.CaseID, "budget:"+r.CaseID, "新账号消费保护", noticeBody); e != nil {
			return e
		}
		return OmniRiskEnqueue(tx, "budget:"+r.CaseID, r.CaseID, "case", fmt.Sprintf("OmniAPI 新账号消费保护\n账号 #%d\n请复核：https://omniapi.biz/risk-control", uid))
	})
	if err == nil {
		_ = PublishUserAuthCache(uid)
		RiskV2AfterControl(uid)
	}
	return err
}

// Unknown procurement stays reserved, including after cancellation or restart.
func OmniRiskFinishV2(id string, actual int64, confirmed bool, notSent bool) error {
	if id == "" {
		return nil
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var r OmniRiskExposure
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&r, "id = ?", id).Error; e != nil {
			return e
		}
		if r.State == "settled" || r.State == "not_sent" {
			return nil
		}
		state := "unknown"
		if notSent {
			state = "not_sent"
			actual = 0
		} else if confirmed {
			if actual < 0 {
				return errors.New("invalid risk cost")
			}
			state = "settled"
		}
		return tx.Model(&r).Updates(map[string]any{"state": state, "actual": actual, "finished_at": time.Now().Unix()}).Error
	})
}
func OmniRiskRestoreV2(uid, actor int, version int64, note string, confirmed bool) error {
	e := DB.Transaction(func(tx *gorm.DB) error {
		if _, e := omniAccount(tx, uid); e != nil {
			return e
		}
		r, e := OmniRiskReadControl(tx, uid)
		if e != nil {
			return e
		}
		if r.UserID == 0 || r.Version != version {
			return errors.New("risk_version_conflict")
		}
		decision := "restored"
		if confirmed {
			decision = "confirmed"
		}
		if e = tx.Model(&OmniRiskAppeal{}).Where("user_id = ? AND case_id = ? AND reviewed_at = 0", uid, r.CaseID).Updates(map[string]any{"reviewed_by": actor, "reviewed_at": time.Now().Unix(), "decision": decision}).Error; e != nil {
			return e
		}
		if confirmed {
			if r.State != "hold" && r.State != "confirmed" {
				return errors.New("risk_confirm_requires_hold")
			}
			r.State = "confirmed"
			r.Version++
			r.Note = note
			r.ReviewedAt = time.Now().Unix()
			if e = tx.Save(&r).Error; e != nil {
				return e
			}
			if e = tx.Model(&OmniRiskCase{}).Where("id = ?", r.CaseID).Updates(map[string]any{"status": "closed", "verdict": "confirmed", "reviewed_by": actor, "closed_at": time.Now().Unix(), "revision": gorm.Expr("revision + 1")}).Error; e != nil {
				return e
			}
			if e = tx.Model(&OmniRiskRewardHold{}).Where("case_id = ? AND state = ?", r.CaseID, "pending").Update("state", "cancelled").Error; e != nil {
				return e
			}
			if e = riskV2Action(tx, uid, actor, r.CaseID, "confirm", uuid.NewString(), note); e != nil {
				return e
			}
			return riskV2Notice(tx, uid, r.CaseID, "confirmed:"+r.CaseID, "复核处理结果", "经管理员复核，本账号继续限制使用。余额与已有账本保留；如有异议可提交说明。")
		}
		if r.State == "hold" || r.State == "confirmed" {
			var u User
			if e = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&u, uid).Error; e != nil {
				return e
			}
			if OmniEmailSettings().Enabled && !OmniEmailVerified(u) {
				return ErrOmniEmailRequired
			}
			if u.Status != common.UserStatusDisabled || u.AuthVersion != r.HeldAuthVersion || r.BeforeStatus != common.UserStatusEnabled {
				return errors.New("risk_restore_conflict")
			}
			var touched int64
			if e = tx.Model(&OmniRiskTokenFence{}).Where("case_id = ? AND touched_at > 0", r.CaseID).Count(&touched).Error; e != nil {
				return e
			}
			if touched > 0 {
				return errors.New("risk_token_restore_conflict")
			}
			if _, e = IncrementUserAuthVersionWithTx(tx, uid); e != nil {
				return e
			}
			if e = tx.Model(&User{}).Where("id = ?", uid).Update("status", common.UserStatusEnabled).Error; e != nil {
				return e
			}
			var ids []int
			_ = json.Unmarshal([]byte(r.TokenIDs), &ids)
			if e = tx.Model(&Token{}).Where("user_id = ? AND id IN ? AND status = ?", uid, ids, common.TokenStatusDisabled).Update("status", common.TokenStatusEnabled).Error; e != nil {
				return e
			}
		}
		if r.State == "budget" || r.State == "guard" {
			var current User
			if e = tx.Select("id", "status", "auth_version").First(&current, uid).Error; e != nil {
				return e
			}
			if current.Status != common.UserStatusEnabled || current.AuthVersion != r.HeldAuthVersion {
				return errors.New("risk_restore_conflict")
			}
		}
		r.State = "restored"
		r.Version++
		r.ReviewedAt = time.Now().Unix()
		r.Note = note
		if e = tx.Save(&r).Error; e != nil {
			return e
		}
		if e = tx.Model(&OmniRiskCase{}).Where("id = ?", r.CaseID).Updates(map[string]any{"status": "closed", "verdict": "false_positive", "reviewed_by": actor, "closed_at": time.Now().Unix(), "revision": gorm.Expr("revision + 1")}).Error; e != nil {
			return e
		}
		if e = tx.Model(&OmniRiskRewardHold{}).Where("case_id = ? AND state IN ?", r.CaseID, []string{"pending", "cancelled"}).Update("state", "released").Error; e != nil {
			return e
		}
		if e = riskV2Action(tx, uid, actor, r.CaseID, "restore", uuid.NewString(), note); e != nil {
			return e
		}
		return riskV2Notice(tx, uid, r.CaseID, "restore:"+r.CaseID, "账号保护已解除", "经人工复核，本次安全保护已解除，余额和奖励保留。请重新登录；原登录会话保持失效。")
	})
	if e == nil {
		_ = PublishUserAuthCache(uid)
		_ = InvalidateUserTokensCache(uid)
	}
	return e
}
func OmniRiskRewardGate(tx *gorm.DB, inviter, invitee int, amount int64) (int64, bool, error) {
	if !OmniRiskV2Ready() {
		return amount, false, nil
	}
	var old OmniRiskRewardHold
	if e := tx.Where("invitee = ?", invitee).Find(&old).Error; e != nil {
		return 0, false, e
	}
	if old.Invitee > 0 {
		amount = old.Quota
		if old.State != "released" {
			return amount, true, nil
		}
	}
	for _, id := range []int{inviter, invitee} {
		r, e := OmniRiskReadControl(tx, id)
		if e != nil {
			return 0, false, e
		}
		if r.UserID > 0 && r.State != "restored" && r.State != "shadow" {
			h := OmniRiskRewardHold{Invitee: invitee, Inviter: inviter, Quota: amount, State: "pending", CaseID: r.CaseID, CreatedAt: time.Now().Unix(), FundingMode: "modern"}
			if old.Invitee > 0 {
				h.FundingMode = old.FundingMode
			}
			return amount, true, tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&h).Error
		}
	}
	return amount, false, nil
}
func OmniRiskLegacyReferral(inviter, invitee int) error {
	if !OmniRiskV2Ready() {
		return inviteUser(inviter)
	}
	defer invalidateUserCache(inviter)
	return DB.Transaction(func(tx *gorm.DB) error {
		if e := OmniRiskLockRewardOwners(tx, inviter, invitee); e != nil {
			return e
		}
		var invited User
		if e := tx.Select("id", "inviter_id").First(&invited, invitee).Error; e != nil {
			return e
		}
		if invited.InviterId != inviter {
			return errors.New("invalid referral")
		}
		var old OmniRiskRewardHold
		if e := tx.Where("invitee = ?", invitee).Find(&old).Error; e != nil {
			return e
		}
		if old.AppliedAt > 0 || old.State == "applied" {
			return nil
		}
		amount, held, e := OmniRiskRewardGate(tx, inviter, invitee, int64(common.QuotaForInviter))
		if e != nil {
			return e
		}
		if held {
			return tx.Model(&OmniRiskRewardHold{}).Where("invitee = ?", invitee).Update("funding_mode", "legacy").Error
		}
		q := tx.Model(&User{}).Where("id = ?", inviter).Updates(map[string]any{"aff_count": gorm.Expr("aff_count + 1"), "aff_quota": gorm.Expr("aff_quota + ?", amount), "aff_history": gorm.Expr("aff_history + ?", amount)})
		if q.Error != nil {
			return q.Error
		}
		if q.RowsAffected != 1 {
			return errors.New("inviter missing")
		}
		return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&OmniRiskRewardHold{Invitee: invitee, Inviter: inviter, Quota: amount, FundingMode: "legacy", State: "applied", CreatedAt: time.Now().Unix(), AppliedAt: time.Now().Unix()}).Error
	})
}

var riskActive = struct {
	sync.Mutex
	Calls map[int]map[string]context.CancelFunc
}{Calls: map[int]map[string]context.CancelFunc{}}

func OmniRiskTrack(uid int, id string, cancel context.CancelFunc) func() {
	riskActive.Lock()
	if riskActive.Calls[uid] == nil {
		riskActive.Calls[uid] = map[string]context.CancelFunc{}
	}
	riskActive.Calls[uid][id] = cancel
	riskActive.Unlock()
	return func() {
		riskActive.Lock()
		delete(riskActive.Calls[uid], id)
		if len(riskActive.Calls[uid]) == 0 {
			delete(riskActive.Calls, uid)
		}
		riskActive.Unlock()
	}
}
func OmniRiskCancel(uid int) {
	riskActive.Lock()
	callbacks := []context.CancelFunc{}
	for _, c := range riskActive.Calls[uid] {
		callbacks = append(callbacks, c)
	}
	riskActive.Unlock()
	for _, c := range callbacks {
		c()
	}
}

func OmniRiskUsageV2(uid int, request string, p RecordConsumeLogParams) error {
	if !OmniRiskV2Ready() || request == "" {
		return nil
	}
	var row OmniRiskExposure
	e := DB.Where("user_id = ? AND request_id = ? AND channel_id = ? AND state IN ?", uid, request, p.ChannelId, []string{"sending", "reserved", "unknown"}).Order("ordinal DESC").First(&row).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return nil
	}
	if e != nil {
		return e
	}
	var q struct {
		Input, Output, FX string
		Fixed             int64
	}
	if json.Unmarshal([]byte(row.CostQuote), &q) != nil {
		return ErrOmniRiskQuote
	}
	if q.Fixed > 0 {
		return OmniRiskFinishV2(row.ID, q.Fixed, true, false)
	}
	if p.PromptTokens < 0 || p.CompletionTokens < 0 || (p.PromptTokens == 0 && p.CompletionTokens == 0) {
		return nil
	}
	input := int64(p.PromptTokens)
	if p.Other != nil {
		var meta struct {
			Semantic string `json:"usage_semantic"`
			Total    int64  `json:"input_tokens_total"`
			Cache    int64  `json:"cache_tokens"`
			Creation int64  `json:"cache_creation_tokens"`
			Five     int64  `json:"cache_creation_tokens_5m"`
			Hour     int64  `json:"cache_creation_tokens_1h"`
		}
		if json.Unmarshal([]byte(p.Other.JSONString()), &meta) != nil {
			return ErrOmniRiskQuote
		}
		if meta.Total > 0 {
			input = meta.Total
		} else if meta.Semantic == "anthropic" {
			input += meta.Cache + max(meta.Creation, meta.Five+meta.Hour)
		}
	}

	a, e := decimal.NewFromString(q.Input)
	if e != nil {
		return e
	}
	b, e := decimal.NewFromString(q.Output)
	if e != nil {
		return e
	}
	fx, e := decimal.NewFromString(q.FX)
	if e != nil {
		return e
	}
	amount := a.Mul(decimal.NewFromInt(input)).Add(b.Mul(decimal.NewFromInt(int64(p.CompletionTokens)))).Mul(fx).Ceil()
	if amount.IsNegative() || amount.GreaterThan(decimal.NewFromInt(1000000000000)) {
		return ErrOmniRiskQuote
	}
	return OmniRiskFinishV2(row.ID, amount.IntPart(), true, false)
}

func (OmniRiskEventV2) TableName() string { return "omni_risk_event_v2" }
func OmniRiskMarkTokenMutation(id int) error {
	if !OmniRiskV2Ready() || id <= 0 {
		return nil
	}
	return DB.Model(&OmniRiskTokenFence{}).Where("token_id = ? AND touched_at = 0", id).Update("touched_at", time.Now().Unix()).Error
}
func GetUserVerificationStateWithTx(tx *gorm.DB, uid int) (*UserVerificationState, error) {
	return getUserVerificationState(tx, uid, true)
}

// Token metadata and restoration serialize on the same account row. A fence
// cannot be written in a separate transaction from the actual mutation.
func OmniRiskMutateToken(id int, mutate func(*gorm.DB) error, expected ...int64) error {
	if !OmniRiskV2Ready() {
		return mutate(DB)
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var t Token
		if e := tx.Select("id", "user_id").First(&t, id).Error; e != nil {
			return e
		}
		if _, e := omniAccount(tx, t.UserId); e != nil {
			return e
		}
		if len(expected) > 0 && expected[0] > 0 {
			var u User
			if e := tx.Select("id", "auth_version").First(&u, t.UserId).Error; e != nil {
				return e
			}
			if u.AuthVersion != expected[0] {
				return ErrOmniRiskHold
			}
		}
		if e := tx.Model(&OmniRiskTokenFence{}).Where("token_id = ? AND touched_at = 0", id).Update("touched_at", time.Now().Unix()).Error; e != nil {
			return e
		}
		return mutate(tx)
	})
}

func OmniRiskEndRequestV2(uid int, request string) error {
	if !OmniRiskV2Ready() || uid <= 0 || request == "" {
		return nil
	}
	return DB.Model(&OmniRiskExposure{}).Where("user_id = ? AND request_id = ? AND state IN ?", uid, request, []string{"reserved", "sending"}).Updates(map[string]any{"state": "unknown", "finished_at": time.Now().Unix()}).Error
}

func OmniRiskLockRewardOwners(tx *gorm.DB, inviter, invitee int) error {
	if inviter <= 0 || invitee <= 0 || inviter == invitee {
		return errors.New("invalid referral")
	}
	for _, id := range []int{min(inviter, invitee), max(inviter, invitee)} {
		if _, e := omniAccount(tx, id); e != nil {
			return e
		}
	}
	return nil
}

func OmniRiskBusinessMutation(tx *gorm.DB, uid int, expected ...int64) error {
	if !OmniRiskV2Ready() {
		return nil
	}
	if e := OmniRiskAdmission(tx, uid); e != nil {
		return e
	}
	var user User
	if e := tx.Select("id", "status", "auth_version").First(&user, uid).Error; e != nil {
		return e
	}
	if user.Status != common.UserStatusEnabled {
		return ErrOmniRiskHold
	}
	if len(expected) > 0 && expected[0] > 0 && user.AuthVersion != expected[0] {
		return ErrOmniRiskHold
	}
	return nil
}

func (OmniRiskRetryV2) TableName() string { return "omni_risk_retry_v2" }
