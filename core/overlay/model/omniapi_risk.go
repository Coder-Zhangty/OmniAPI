package model

// Risk control owns these additive tables only. It has no account enforcement API.
import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"os"
	"strconv"
	"strings"
	"time"
)

const OmniRiskModel = "deepseek-v4.1-flash"
const OmniRiskDailyMicros int64 = 1000000

type OmniRiskSettings struct {
	ID             int    `json:"id" gorm:"primaryKey"`
	Enabled        bool   `json:"enabled"`
	Notifications  bool   `json:"notifications"`
	ServiceUserID  int    `json:"service_user_id"`
	ServiceTokenID int    `json:"service_token_id"`
	AuditSince     int64  `json:"audit_since"`
	CollectedAt    int64  `json:"collected_at"`
	ReviewedAt     int64  `json:"reviewed_at"`
	LastError      string `json:"last_error"`
	Version        int64  `json:"version"`
}
type OmniRiskLease struct {
	Key     string `gorm:"primaryKey;size:40"`
	Owner   string
	Epoch   int64
	Expires int64
}
type OmniRiskRun struct {
	ID            string `json:"id" gorm:"primaryKey;size:100"`
	Kind          string `json:"kind"`
	State         string `json:"state" gorm:"index"`
	StartedAt     int64  `json:"started_at" gorm:"index"`
	FinishedAt    int64  `json:"finished_at"`
	Collected     int    `json:"collected"`
	Reviewed      int    `json:"reviewed"`
	Pending       int64  `json:"pending"`
	ErrorCode     string `json:"error_code"`
	PolicyVersion string `json:"policy_version"`
	PromptSHA     string `json:"prompt_sha"`
	Owner         string `json:"-"`
	Epoch         int64  `json:"-"`
}
type OmniRiskSubject struct {
	UserID              int    `json:"user_id" gorm:"primaryKey"`
	Fingerprint         string `json:"fingerprint"`
	ReviewedFingerprint string `json:"reviewed_fingerprint"`
	RiskFingerprint     string `json:"-"`
	Evidence            string `json:"-" gorm:"type:text"`
	State               string `json:"state" gorm:"index"`
	Priority            int    `json:"priority"`
	ChangedAt           int64  `json:"changed_at" gorm:"index"`
	ReviewedAt          int64  `json:"reviewed_at"`
	CallID              string `json:"call_id"`
}
type OmniRiskDay struct {
	Day      string `json:"day" gorm:"primaryKey;size:10"`
	Reserved int64  `json:"reserved_micros"`
	Spent    int64  `json:"spent_micros"`
}
type OmniRiskCall struct {
	ID          string `json:"id" gorm:"primaryKey;size:80"`
	RunID       string `json:"run_id" gorm:"index"`
	UserID      int    `json:"user_id"`
	Fingerprint string `json:"fingerprint"`
	Day         string `json:"day" gorm:"index"`
	State       string `json:"state" gorm:"index"`
	Reserved    int64  `json:"reserved_micros"`
	Actual      int64  `json:"actual_micros"`
	FXMicros    int64  `json:"fx_micros"`
	RequestID   string `json:"request_id" gorm:"index"`
	CreatedAt   int64  `json:"created_at" gorm:"index"`
	FinishedAt  int64  `json:"finished_at"`
	ErrorCode   string `json:"error_code"`
}
type OmniRiskCase struct {
	ID          string `json:"id" gorm:"primaryKey;size:80"`
	UserID      int    `json:"user_id" gorm:"index"`
	GroupKey    string `json:"group_key" gorm:"index"`
	Fingerprint string `json:"fingerprint"`
	Severity    string `json:"severity"`
	Status      string `json:"status" gorm:"index"`
	Verdict     string `json:"verdict"`
	Report      string `json:"-" gorm:"type:text"`
	Evidence    string `json:"-" gorm:"type:text"`
	Revision    int64  `json:"revision"`
	CreatedAt   int64  `json:"created_at" gorm:"index"`
	UpdatedAt   int64  `json:"updated_at"`
	ClosedAt    int64  `json:"closed_at"`
	ReviewedBy  int    `json:"reviewed_by"`
	Note        string `json:"note"`
}
type OmniRiskOutbox struct {
	ID          string `json:"id" gorm:"primaryKey;size:180"`
	CaseID      string `json:"case_id" gorm:"index"`
	Kind        string `json:"kind"`
	Content     string `json:"content" gorm:"type:text"`
	State       string `json:"state" gorm:"index"`
	Attempts    int    `json:"attempts"`
	NextAt      int64  `json:"next_at" gorm:"index"`
	CreatedAt   int64  `json:"created_at" gorm:"index"`
	DeliveredAt int64  `json:"delivered_at"`
	ErrorCode   string `json:"error_code"`
}
type OmniRiskIncident struct {
	Key        string `json:"key" gorm:"primaryKey;size:80"`
	Active     bool   `json:"active"`
	Generation int64  `json:"generation"`
	UpdatedAt  int64  `json:"updated_at"`
}
type OmniRiskAuthEvent struct {
	ID            uint   `json:"id" gorm:"primaryKey"`
	UserID        int    `json:"user_id" gorm:"index:idx_risk_auth_user_time,priority:1"`
	InviterID     int    `json:"inviter_id"`
	GrantedQuota  int64  `json:"granted_quota"`
	CreatedAt     int64  `json:"created_at" gorm:"index:idx_risk_auth_user_time,priority:2;index"`
	Action        string `json:"action"`
	Success       bool   `json:"success"`
	Stage         string `json:"stage"`
	NetworkTag    string `json:"network_tag" gorm:"index"`
	BrowserTag    string `json:"browser_tag"`
	BrowserFamily string `json:"browser_family"`
	RequestID     string `json:"request_id" gorm:"size:64"`
}
type OmniRiskAction struct {
	ID        uint   `json:"id" gorm:"primaryKey"`
	CreatedAt int64  `json:"created_at" gorm:"index"`
	ActorID   int    `json:"actor_id"`
	Action    string `json:"action"`
	CaseID    string `json:"case_id"`
	Note      string `json:"note"`
}

