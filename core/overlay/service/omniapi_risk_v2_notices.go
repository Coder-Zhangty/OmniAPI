package service

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
	"unicode/utf8"
)

func riskV2LegacyNotices() error {
	if !model.DB.Migrator().HasTable(&model.OmniNotice{}) {
		return nil
	}
	var cursor model.OmniRiskCursorV2
	if e := model.DB.Where("key = ?", "private_notice_backfill").Find(&cursor).Error; e != nil {
		return e
	}
	var notices []model.OmniNotice
	q := model.DB.Where("key LIKE ? AND (key LIKE ? OR key LIKE ? OR title LIKE ? OR title LIKE ?)", "announcement:customer:%", "%risk%", "%registration-%", "%风控%", "%封禁%")
	q = q.Where("created_at > ? OR (created_at = ? AND key > ?)", cursor.Value, cursor.Value, cursor.Text)
	if e := q.Order("created_at,key").Limit(500).Find(&notices).Error; e != nil {
		return e
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		for _, n := range notices {
			// Legacy read flags did not prove popup presentation. Start a fresh receipt.
			item := model.OmniRiskNoticeV2{ID: "legacy:" + model.OmniRiskDigest([]byte(n.Key)), UserID: n.UserID, LegacyRead: n.Read, CaseID: "LEGACY-" + model.OmniRiskDigest([]byte(n.Key))[:16], Title: n.Title, Body: n.Body, CreatedAt: n.CreatedAt}
			if e := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&item).Error; e != nil {
				return e
			}
			cursor.Value = n.CreatedAt
			cursor.Text = n.Key
		}
		if len(notices) == 0 {
			return nil
		}
		cursor.Key = "private_notice_backfill"
		cursor.UpdatedAt = time.Now().Unix()
		return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&cursor).Error
	})
}

// Earlier owner-authorized SSH operations record actor user_id=0 and put the
// actual target in structured metadata. Never infer enforcement from money.
func riskV2HandledTimes(ctx context.Context) (map[int]int64, error) {
	var rows []struct {
		UserID       int
		CreatedAt    int64
		EncodedOther string
	}
	e := riskRead(model.DB, ctx, func(tx *gorm.DB) error {
		return tx.Table("audit_logs").Select("user_id,created_at,COALESCE(CAST(other AS TEXT),'{}') AS encoded_other").Where("success = ? AND action IN ?", true, []string{"user.risk_suspension", "user.risk_restored", "risk.v2.hold", "risk.v2.restore"}).Order("id").Limit(20001).Find(&rows).Error
	})
	if e != nil {
		return nil, e
	}
	if len(rows) > 20000 {
		return nil, fmt.Errorf("risk_handled_history_overflow")
	}
	result := map[int]int64{}
	for _, r := range rows {
		var metadata struct {
			Target int `json:"target_user_id"`
		}
		if json.Unmarshal([]byte(r.EncodedOther), &metadata) != nil {
			return nil, fmt.Errorf("risk_handled_metadata_invalid")
		}
		uid := r.UserID
		if metadata.Target > 0 {
			uid = metadata.Target
		}
		if uid > 0 {
			result[uid] = max(result[uid], r.CreatedAt)
		}
	}
	return result, nil
}

func riskV2LegacyCaseHasFreshEvidence(c model.OmniRiskCase) (bool, error) {
	var last int64
	if e := model.DB.Model(&model.OmniRiskCase{}).Where("user_id = ? AND status = ? AND verdict = ?", c.UserID, "closed", "false_positive").Select("COALESCE(MAX(closed_at),0)").Scan(&last).Error; e != nil {
		return false, e
	}
	if last == 0 {
		return true, nil
	}
	var pack OmniRiskPack
	if json.Unmarshal([]byte(c.Evidence), &pack) != nil {
		return false, nil
	}
	for _, e := range pack.Evidence {
		switch e.Kind {
		case "verified_registration_event":
			if e.Values["registered_at"] > last {
				return true, nil
			}
		case "registration_burst", "chain_registration_burst", "known_initial_registration_credit", "native_invitation_rewards", "chain_native_rewards", "invitation_benefit_pattern", "related_registration_benefit_claims", "referral":
			if e.Values["last_at"] > last {
				return true, nil
			}
		}
	}
	return false, nil
}

func riskV2RegistrationBackfill() error {
	var cursor model.OmniRiskCursorV2
	if e := model.DB.Where("key = ?", "registrations").Find(&cursor).Error; e != nil {
		return e
	}
	var events []model.OmniRiskAuthEvent
	if e := model.DB.Where("id > ? AND created_at >= ? AND action = ? AND success = ?", cursor.Value, time.Now().Unix()-1800, "registration.success", true).Order("id").Limit(500).Find(&events).Error; e != nil {
		return e
	}
	missing := false
	for _, e := range events {
		if e.NetworkTag == "" || e.BrowserTag == "" {
			missing = true
		} else {
			row := model.OmniRiskEventV2{Key: fmt.Sprint("registration.success:", e.UserID), UserID: e.UserID, Kind: "registration.success", CreatedAt: e.CreatedAt, Network: e.NetworkTag, Browser: e.BrowserTag, Quota: e.GrantedQuota}
			if err := model.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
				return err
			}
		}
		cursor.Value = int64(e.ID)
	}
	if len(events) > 0 {
		cursor.Key = "registrations"
		cursor.UpdatedAt = time.Now().Unix()
		if e := model.DB.Clauses(clause.OnConflict{UpdateAll: true}).Create(&cursor).Error; e != nil {
			return e
		}
	}
	var n int64
	if e := model.DB.Model(&model.OmniRiskAuthEvent{}).Where("action = ? AND success = ? AND created_at >= ? AND (network_tag = ? OR browser_tag = ?)", "registration.success", true, time.Now().Unix()-1800, "", "").Count(&n).Error; e != nil {
		return e
	}
	missing = n > 0
	return model.OmniRiskOperational("registration_metadata_missing", missing)
}

func riskWeComBounded(content string) string {
	if len(content) <= 3400 {
		return content
	}
	prefix := content[:3250]
	for !utf8.ValidString(prefix) {
		prefix = prefix[:len(prefix)-1]
	}
	return prefix + "\n消息较长，请到风控中心查看完整记录。\nhttps://omniapi.biz/risk-control"
}
