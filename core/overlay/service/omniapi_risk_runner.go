package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

var omniRiskOnce sync.Once
var omniRiskDeliveryMu sync.Mutex
var omniRiskWecomDo = func(request *http.Request) (*http.Response, error) {
	client := http.Client{Timeout: 8 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	return client.Do(request)
}

func StartOmniRiskControl() {
	StartOmniRiskV2()
	omniRiskOnce.Do(func() {
		go func() {
			owner := "risk-" + common.NodeName + "-" + common.GetRandomString(12)
			for {
				OmniRiskTick(owner)
				time.Sleep(15 * time.Second)
			}
		}()
	})
}

func OmniRiskTick(owner string) {
	s, err := model.GetOmniRiskSettings()
	if err != nil {
		return
	}
	secrets, err := model.LoadOmniRiskSecrets()
	if err != nil {
		return
	}
	epoch, acquired, err := model.OmniRiskAcquire(owner, time.Now().Unix())
	if err != nil || !acquired {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !model.OmniRiskRenew(owner, epoch, time.Now().Unix()) {
					cancel()
					return
				}
				// Local prechecks must continue while a slow paid review is in
				// flight; they share the existing fenced lease, never the budget.
				if current, err := model.GetOmniRiskSettings(); err == nil && current.Enabled {
					omniRiskRunLocalPrecheck(ctx, current, secrets, owner, epoch)
				}
			}
		}
	}()
	defer func() {
		cancel()
		<-stopped
		model.DB.Model(&model.OmniRiskLease{}).Where("key = ? AND owner = ? AND epoch = ?", "worker", owner, epoch).Update("expires", 0)
	}()
	if s.Enabled {
		omniRiskRunLocalPrecheck(ctx, s, secrets, owner, epoch)
		model.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.OmniRiskRun{ID: omniRiskScheduledKey(time.Now().Unix()), Kind: "scheduled", State: "queued", StartedAt: time.Now().Unix(), PolicyVersion: OmniRiskPolicyVersion, PromptSHA: model.OmniRiskDigest([]byte(OmniRiskPrompt))})
	}
	// Calls whose sender died are not reissued by the new lease holder.
	model.DB.Model(&model.OmniRiskRun{}).Where("state = ? AND epoch <> ?", "running", epoch).Updates(map[string]any{"state": "interrupted", "error_code": "risk_previous_sender_interrupted", "finished_at": time.Now().Unix()})
	var pending []model.OmniRiskCall
	model.DB.Where("state IN ?", []string{"sending", "reserved", "unknown"}).Order("created_at").Limit(100).Find(&pending)
	for _, call := range pending {
		_ = OmniRiskReconcileCall(call.ID, s)
		if call.UserID > 0 {
			model.DB.Model(&model.OmniRiskSubject{}).Where("call_id = ? AND state = ?", call.ID, "inflight").Update("state", "unknown")
		}
	}
	// A paid invalid report can finish settling after its response was rejected.
	// Classify that result as failed without reissuing it or releasing unknown cost.
	model.DB.Model(&model.OmniRiskSubject{}).Where("state = ? AND call_id IN (?)", "unknown", model.DB.Model(&model.OmniRiskCall{}).Select("id").Where("state = ? AND error_code LIKE ?", "settled", "risk_output_%")).Update("state", "failed")
	var run model.OmniRiskRun
	q := model.DB.Where("state = ?", "queued")
	if !s.Enabled {
		q = q.Where("kind = ?", "acceptance")
	}
	if q.Order("started_at, id").First(&run).Error == nil {
		if err := OmniRiskExecute(ctx, &run, s, secrets, owner, epoch); err != nil {
			common.SysError("Risk review run incomplete: " + riskSafeError(err))
		}
	}
	if s.Notifications {
		OmniRiskReminders()
		OmniRiskDeliver(ctx, secrets, owner, epoch)
	}
	OmniRiskPrune(time.Now().Unix())
}

func omniRiskScheduledKey(now int64) string { return fmt.Sprintf("half-hour:%d", now/1800) }

func omniRiskRunLocalPrecheck(ctx context.Context, s model.OmniRiskSettings, secrets model.OmniRiskSecrets, owner string, epoch int64) {
	if err := omniRiskFastScan(ctx, s, secrets, owner, epoch); err != nil {
		common.SysError("Risk local precheck incomplete: " + riskSafeError(err))
		_ = model.OmniRiskOperational("local_precheck_incomplete", true)
	}
	if s.Notifications {
		OmniRiskDeliver(ctx, secrets, owner, epoch)
	}
}

