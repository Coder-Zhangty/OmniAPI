package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"sort"
	"strings"
	"time"
)

// No request/response text or key material is inspected. A deviation alone
// never triggers a suspension; combined anomalies only request protection.
func OmniRiskConsumptionSignalsV2(uid int) error {
	now := time.Now().Unix()
	var operation OmniRiskEventV2
	e := DB.Where("user_id = ? AND created_at >= ? AND kind IN ?", uid, now-600, []string{"token.create", "token.view"}).Order("id DESC").First(&operation).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return nil
	}
	if e != nil {
		return e
	}
	reasons := []string{}
	var origins []struct {
		TokenID            int
		Network            string
		FirstAt, LastAt, N int64
	}
	if e = DB.Model(&OmniRiskEventV2{}).Select("token_id,network,MIN(created_at) AS first_at,MAX(created_at) AS last_at,COUNT(*) AS n").Where("user_id = ? AND kind = ? AND created_at >= ? AND token_id > 0 AND network <> ''", uid, "model.admission", now-300).Group("token_id,network").Having("COUNT(*) >= 3").Order("token_id,first_at").Limit(10001).Find(&origins).Error; e != nil {
		return e
	}
	if len(origins) > 10000 {
		return errors.New("risk_source_group_overflow")
	}
	lastToken := 0
	var lastEnd int64
	for _, r := range origins {
		if r.TokenID == lastToken && r.FirstAt <= lastEnd+60 {
			reasons = append(reasons, "key_sources_with_sensitive_action")
			break
		}
		lastToken = r.TokenID
		lastEnd = r.LastAt
	}
	var current struct{ N, Amount int64 }
	if e = DB.Model(&OmniRiskEventV2{}).Select("COUNT(*) AS n,COALESCE(SUM(quota),0) AS amount").Where("user_id = ? AND kind = ? AND created_at >= ?", uid, "model.settled", now-300).Scan(&current).Error; e != nil {
		return e
	}
	if current.N >= 10 {
		var buckets []struct{ Bucket, N, Amount int64 }
		q := DB.Model(&OmniRiskEventV2{}).Select("CAST(created_at / 300 AS BIGINT) AS bucket,COUNT(*) AS n,SUM(quota) AS amount").Where("user_id = ? AND kind = ? AND created_at >= ? AND created_at < ?", uid, "model.settled", now-7*86400, now-1800)
		q = q.Where("NOT EXISTS (SELECT 1 FROM omni_risk_cases c WHERE c.user_id = ? AND c.verdict <> ? AND omni_risk_event_v2.created_at >= c.created_at - 1800 AND (c.closed_at = 0 OR omni_risk_event_v2.created_at <= c.closed_at))", uid, "false_positive")
		if e = q.Group("CAST(created_at / 300 AS BIGINT)").Find(&buckets).Error; e != nil {
			return e
		}
		var n int64
		days := map[int64]bool{}
		values := []int64{}
		for _, b := range buckets {
			n += b.N
			days[b.Bucket/288] = true
			values = append(values, b.Amount)
		}
		if n >= 30 && len(days) >= 3 {
			sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
			baseline := values[(len(values)*95+99)/100-1]
			if baseline > 0 && current.Amount > baseline*4 {
				var fresh int64
				e = DB.Model(&OmniRiskEventV2{}).Where("user_id = ? AND kind = ? AND created_at >= ? AND network <> ''", uid, "model.settled", now-300).Where("NOT EXISTS (SELECT 1 FROM omni_risk_event_v2 old WHERE old.user_id = ? AND old.kind = ? AND old.created_at >= ? AND old.created_at < ? AND old.network = omni_risk_event_v2.network)", uid, "model.settled", now-7*86400, now-1800).Count(&fresh).Error
				if e != nil {
					return e
				}
				if fresh > 0 {
					reasons = append(reasons, "spend_deviation_new_source_sensitive_action")
				}
			}
		}
	}
	if len(reasons) == 0 {
		return nil
	}
	p, e := OmniRiskV2Policy()
	if e != nil {
		return e
	}
	sort.Strings(reasons)
	fp := OmniRiskDigest([]byte(fmt.Sprint(uid, reasons, operation.ID)))
	return OmniRiskApplyV2(uid, fp, "observe:"+reasons[0], p.AutoHold && !p.Shadow, 0, "")
}

func riskV2HistoricalBaseline(tx *gorm.DB, uid int, before int64) (int64, int) {
	fallback := func() (int64, int) { return 2000000, 1 }
	if !tx.Migrator().HasTable(&OmniAttempt{}) {
		return fallback()
	}
	var rows []Log
	if e := tx.Select("request_id", "created_at").Where("user_id = ? AND type = 2 AND created_at >= ? AND created_at < ?", uid, before-7*86400, before-1800).Limit(20001).Find(&rows).Error; e != nil || len(rows) > 20000 {
		return fallback()
	}
	var cases []OmniRiskCase
	if e := tx.Where("user_id = ? AND created_at >= ? AND verdict <> ?", uid, before-8*86400, "false_positive").Find(&cases).Error; e != nil {
		return fallback()
	}
	requests := map[string]int64{}
	ids := []string{}
	for _, r := range rows {
		if r.RequestId == "" {
			continue
		}
		bad := false
		for _, c := range cases {
			end := c.ClosedAt
			if end == 0 {
				end = before
			}
			if r.CreatedAt >= c.CreatedAt-1800 && r.CreatedAt <= end {
				bad = true
				break
			}
		}
		if bad {
			continue
		}
		if _, ok := requests[r.RequestId]; !ok {
			requests[r.RequestId] = r.CreatedAt
			ids = append(ids, r.RequestId)
		}
	}
	if len(ids) < 30 {
		return fallback()
	}
	var attempts []OmniAttempt
	if e := tx.Where("request_id IN ?", ids).Find(&attempts).Error; e != nil {
		return fallback()
	}
	costs := map[string]decimal.Decimal{}
	unknown := map[string]bool{}
	for _, a := range attempts {
		if a.Outcome == "skipped_before_send" || a.Outcome == "invalid_model_mapping" {
			continue
		}
		if a.CostUSD <= 0 || a.Outcome != "completed" {
			unknown[a.RequestID] = true
			continue
		}
		costs[a.RequestID] = costs[a.RequestID].Add(decimal.NewFromFloat(a.CostUSD))
	}
	var raw json.RawMessage = GetOmniPrices().RiskFX
	fx, e := decimal.NewFromString(strings.Trim(string(raw), "\""))
	if e != nil || fx.LessThan(decimal.NewFromInt(1)) || fx.GreaterThan(decimal.NewFromInt(20)) {
		return fallback()
	}
	buckets := map[int64]int64{}
	days := map[int64]bool{}
	n := 0
	for id, cost := range costs {
		if unknown[id] || !cost.IsPositive() {
			continue
		}
		at := requests[id]
		buckets[at/300] += cost.Mul(fx).Mul(decimal.NewFromInt(1000000)).Ceil().IntPart()
		days[at/86400] = true
		n++
	}
	if n < 30 || len(days) < 3 {
		return fallback()
	}
	values := []int64{}
	for _, cost := range buckets {
		values = append(values, cost)
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	return min(int64(10000000), max(int64(2000000), values[(len(values)*95+99)/100-1]*2)), 3
}
