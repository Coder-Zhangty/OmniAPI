package model

import (
	"encoding/json"
	"slices"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

const OmniTrialOption = "OmniAPINewAccountTrial"

type OmniTrialLimits struct {
	Enabled       bool
	Fallback      bool
	RequestMicros int64
	TotalMicros   int64
	Concurrency   int
}

type OmniTrialRejection struct {
	Code            string
	TryBackup       bool
	RemainingMicros int64
	EndsAt          int64
}

func (e *OmniTrialRejection) Error() string { return e.Code }

func OmniTrialLimitsFor(uid int) OmniTrialLimits {
	common.OptionMapRWMutex.RLock()
	raw := common.OptionMap[OmniTrialOption]
	common.OptionMapRWMutex.RUnlock()
	var cfg struct {
		Mode    string `json:"mode"`
		UserIDs []int  `json:"user_ids"`
	}
	if json.Unmarshal([]byte(raw), &cfg) != nil {
		return OmniTrialLimits{}
	}
	switch cfg.Mode {
	case "safe":
		return OmniTrialLimits{Enabled: true, RequestMicros: 1000000, TotalMicros: 1000000, Concurrency: 1}
	case "pilot":
		if !slices.Contains(cfg.UserIDs, uid) {
			return OmniTrialLimits{}
		}
	case "all":
	default:
		return OmniTrialLimits{}
	}
	return OmniTrialLimits{Enabled: true, Fallback: true, RequestMicros: 5000000, TotalMicros: 10000000, Concurrency: 10}
}

func OmniNewUserProtected(p OmniRiskPolicyV2, user User, control OmniRiskControl, paid, requestScoped bool) bool {
	restoredTrial := requestScoped && (control.Reason == "quote_unavailable" || control.Reason == "new_user_budget")
	return p.NewUserProtection && !p.Shadow && !paid && user.Role == common.RoleCommonUser &&
		user.CreatedAt > time.Now().Unix()-86400 && (control.State != "restored" || restoredTrial)
}

func omniTrialExposures(tx *gorm.DB, uid int, start int64) *gorm.DB {
	return tx.Model(&OmniRiskExposure{}).Where("user_id = ? AND ((state IN ?) OR (created_at >= ? AND state = ?))",
		uid, []string{"reserved", "sending", "unknown"}, start, "settled")
}

func omniTrialExposureTotal(q *gorm.DB) (int64, error) {
	var total int64
	err := q.Session(&gorm.Session{}).Select("COALESCE(SUM(CASE WHEN state = 'settled' THEN actual ELSE reserved END),0)").Scan(&total).Error
	return total, err
}

type OmniTrialStatus struct {
	Active          bool  `json:"active"`
	RequestMicros   int64 `json:"request_micros,omitempty"`
	TotalMicros     int64 `json:"total_micros,omitempty"`
	RemainingMicros int64 `json:"remaining_micros"`
	Concurrency     int   `json:"concurrency,omitempty"`
	EndsAt          int64 `json:"ends_at,omitempty"`
}

func OmniTrialStatusFor(uid int) (OmniTrialStatus, error) {
	limits := OmniTrialLimitsFor(uid)
	if !limits.Enabled || !OmniRiskV2Ready() {
		return OmniTrialStatus{}, nil
	}
	p, err := OmniRiskV2Policy()
	if err != nil {
		return OmniTrialStatus{}, err
	}
	var user User
	if err := DB.Select("id", "status", "role", "created_at").First(&user, uid).Error; err != nil {
		return OmniTrialStatus{}, err
	}
	if user.Status != common.UserStatusEnabled || !OmniNewUserProtected(p, user, OmniRiskControl{}, false, true) {
		return OmniTrialStatus{}, nil
	}
	control, err := OmniRiskReadControl(DB, uid)
	if err != nil {
		return OmniTrialStatus{}, err
	}
	if riskV2Held(control.State) || control.State == "guard" || !OmniNewUserProtected(p, user, control, false, true) {
		return OmniTrialStatus{}, nil
	}
	paid, err := OmniRiskPaid(DB, uid)
	if err != nil || paid {
		return OmniTrialStatus{}, err
	}
	total, err := omniTrialExposureTotal(omniTrialExposures(DB, uid, user.CreatedAt))
	if err != nil {
		return OmniTrialStatus{}, err
	}
	return OmniTrialStatus{Active: true, RequestMicros: limits.RequestMicros, TotalMicros: limits.TotalMicros,
		RemainingMicros: max(0, limits.TotalMicros-total), Concurrency: limits.Concurrency, EndsAt: user.CreatedAt + 86400}, nil
}