type OmniRiskSecrets struct {
	HMACKey string `json:"hmac_key"`
	APIKey  string `json:"api_key"`
	Wecom   struct {
		Webhook string `json:"webhook"`
	} `json:"wecom"`
}

func LoadOmniRiskSecrets() (OmniRiskSecrets, error) {
	var s OmniRiskSecrets
	path := os.Getenv("OMNI_RISK_SECRETS_FILE")
	if path == "" {
		return s, errors.New("risk_secrets_unavailable")
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 16384 {
		return s, errors.New("risk_secrets_unavailable")
	}
	if json.Unmarshal(raw, &s) != nil || len(s.HMACKey) < 32 || len(s.APIKey) < 32 {
		return OmniRiskSecrets{}, errors.New("risk_secrets_invalid")
	}
	return s, nil
}
func OmniRiskTag(key, kind, value string) string {
	if kind == "browser" && len(value) > 2048 {
		value = value[:2048]
	}
	if value == "" || len(key) < 32 {
		return ""
	}
	h := hmac.New(sha256.New, []byte(key))
	h.Write([]byte(kind + "\x00" + value))
	return hex.EncodeToString(h.Sum(nil))[:24]
}
func OmniRiskDigest(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }
func OmniRiskDayKey(now time.Time) string {
	return now.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01-02")
}
func OmniRiskBrowser(ua string) string {
	for _, p := range []struct{ Need, Name string }{{"Edg/", "Edge"}, {"Chrome/", "Chrome"}, {"Firefox/", "Firefox"}, {"Safari/", "Safari"}, {"curl/", "curl"}, {"python", "Python"}} {
		if strings.Contains(ua, p.Need) {
			return p.Name
		}
	}
	return "unknown"
}
func OmniRiskMigrate(db *gorm.DB) error {
	if err := OmniRiskV2Migrate(db); err != nil {
		return err
	}
	if err := db.AutoMigrate(&OmniRiskSettings{}, &OmniRiskLease{}, &OmniRiskRun{}, &OmniRiskSubject{}, &OmniRiskDay{}, &OmniRiskCall{}, &OmniRiskCase{}, &OmniRiskOutbox{}, &OmniRiskIncident{}, &OmniRiskAuthEvent{}, &OmniRiskAction{}); err != nil {
		return err
	}
	return db.Clauses(clause.OnConflict{DoNothing: true}).Create(&OmniRiskSettings{ID: 1, AuditSince: time.Now().Unix(), Version: 1}).Error
}
func GetOmniRiskSettings() (OmniRiskSettings, error) {
	var s OmniRiskSettings
	err := DB.First(&s, 1).Error
	return s, err
}

func OmniRiskAcquire(owner string, now int64) (int64, bool, error) {
	row := OmniRiskLease{Key: "worker"}
	if err := DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return 0, false, err
	}
	q := DB.Model(&OmniRiskLease{}).Where("key = ? AND expires <= ?", "worker", now).Updates(map[string]any{"owner": owner, "epoch": gorm.Expr("epoch + 1"), "expires": now + 90})
	if q.Error != nil || q.RowsAffected != 1 {
		return 0, false, q.Error
	}
	err := DB.First(&row, "key = ? AND owner = ?", "worker", owner).Error
	return row.Epoch, err == nil, err
}
func OmniRiskRenew(owner string, epoch, now int64) bool {
	q := DB.Model(&OmniRiskLease{}).Where("key = ? AND owner = ? AND epoch = ? AND expires > ?", "worker", owner, epoch, now).Update("expires", now+90)
	return q.Error == nil && q.RowsAffected == 1
}
func OmniRiskFence(tx *gorm.DB, owner string, epoch int64) error {
	var l OmniRiskLease
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&l, "key = ?", "worker").Error; err != nil {
		return err
	}
	if l.Owner != owner || l.Epoch != epoch || l.Expires <= time.Now().Unix() {
		return errors.New("risk_lease_lost")
	}
	return nil
}