// A cheap local precheck is independent of the paid-model budget. It is a
// provisional notification only: never changes login, invitations or money.
func omniRiskFastScan(ctx context.Context, s model.OmniRiskSettings, secrets model.OmniRiskSecrets, owner string, epoch int64) error {
	now := time.Now().Unix()
	claimed := false
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := model.OmniRiskFence(tx, owner, epoch); err != nil {
			return err
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.OmniRiskLease{Key: "fast_scan"}).Error; err != nil {
			return err
		}
		q := tx.Model(&model.OmniRiskLease{}).Where("key = ? AND expires <= ?", "fast_scan", now).Update("expires", now+60)
		claimed = q.RowsAffected == 1
		return q.Error
	}); err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	packs, _, err := OmniRiskCollect(ctx, s, secrets, now)
	if err != nil {
		return err
	}
	if _, err := OmniRiskStorePacks(packs, owner, epoch, now); err != nil {
		return err
	}
	model.DB.Model(&model.OmniRiskSettings{}).Where("id = ?", 1).Update("collected_at", now)
	for _, p := range packs {
		if err := omniRiskLocalPrealert(p, owner, epoch, s.Notifications); err != nil {
			return err
		}
	}
	_ = model.OmniRiskOperational("local_precheck_incomplete", false)
	return nil
}

func omniRiskLocalPrealert(p OmniRiskPack, owner string, epoch int64, notifications bool) error {
	var burst, reward, pattern *OmniRiskEvidence
	for i := range p.Evidence {
		e := &p.Evidence[i]
		if e.Values["inviter_id"] != int64(p.SubjectID) {
			continue
		}
		switch e.Kind {
		case "invitation_registration_burst":
			burst = e
		case "native_referral":
			reward = e
		case "invitation_benefit_pattern":
			pattern = e
		}
	}
	isChain := false
	for i := range p.Evidence {
		e := &p.Evidence[i]
		if e.Values["inviter_id"] != int64(p.SubjectID) {
			continue
		}
		switch e.Kind {
		case "chain_registration_burst":
			burst = e
			isChain = true
		case "chain_native_rewards":
			reward = e
		case "chain_benefit_pattern":
			pattern = e
		}
	}
	if burst == nil || reward == nil || pattern == nil || int(burst.Values["inviter_id"]) != p.SubjectID || burst.Values["accounts_30min"] < 3 || pattern.Values["known_registration_gifts"] < 3 {
		return nil
	}
	third, fourth := "rapid_users_15m", "shared_network_accounts_not_identity"
	if isChain {
		third, fourth = "same_browser_accounts_not_identity", "reward_counter_gaps_not_proven_transfers"
		if burst.Values["chain_edges"] < 2 || reward.Values["events"] < 2 || pattern.Values[third] < 3 || pattern.Values[fourth] < 2 {
			return nil
		}
	} else if reward.Values["events"] < 3 || pattern.Values[third] < 2 || pattern.Values[fourth] < 2 {
		return nil
	}
	if p.PriorHandledAt >= pattern.Values["last_at"] {
		return nil
	}
	r := OmniRiskReport{SubjectID: p.SubjectID, Severity: "medium", EvidenceIDs: []string{burst.ID, reward.ID, pattern.ID}, Facts: []OmniRiskFact{{burst.ID, "accounts_30min", burst.Values["accounts_30min"]}, {reward.ID, "events", reward.Values["events"]}, {pattern.ID, third, pattern.Values[third]}, {pattern.ID, fourth, pattern.Values[fourth]}}, Suspicious: []string{"同一邀请链短时注册、重复发奖并快速调用，且存在网络关联"}, Counter: []string{"可能为家庭、公司或公开推广带来的正常试用"}, Uncertainties: []string{"网络关联不能证明同一身份；累计奖励不等于本期入账或采购损失"}, Actions: []string{"manual_review", "review_reward_ledger", "consider_invitation_limit"}, Summary: "本地规则预警（待人工复核）：注册、邀请奖励与快速使用形成组合疑点，尚未确认违规"}
	if isChain {
		r.Suspicious = []string{"连续逐级注册形成多层邀请，反复产生奖励并出现多个奖励计数差额，浏览器信息相关联"}
		r.Uncertainties = []string{"相同浏览器不等于同一设备或身份；奖励计数差额不等于已核实的转出或采购损失"}
		r.Summary = "本地规则预警（待人工复核）：短时逐级邀请、重复奖励与浏览器关联形成组合疑点，尚未确认违规"
	}
	data, _ := json.Marshal(r)
	if _, err := OmniRiskDecodeReport(data, p); err != nil {
		return err
	}
	material, _ := json.Marshal(map[string]any{"facts": r.Facts, "members": burst.Users, "first_at": burst.Values["first_at"], "last_at": burst.Values["last_at"]})
	fingerprint := "rule:" + model.OmniRiskDigest(material)
	return model.DB.Transaction(func(tx *gorm.DB) error {
		if err := model.OmniRiskFence(tx, owner, epoch); err != nil {
			return err
		}
		group := fmt.Sprintf("inviter:%d", p.SubjectID)
		if isChain {
			group = fmt.Sprintf("chain:%d", p.SubjectID)
		}
		var row model.OmniRiskCase
		err := tx.Where("group_key = ? AND status <> ?", group, "closed").Order("created_at DESC").First(&row).Error
		if err != nil && err != gorm.ErrRecordNotFound {
			return err
		}
		// Never downgrade an AI or manually reviewed case with a local prealert.
		if row.ID != "" && (row.Fingerprint == fingerprint || !strings.HasPrefix(row.Fingerprint, "rule:")) {
			return nil
		}
		if row.ID == "" {
			var closed model.OmniRiskCase
			if tx.Where("group_key = ? AND status = ?", group, "closed").Order("closed_at DESC").First(&closed).Error == nil && closed.ClosedAt >= pattern.Values["last_at"] {
				return nil
			}
		}
		now := time.Now().Unix()
		if row.ID == "" {
			row = model.OmniRiskCase{ID: "R-" + common.GetRandomString(20), UserID: p.SubjectID, GroupKey: group, Status: "open", CreatedAt: now}
		}
		row.Fingerprint = fingerprint
		row.Severity = "medium"
		row.Report = string(data)
		evidence, _ := json.Marshal(p)
		row.Evidence = string(evidence)
		row.UpdatedAt = now
		row.Revision++
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		if err := tx.Create(&model.OmniRiskAction{CreatedAt: now, Action: "local_prealert", CaseID: row.ID, Note: "本地组合规则预警，未调用模型、未执行账号或资金处置"}).Error; err != nil {
			return err
		}
		if notifications {
			return model.OmniRiskEnqueue(tx, fmt.Sprintf("prealert:%s:%d", row.ID, row.Revision), row.ID, "case", OmniRiskNotification(row, r))
		}
		return nil
	})
}

