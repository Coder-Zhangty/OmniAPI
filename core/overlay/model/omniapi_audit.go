package model

import (
	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"time"
)

// No prompt, answer, credentials or upstream error body enters this ledger.
type OmniAttempt struct {
	ID             uint    `json:"-" gorm:"primaryKey"`
	RequestID      string  `json:"-" gorm:"index;size:128"`
	ChannelID      int     `json:"channel_id"`
	Model          string  `json:"model"`
	Ordinal        int     `json:"ordinal"`
	Continuation   bool    `json:"continuation"`
	FirstMillis    int64   `json:"first_millis"`
 CommitMillis int64 `json:"commit_millis"`
 Buffered bool `json:"buffered"`
	DurationMillis int64   `json:"duration_millis"`
	Outcome        string  `json:"outcome"`
	InputTokens    int     `json:"input_tokens"`
	OutputTokens   int     `json:"output_tokens"`
	CostUSD        float64 `json:"cost_usd"`
	CostEstimated  bool    `json:"cost_estimated"`
	CreatedAt      int64   `json:"created_at"`
}

func OmniRecordAttempt(a OmniAttempt) {
	a.CreatedAt = time.Now().Unix()
	if DB != nil {
		if err := DB.Create(&a).Error; err != nil {
			common.SysError("OmniAPI attempt ledger: " + err.Error())
		}
	}
}
func OmniRequestAttempts(id string) []OmniAttempt {
	common.OptionMapRWMutex.RLock()
	configured := common.OptionMap["OmniAPIRouting"] != ""
	common.OptionMapRWMutex.RUnlock()
	if !configured {
		return []OmniAttempt{}
	}
	rows := []OmniAttempt{}
	if DB != nil {
		DB.Where("request_id = ?", id).Order("ordinal").Find(&rows)
	}
	return rows
}
func OmniPublicAttempts(id string) []map[string]any {
	rows := []map[string]any{}
	for _, a := range OmniRequestAttempts(id) {
		rows = append(rows, map[string]any{"channel_id": a.ChannelID, "ordinal": a.Ordinal, "continuation": a.Continuation, "first_millis": a.FirstMillis, "commit_millis":a.CommitMillis, "buffered":a.Buffered, "outcome": a.Outcome})
	}
	return rows
}
func OmniRequestRecord(id string) (OmniReservation, error) {
	common.OptionMapRWMutex.RLock()
	configured := common.OptionMap["OmniAPICommercial"] != ""
	common.OptionMapRWMutex.RUnlock()
	if !configured {
		return OmniReservation{}, gorm.ErrRecordNotFound
	}
	var r OmniReservation
	err := DB.First(&r, "request_id = ?", id).Error
	return r, err
}
func (s *OmniSubscription) Project(now int64) error { return s.advance(now) }
func OmniSubscriptionView(uid int) map[string]any {
	common.OptionMapRWMutex.RLock()
	configured := common.OptionMap["OmniAPICommercial"] != ""
	common.OptionMapRWMutex.RUnlock()
	if !configured {
		return nil
	}
	var s OmniSubscription
	if DB.First(&s, "user_id = ?", uid).Error != nil {
		return nil
	}
	if s.Project(time.Now().Unix()) != nil {
		return nil
	}
	return map[string]any{"subscription": s, "five_reset_at": s.Start + (s.FiveEpoch+1)*18000, "week_reset_at": s.Start + (s.WeekEpoch+1)*604800, "billing_version": 2}
}