// Serializes budget reservation with the call and queue transition. Unknown
// procurement holds remain reserved even if the gateway refunded its customer.
func OmniRiskReserve(call *OmniRiskCall, owner string, epoch int64) error {
	if call.Reserved <= 0 || call.Reserved > OmniRiskDailyMicros {
		return errors.New("risk_budget_invalid")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := OmniRiskFence(tx, owner, epoch); err != nil {
			return err
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&OmniRiskDay{Day: call.Day}).Error; err != nil {
			return err
		}
		var day OmniRiskDay
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&day, "day = ?", call.Day).Error; err != nil {
			return err
		}
		if day.Reserved+day.Spent+call.Reserved > OmniRiskDailyMicros {
			return errors.New("risk_budget_exhausted")
		}
		if err := tx.Create(call).Error; err != nil {
			return err
		}
		if call.UserID > 0 {
			q := tx.Model(&OmniRiskSubject{}).Where("user_id = ? AND fingerprint = ? AND state = ?", call.UserID, call.Fingerprint, "pending").Updates(map[string]any{"state": "inflight", "call_id": call.ID})
			if q.Error != nil {
				return q.Error
			}
			if q.RowsAffected != 1 {
				return errors.New("risk_subject_changed")
			}
		}
		return tx.Model(&day).Update("reserved", day.Reserved+call.Reserved).Error
	})
}
func OmniRiskSettle(id string, actual int64, zeroProcurement bool) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var c OmniRiskCall
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&c, "id = ?", id).Error; err != nil {
			return err
		}
		if c.State == "settled" || c.State == "not_sent" {
			return nil
		}
		if actual < 0 {
			return errors.New("risk_negative_cost")
		}
		var d OmniRiskDay
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&d, "day = ?", c.Day).Error; err != nil {
			return err
		}
		if actual == 0 && !zeroProcurement {
			return tx.Model(&c).Updates(map[string]any{"state": "unknown", "error_code": "procurement_unconfirmed"}).Error
		}
		state := "settled"
		if actual == 0 {
			state = "not_sent"
		}
		if err := tx.Model(&c).Updates(map[string]any{"state": state, "actual": actual, "finished_at": time.Now().Unix()}).Error; err != nil {
			return err
		}
		return tx.Model(&d).Updates(map[string]any{"reserved": d.Reserved - c.Reserved, "spent": d.Spent + actual}).Error
	})
}
func OmniRiskEnqueue(tx *gorm.DB, id, caseID, kind, content string) error {
	now := time.Now().Unix()
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&OmniRiskOutbox{ID: id, CaseID: caseID, Kind: kind, Content: content, State: "pending", CreatedAt: now, NextAt: now}).Error
}
func OmniRiskOperational(key string, active bool) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&OmniRiskIncident{Key: key}).Error; err != nil {
			return err
		}
		var row OmniRiskIncident
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, "key = ?", key).Error; err != nil {
			return err
		}
		if row.Active == active && (!active || time.Now().Unix()-row.UpdatedAt < 7200) {
			return nil
		}
		row.Active = active
		row.Generation++
		row.UpdatedAt = time.Now().Unix()
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		suffix := "异常"
		if !active {
			suffix = "已恢复"
		}
		content := "OmniAPI 风控系统" + suffix + "\n原因码：" + key + "\n未完成不代表无风险。请登录查看运行状态。\nhttps://omniapi.biz/risk-control"
		id := key + ":" + strconv.FormatInt(row.Generation, 10) + ":" + suffix
		return OmniRiskEnqueue(tx, id, "", "operation", content)
	})
}