func riskSafeError(err error) string {
	if err == nil {
		return ""
	}
	v := err.Error()
	if strings.HasPrefix(v, "risk_") && len(v) < 100 && !strings.ContainsAny(v, " \n:/") {
		return v
	}
	return "risk_internal_error"
}
func OmniRiskExecute(ctx context.Context, run *model.OmniRiskRun, s model.OmniRiskSettings, secrets model.OmniRiskSecrets, owner string, epoch int64) (result error) {
	run.State = "running"
	run.Owner = owner
	run.Epoch = epoch
	run.StartedAt = time.Now().Unix()
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := model.OmniRiskFence(tx, owner, epoch); err != nil {
			return err
		}
		q := tx.Model(&model.OmniRiskRun{}).Where("id = ? AND state = ?", run.ID, "queued").Updates(map[string]any{"state": run.State, "owner": owner, "epoch": epoch, "started_at": run.StartedAt})
		if q.Error != nil {
			return q.Error
		}
		if q.RowsAffected != 1 {
			return errors.New("risk_run_claim_lost")
		}
		return nil
	}); err != nil {
		return err
	}
	defer func() {
		run.FinishedAt = time.Now().Unix()
		run.State = "completed"
		run.ErrorCode = riskSafeError(result)
		model.DB.Model(&model.OmniRiskSubject{}).Where("state IN ?", []string{"pending", "inflight", "unknown", "failed"}).Count(&run.Pending)
		if result != nil {
			run.State = "incomplete"
		}
		if run.Pending > 0 && run.Kind != "acceptance" && result == nil {
			run.State = "partial"
			run.ErrorCode = "risk_review_backlog"
		}
		model.DB.Model(&model.OmniRiskRun{}).Where("id = ? AND owner = ? AND epoch = ?", run.ID, owner, epoch).Updates(map[string]any{"state": run.State, "finished_at": run.FinishedAt, "collected": run.Collected, "reviewed": run.Reviewed, "pending": run.Pending, "error_code": run.ErrorCode})
		if run.Kind != "acceptance" {
			_ = model.OmniRiskOperational("review_incomplete", run.State != "completed")
			_ = model.OmniRiskOperational("daily_budget_exhausted", run.ErrorCode == "risk_budget_exhausted")
			model.DB.Model(&model.OmniRiskSettings{}).Where("id = 1").Update("last_error", run.ErrorCode)
		}
	}()
	if run.Kind == "acceptance" {
		pack := OmniRiskPack{SubjectID: 0, Related: []int{}, Evidence: []OmniRiskEvidence{}, Missing: []string{"synthetic_acceptance_only"}, Overview: map[string]int64{"customer_accounts": 0, "new_accounts_30d": 0, "synthetic_acceptance": 1}}
		report, _, err := OmniRiskModelReview(ctx, pack, run.ID, owner, epoch, s, secrets)
		if err != nil {
			return err
		}
		if report.Severity != "none" && report.Severity != "observe" {
			return errors.New("risk_acceptance_verdict_invalid")
		}
		run.Reviewed = 1
		return nil
	}
	packs, overview, err := OmniRiskCollect(ctx, s, secrets, time.Now().Unix())
	if err != nil {
		return err
	}
	run.Collected, err = OmniRiskStorePacks(packs, owner, epoch, time.Now().Unix())
	if err != nil {
		return err
	}
	model.DB.Model(&model.OmniRiskSettings{}).Where("id = 1").Update("collected_at", time.Now().Unix())
	var subjects []model.OmniRiskSubject
	if err := model.DB.Where("state = ?", "pending").Order("priority DESC, changed_at, user_id").Limit(6).Find(&subjects).Error; err != nil {
		return err
	}
	if len(subjects) == 0 {
		pack := OmniRiskPack{SubjectID: 0, Related: []int{}, Evidence: []OmniRiskEvidence{}, Missing: []string{"overview_only_no_account_verdicts"}, Overview: overview}
		_, _, err = OmniRiskModelReview(ctx, pack, run.ID, owner, epoch, s, secrets)
		if err != nil {
			return err
		}
		run.Reviewed = 1
	}
	for _, sub := range subjects {
		current, err := model.GetOmniRiskSettings()
		if err != nil || !current.Enabled {
			return errors.New("risk_paused")
		}
		if ctx.Err() != nil {
			return errors.New("risk_run_cancelled")
		}
		var pack OmniRiskPack
		if json.Unmarshal([]byte(sub.Evidence), &pack) != nil {
			model.DB.Model(&model.OmniRiskSubject{}).Where("user_id = ? AND fingerprint = ?", sub.UserID, sub.Fingerprint).Update("state", "failed")
			continue
		}
		pack.Overview = overview
		report, call, err := OmniRiskModelReview(ctx, pack, run.ID, owner, epoch, s, secrets)
		if err != nil {
			if call == nil && riskSafeError(err) != "risk_budget_exhausted" {
				model.DB.Model(&model.OmniRiskSubject{}).Where("user_id = ? AND fingerprint = ? AND state = ?", sub.UserID, sub.Fingerprint, "pending").Update("state", "failed")
			}
			state := "failed"
			if call != nil {
				var saved model.OmniRiskCall
				model.DB.First(&saved, "id = ?", call.ID)
				if saved.State != "settled" && saved.State != "not_sent" {
					state = "unknown"
				}
				model.DB.Model(&model.OmniRiskSubject{}).Where("user_id = ? AND call_id = ?", sub.UserID, call.ID).Update("state", state)
			}
			continue // Isolate a failed subject; never discard the rest of the batch.
		}
		if err := OmniRiskSaveReport(pack, report, call, owner, epoch, s.Notifications); err != nil {
			return err
		}
		run.Reviewed++
		if s.Notifications {
			OmniRiskDeliver(ctx, secrets, owner, epoch)
		}
	}
	model.DB.Model(&model.OmniRiskSettings{}).Where("id = 1").Update("reviewed_at", time.Now().Unix())
	return nil
}

func OmniRiskReminders() {
	var cases []model.OmniRiskCase
	model.DB.Where("status <> ? AND updated_at >= ?", "closed", time.Now().Unix()-90*86400).Order("created_at").Limit(1000).Find(&cases)
	now := time.Now().Unix()
	day := model.OmniRiskDayKey(time.Now())
	for _, c := range cases {
		if now-c.CreatedAt < 86400 {
			continue
		}
		var r OmniRiskReport
		if json.Unmarshal([]byte(c.Report), &r) != nil {
			continue
		}
		_ = model.OmniRiskEnqueue(model.DB, "reminder:"+c.ID+":"+day, c.ID, "reminder", "待处理提醒\n"+OmniRiskNotification(c, r))
	}
}

func OmniRiskDeliver(ctx context.Context, secrets model.OmniRiskSecrets, owner string, epoch int64) {
	omniRiskDeliveryMu.Lock()
	defer omniRiskDeliveryMu.Unlock()
	sendOwner, claimErr := riskDeliveryClaim()
	if claimErr != nil {
		return
	}
	defer model.DB.Model(&model.OmniRiskLease{}).Where("key = ? AND owner = ?", "delivery_v2", sendOwner).Update("expires", 0)
	endpoint, err := url.Parse(secrets.Wecom.Webhook)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host != "qyapi.weixin.qq.com" || endpoint.Path != "/cgi-bin/webhook/send" {
		_ = model.OmniRiskOperational("notification_config_invalid", true)
		return
	}
	var rows []model.OmniRiskOutbox
	if model.DB.Where("state = ? AND next_at <= ?", "pending", time.Now().Unix()).Order("created_at,id").Limit(10).Find(&rows).Error != nil {
		return
	}
	batchIDs := []string{}
	content := ""
	attempts := 0
	for _, row := range rows {
		if len(content)+len(riskWeComBounded(row.Content))+2 > 3500 {
			break
		}
		batchIDs = append(batchIDs, row.ID)
		if content != "" {
			content += "\n\n"
		}
		content += riskWeComBounded(row.Content)
		attempts = max(attempts, row.Attempts)
	}
	if len(batchIDs) == 0 {
		return
	}
	rows = []model.OmniRiskOutbox{{Content: content, Attempts: attempts}}
	for _, row := range rows {
		if ctx.Err() != nil {
			return
		}
		// A lease checked before each send prevents independent dual-node dispatch.
		if !riskDeliveryRenew(sendOwner) {
			return
		}
		if model.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.OmniRiskLease{Key: "wecom_rate"}).Error != nil {
			return
		}
		rate := model.DB.Model(&model.OmniRiskLease{}).Where("key = ? AND expires <= ?", "wecom_rate", time.Now().Unix()).Update("expires", time.Now().Unix()+5)
		if rate.Error != nil || rate.RowsAffected != 1 {
			return
		}
		payload, _ := json.Marshal(map[string]any{"msgtype": "text", "text": map[string]string{"content": row.Content}})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(payload))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		response, err := omniRiskWecomDo(req)
		ok := false
		code := "wecom_transport_failed"
		if err == nil {
			data, readErr := io.ReadAll(io.LimitReader(response.Body, 16384))
			response.Body.Close()
			var answer struct {
				ErrCode *int `json:"errcode"`
			}
			if readErr == nil && response.StatusCode == 200 && json.Unmarshal(data, &answer) == nil && answer.ErrCode != nil && *answer.ErrCode == 0 {
				ok = true
			} else {
				code = "wecom_rejected"
			}
		}
		if ok {
			model.DB.Model(&model.OmniRiskOutbox{}).Where("id IN ? AND state = ?", batchIDs, "pending").Updates(map[string]any{"state": "delivered", "delivered_at": time.Now().Unix(), "attempts": row.Attempts + 1, "error_code": ""})
			_ = model.OmniRiskOperational("notification_delivery_failed", false)
		} else {
			delay := int64(30) << min(row.Attempts, 5)
			if delay > 900 {
				delay = 900
			}
			model.DB.Model(&model.OmniRiskOutbox{}).Where("id IN ? AND state = ?", batchIDs, "pending").Updates(map[string]any{"attempts": row.Attempts + 1, "next_at": time.Now().Unix() + delay, "error_code": code})
			_ = model.OmniRiskOperational("notification_delivery_failed", true)
			return
		}
	}
	_ = model.OmniRiskOperational("notification_config_invalid", false)
}

func OmniRiskPrune(now int64) {
	// Keep minimal unresolved monetary holds, never erase unknown procurement.
	before := now - 90*86400
	for _, item := range []struct {
		Table any
		Where string
		Args  []any
	}{{&model.OmniRiskAuthEvent{}, "created_at < ?", []any{before}}, {&model.OmniRiskAction{}, "created_at < ?", []any{before}}, {&model.OmniRiskOutbox{}, "created_at < ?", []any{before}}, {&model.OmniRiskRun{}, "started_at < ? AND state NOT IN ?", []any{before, []string{"queued", "running"}}}, {&model.OmniRiskCall{}, "created_at < ? AND state IN ?", []any{before, []string{"settled", "not_sent"}}}, {&model.OmniRiskCase{}, "updated_at < ? AND status = 'closed'", []any{before}}, {&model.OmniRiskSubject{}, "changed_at < ? AND state NOT IN ?", []any{before, []string{"inflight", "unknown"}}}} {
		model.DB.Where(item.Where, item.Args...).Delete(item.Table)
	}
	model.DB.Model(&model.OmniRiskSubject{}).Where("changed_at < ? AND state IN ?", before, []string{"inflight", "unknown"}).Update("evidence", "{}")
}
