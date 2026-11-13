package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net"
	"sort"
	"strings"
	"time"
)

type OmniRiskEvidence struct {
	ID      string           `json:"id"`
	Family  string           `json:"family"`
	Kind    string           `json:"kind"`
	Users   []int            `json:"users"`
	Values  map[string]int64 `json:"values"`
	Sources []string         `json:"sources"`
	Tag     string           `json:"tag,omitempty"`
}
type OmniRiskPack struct {
	SubjectID      int                `json:"subject_id"`
	InviterID      int                `json:"inviter_id"`
	Related        []int              `json:"related_user_ids"`
	CreatedAt      int64              `json:"created_at"`
	LastActivity   int64              `json:"last_activity"`
	PriorHandledAt int64              `json:"prior_handled_at"`
	Evidence       []OmniRiskEvidence `json:"evidence"`
	Missing        []string           `json:"missing"`
	Overview       map[string]int64   `json:"overview,omitempty"`
}
type riskUser struct {
	ID           int
	InviterID    int
	CreatedAt    int64
	LastLoginAt  int64
	Status       int
	Quota        int64
	RequestCount int64
	AffHistory   int64
	AffQuota     int64
}
type riskUsage struct {
	UserID   int
	Requests int64
	Errors   int64
	Quota    int64
	LastAt   int64
	FirstAt  int64
	LastID   int64
}
type riskAuditGroup struct {
	UserID    int
	Ip        string
	UserAgent string
	Events    int64
	LastAt    int64
	LastID    int64
}
type riskLedgerGroup struct {
	UserID  int
	Kind    string
	Amount  int64
	Cents   int64
	Events  int64
	LastAt  int64
	FirstAt int64
}
type riskNativeReward struct {
	UserID    int
	CreatedAt int64
	Events    int64
}

func riskRead(db *gorm.DB, ctx context.Context, fn func(*gorm.DB) error) error {
	child, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
	defer cancel()
	return db.WithContext(child).Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "postgres" {
			for _, q := range []string{"SET TRANSACTION READ ONLY", "SET LOCAL statement_timeout='2500ms'", "SET LOCAL lock_timeout='150ms'", "SET LOCAL work_mem='4MB'", "SET LOCAL max_parallel_workers_per_gather=0"} {
				if err := tx.Exec(q).Error; err != nil {
					return err
				}
			}
		}
		return fn(tx)
	})
}

// All columns are explicit. Free-text content/other, usernames, email, bearer
// credentials and customer prompts never enter a pack or its fingerprint.
func OmniRiskCollect(ctx context.Context, s model.OmniRiskSettings, secrets model.OmniRiskSecrets, now int64) ([]OmniRiskPack, map[string]int64, error) {
	start := now - 30*86400
	users := []riskUser{}
	for after := 0; ; {
		var page []riskUser
		err := riskRead(model.DB, ctx, func(tx *gorm.DB) error {
			return tx.Table("users").Select("id, inviter_id, created_at, last_login_at, status, quota, request_count, aff_history, aff_quota").Where("role = ? AND id <> ? AND id > ?", 1, s.ServiceUserID, after).Order("id").Limit(500).Find(&page).Error
		})
		if err != nil {
			return nil, nil, errors.New("risk_users_read_failed")
		}
		users = append(users, page...)
		if len(page) < 500 {
			break
		}
		after = page[len(page)-1].ID
		if len(users) > 100000 {
			return nil, nil, errors.New("risk_collection_capacity")
		}
	}
	overview := map[string]int64{"customer_accounts": int64(len(users)), "new_accounts_30d": 0, "window_start": start, "as_of": now, "failed_auth_last_hour": 0}
	index := map[int]int{}
	packs := make([]OmniRiskPack, len(users))
	networks := map[string][]int{}
	browsers := map[string]map[int]bool{}
	for i, u := range users {
		index[u.ID] = i
		if u.CreatedAt >= start {
			overview["new_accounts_30d"]++
		}
		packs[i] = OmniRiskPack{SubjectID: u.ID, InviterID: u.InviterID, CreatedAt: u.CreatedAt, LastActivity: u.LastLoginAt, Related: []int{}, Missing: []string{"wallet_balance_is_not_reward_attribution", "network_or_browser_does_not_prove_identity"}, Evidence: []OmniRiskEvidence{{ID: fmt.Sprintf("user:%d", u.ID), Family: "account", Kind: "account_metadata", Users: []int{u.ID}, Values: map[string]int64{"created_at": u.CreatedAt, "status": int64(u.Status), "wallet_quota_unattributed": u.Quota, "request_count_lifetime": u.RequestCount}, Sources: []string{fmt.Sprintf("users:%d", u.ID)}}}}
		if u.CreatedAt < s.AuditSince {
			packs[i].Missing = append(packs[i].Missing, "historical_registration_network_unavailable")
		}
	}
	var usage []riskUsage
	if err := riskRead(model.LOG_DB, ctx, func(tx *gorm.DB) error {
		return tx.Table("logs").Select("user_id, SUM(CASE WHEN type=2 THEN 1 ELSE 0 END) AS requests, SUM(CASE WHEN type=5 THEN 1 ELSE 0 END) AS errors, SUM(CASE WHEN type=2 THEN quota ELSE 0 END) AS quota, MAX(created_at) AS last_at, MIN(CASE WHEN type=2 THEN created_at ELSE NULL END) AS first_at, MAX(id) AS last_id").Where("created_at >= ? AND created_at <= ? AND type IN ? AND user_id <> ?", start, now, []int{2, 5}, s.ServiceUserID).Group("user_id").Find(&usage).Error
	}); err != nil {
		return nil, nil, errors.New("risk_usage_read_failed")
	}
	for _, r := range usage {
		if i, ok := index[r.UserID]; ok {
			p := &packs[i]
			p.LastActivity = max(p.LastActivity, r.LastAt)
			p.Evidence = append(p.Evidence, OmniRiskEvidence{ID: fmt.Sprintf("usage:%d:%d", r.UserID, r.LastID), Family: "usage", Kind: "usage_30d", Users: []int{r.UserID}, Values: map[string]int64{"successful_requests": r.Requests, "failed_requests_not_abuse_evidence": r.Errors, "billed_quota": r.Quota, "first_success_at": r.FirstAt, "last_activity_at": r.LastAt}, Sources: []string{fmt.Sprintf("logs:user:%d:last:%d", r.UserID, r.LastID)}})
		}
	}
	var audits []riskAuditGroup
	if err := riskRead(model.LOG_DB, ctx, func(tx *gorm.DB) error {
		return tx.Table("audit_logs").Select("user_id, ip, user_agent, COUNT(*) AS events, MAX(created_at) AS last_at, MAX(id) AS last_id").Where("created_at >= ? AND created_at <= ? AND success = ? AND user_id > 0 AND user_id <> ?", start, now, true, s.ServiceUserID).Group("user_id, ip, user_agent").Limit(50001).Find(&audits).Error
	}); err != nil {
		return nil, nil, errors.New("risk_audit_read_failed")
	}
	if len(audits) > 50000 {
		return nil, nil, errors.New("risk_audit_capacity_incomplete")
	}
	addNetwork := func(uid int, tag, browser string) {
		if tag == "" {
			return
		}
		if _, ok := index[uid]; !ok {
			return
		}
		found := false
		for _, id := range networks[tag] {
			if id == uid {
				found = true
			}
		}
		if !found {
			networks[tag] = append(networks[tag], uid)
		}
		if browser != "" {
			key := tag + ":" + browser
			if browsers[key] == nil {
				browsers[key] = map[int]bool{}
			}
			browsers[key][uid] = true
		}
	}
	browserAccounts := map[string]map[int]bool{}
	for _, a := range audits {
		if i, ok := index[a.UserID]; ok {
			if b := model.OmniRiskTag(secrets.HMACKey, "browser", a.UserAgent); b != "" {
				if browserAccounts[b] == nil {
					browserAccounts[b] = map[int]bool{}
				}
				browserAccounts[b][a.UserID] = true
			}
			tag := ""
			if ip := net.ParseIP(a.Ip); ip != nil && ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() {
				tag = model.OmniRiskTag(secrets.HMACKey, "network", a.Ip)
			}
			addNetwork(a.UserID, tag, model.OmniRiskTag(secrets.HMACKey, "browser", a.UserAgent))
			packs[i].LastActivity = max(packs[i].LastActivity, a.LastAt)
		}
	}
	var auth []model.OmniRiskAuthEvent
	if err := riskRead(model.DB, ctx, func(tx *gorm.DB) error {
		return tx.Where("created_at >= ? AND created_at <= ?", start, now).Order("id").Limit(50001).Find(&auth).Error
	}); err != nil {
		return nil, nil, errors.New("risk_auth_read_failed")
	}
	if len(auth) > 50000 {
		return nil, nil, errors.New("risk_auth_capacity_incomplete")
	}
	for _, a := range auth {
		if !a.Success && a.CreatedAt >= now-3600 {
			overview["failed_auth_last_hour"]++
		}
		if i, ok := index[a.UserID]; ok {
			packs[i].LastActivity = max(packs[i].LastActivity, a.CreatedAt)
			if a.Success {
				addNetwork(a.UserID, a.NetworkTag, a.BrowserTag)
			}
			if a.Action == "registration.success" {
				packs[i].Evidence = append(packs[i].Evidence, OmniRiskEvidence{ID: fmt.Sprintf("auth:%d", a.ID), Family: "account", Kind: "verified_registration_event", Users: []int{a.UserID}, Values: map[string]int64{"registered_at": a.CreatedAt}, Sources: []string{fmt.Sprintf("risk_auth_events:%d", a.ID)}, Tag: a.NetworkTag})
				if a.GrantedQuota > 0 {
					packs[i].Evidence = append(packs[i].Evidence, OmniRiskEvidence{ID: fmt.Sprintf("auth-gift:%d", a.ID), Family: "benefit", Kind: "known_initial_registration_credit", Users: []int{a.UserID}, Values: map[string]int64{"known_gift_quota": a.GrantedQuota, "events": 1, "last_at": a.CreatedAt}, Sources: []string{fmt.Sprintf("risk_auth_events:%d", a.ID)}})
				}
			}
		}
	}
	// Network and browser correlation form ONE family. Never double-count them.
	networkKeys := make([]string, 0, len(networks))
	for k := range networks {
		networkKeys = append(networkKeys, k)
	}
	sort.Strings(networkKeys)
	for _, tag := range networkKeys {
		ids := networks[tag]
		sort.Ints(ids)
		if len(ids) < 2 {
			continue
		}
		for _, id := range ids {
			p := &packs[index[id]]
			sameBrowser := 0
			for key, members := range browsers {
				if strings.HasPrefix(key, tag+":") && members[id] && len(members) > sameBrowser {
					sameBrowser = len(members)
				}
			}
			if len(p.Evidence) < 20 {
				p.Evidence = append(p.Evidence, OmniRiskEvidence{ID: "network:" + tag, Family: "association", Kind: "shared_network_not_identity", Users: riskLimitIDs(ids, 40), Values: map[string]int64{"distinct_accounts": int64(len(ids)), "same_network_browser_accounts_not_device_identity": int64(sameBrowser)}, Sources: []string{"audit_logs:network_hmac:" + tag}, Tag: tag})
			}
			p.Related = riskUnion(p.Related, ids, 40)
		}
		// A sliding window detects short registration bursts inside a 30-day group.
		ordered := append([]int{}, ids...)
		sort.Slice(ordered, func(i, j int) bool { return packs[index[ordered[i]]].CreatedAt < packs[index[ordered[j]]].CreatedAt })
		for left, right := 0, 0; right < len(ordered); right++ {
			for packs[index[ordered[right]]].CreatedAt-packs[index[ordered[left]]].CreatedAt > 1800 {
				left++
			}
			if right-left+1 < 3 || packs[index[ordered[right]]].CreatedAt < start {
				continue
			}
			group := ordered[left : right+1]
			for _, id := range group {
				p := &packs[index[id]]
				if len(p.Evidence) < 24 {
					p.Evidence = append(p.Evidence, OmniRiskEvidence{ID: fmt.Sprintf("burst:%s:%d", tag, packs[index[ordered[left]]].CreatedAt), Family: "timing", Kind: "registration_burst", Users: riskLimitIDs(group, 40), Values: map[string]int64{"accounts_30min": int64(len(group)), "first_at": packs[index[ordered[left]]].CreatedAt, "last_at": packs[index[ordered[right]]].CreatedAt}, Sources: []string{"users:created_at"}})
				}
			}
		}
	}
	invitations := map[int][]int{}
	for _, u := range users {
		if u.InviterID > 0 {
			invitations[u.InviterID] = append(invitations[u.InviterID], u.ID)
		}
	}
	for inviter, ids := range invitations {
		sort.Ints(ids)
		for _, id := range append([]int{inviter}, ids...) {
			if i, ok := index[id]; ok {
				p := &packs[i]
				p.Related = riskUnion(p.Related, append([]int{inviter}, ids...), 40)
				p.Evidence = append(p.Evidence, OmniRiskEvidence{ID: fmt.Sprintf("invites:%d", inviter), Family: "relationship", Kind: "invitation_graph_not_abuse", Users: riskLimitIDs(append([]int{inviter}, ids...), 40), Values: map[string]int64{"inviter_id": int64(inviter), "invitees": int64(len(ids))}, Sources: []string{"users:inviter_id"}})
			}
		}
	}
	handled, handledErr := riskV2HandledTimes(ctx)
	if handledErr != nil {
		return nil, nil, handledErr
	}
	for uid, at := range handled {
		if i, ok := index[uid]; ok {
			packs[i].PriorHandledAt = max(packs[i].PriorHandledAt, at)
		}
	}
	var ledgers []riskLedgerGroup
	if err := riskRead(model.DB, ctx, func(tx *gorm.DB) error {
		return tx.Model(&model.OmniLedger{}).Select("user_id, kind, SUM(quota) AS amount, SUM(cents) AS cents, COUNT(*) AS events, MAX(created_at) AS last_at, MIN(created_at) AS first_at").Where("created_at >= ? AND created_at <= ? AND user_id <> ?", start, now, s.ServiceUserID).Group("user_id, kind").Find(&ledgers).Error
	}); err != nil {
		return nil, nil, errors.New("risk_ledger_read_failed")
	}
	for _, r := range ledgers {
		if i, ok := index[r.UserID]; ok {
			p := &packs[i]
			p.LastActivity = max(p.LastActivity, r.LastAt)
			if r.Kind == "risk_enforcement" {
				p.PriorHandledAt = max(p.PriorHandledAt, r.LastAt)
				continue
			}
			allowed := map[string]bool{"referral": true, "referral_migration": true, "topup": true, "refund": true, "verified_credit": true, "wallet_credit": true, "wallet_refund": true, "checkin": true}
			if !allowed[r.Kind] {
				continue
			}
			family := "finance"
			if r.Kind == "referral" || r.Kind == "referral_migration" {
				family = "benefit"
			}
			p.Evidence = append(p.Evidence, OmniRiskEvidence{ID: fmt.Sprintf("ledger:%d:%s:%d", r.UserID, r.Kind, r.LastAt), Family: family, Kind: r.Kind, Users: []int{r.UserID}, Values: map[string]int64{"quota": r.Amount, "cents": r.Cents, "events": r.Events, "first_at": r.FirstAt, "last_at": r.LastAt}, Sources: []string{fmt.Sprintf("omni_ledgers:user:%d:kind:%s", r.UserID, r.Kind)}})
		}
	}
	var accounts []model.OmniAccount
	if err := riskRead(model.DB, ctx, func(tx *gorm.DB) error { return tx.Select("user_id, net_recharge_cents").Find(&accounts).Error }); err != nil {
		return nil, nil, errors.New("risk_finance_read_failed")
	}
	for _, a := range accounts {
		if i, ok := index[a.UserID]; ok {
			packs[i].Evidence = append(packs[i].Evidence, OmniRiskEvidence{ID: fmt.Sprintf("recharge:%d", a.UserID), Family: "finance", Kind: "net_recharge_not_identity", Users: []int{a.UserID}, Values: map[string]int64{"net_recharge_cents": a.NetRechargeCents}, Sources: []string{fmt.Sprintf("omni_accounts:%d", a.UserID)}})
		}
	}
	// Registration gifts are known system entries, not arbitrary free text sent
	// to the model. Only numeric aggregates leave the database boundary.
	var gifts []struct {
		UserID int
		Amount int64
		Events int64
		LastAt int64
	}
	if err := riskRead(model.LOG_DB, ctx, func(tx *gorm.DB) error {
		return tx.Table("logs").Select("user_id,SUM(quota) AS amount,COUNT(*) AS events,MAX(created_at) AS last_at").Where("created_at >= ? AND created_at <= ? AND type = 4 AND (content LIKE ? OR content LIKE ?)", start, now, "%注册赠送%", "%注册赠额%").Group("user_id").Find(&gifts).Error
	}); err != nil {
		return nil, nil, errors.New("risk_gifts_read_failed")
	}
	giftUsers := map[int]bool{}
	giftTimes := map[int]int64{}
	verifiedGift := map[int]bool{}
	for _, a := range auth {
		if a.Action == "registration.success" && a.GrantedQuota > 0 {
			verifiedGift[a.UserID] = true
		}
	}
	for _, g := range gifts {
		if i, ok := index[g.UserID]; ok {
			giftUsers[g.UserID] = true
			giftTimes[g.UserID] = g.LastAt
			packs[i].LastActivity = max(packs[i].LastActivity, g.LastAt)
			values := map[string]int64{"events": g.Events, "last_at": g.LastAt}
			if g.Amount > 0 {
				values["known_gift_quota"] = g.Amount
			} else if !verifiedGift[g.UserID] {
				packs[i].Missing = append(packs[i].Missing, "historical_gift_amount_unavailable_from_numeric_ledger")
			}
			packs[i].Evidence = append(packs[i].Evidence, OmniRiskEvidence{ID: fmt.Sprintf("gift:%d:%d", g.UserID, g.LastAt), Family: "benefit", Kind: "registration_gift_not_abuse", Users: []int{g.UserID}, Values: values, Sources: []string{"logs:system_registration_gifts"}})
		}
	}
	for _, a := range auth {
		if a.Action == "registration.success" && a.GrantedQuota > 0 {
			giftUsers[a.UserID] = true
			giftTimes[a.UserID] = a.CreatedAt
		}
	}
	firstUse := map[int]int64{}
	for _, u := range usage {
		if u.FirstAt > 0 {
			firstUse[u.UserID] = u.FirstAt
		}
	}
	// The legacy registration path records each native invitation reward in
	// system logs, not OmniLedger. Only exact server-generated event categories
	// and numeric counters enter evidence; never parse/export free-text amounts.
	var native []riskNativeReward
	if err := riskRead(model.LOG_DB, ctx, func(tx *gorm.DB) error {
		return tx.Table("logs").Select("user_id,created_at,COUNT(*) AS events").Where("created_at >= ? AND created_at <= ? AND type = 4 AND content LIKE ?", start, now, "邀请用户赠送 %").Group("user_id,created_at").Limit(50001).Find(&native).Error
	}); err != nil {
		return nil, nil, errors.New("risk_native_rewards_read_failed")
	}
	if len(native) > 50000 {
		return nil, nil, errors.New("risk_native_rewards_capacity_incomplete")
	}
	for inviter, children := range invitations {
		root, exists := index[inviter]
		if !exists {
			continue
		}
		ordered := append([]int{}, children...)
		sort.Slice(ordered, func(i, j int) bool { return packs[index[ordered[i]]].CreatedAt < packs[index[ordered[j]]].CreatedAt })
		bestLeft, bestRight := -1, -1
		for left, right := 0, 0; right < len(ordered); right++ {
			for packs[index[ordered[right]]].CreatedAt-packs[index[ordered[left]]].CreatedAt > 1800 {
				left++
			}
			if right-left+1 < 3 || packs[index[ordered[right]]].CreatedAt < start {
				continue
			}
			if right-left >= bestRight-bestLeft {
				bestLeft, bestRight = left, right
			}
		}
		if bestLeft < 0 {
			continue
		}
		{
			group := ordered[bestLeft : bestRight+1]
			first, last := packs[index[group[0]]].CreatedAt, packs[index[group[len(group)-1]]].CreatedAt
			events := int64(0)
			for _, r := range native {
				if r.UserID == inviter && r.CreatedAt >= first && r.CreatedAt <= last+2 {
					events += r.Events
				}
			}
			known, rapid := int64(0), int64(0)
			patternAt := last
			for _, id := range group {
				if giftUsers[id] {
					known++
					at := firstUse[id]
					if at >= giftTimes[id] && at > 0 && at-giftTimes[id] <= 900 {
						rapid++
						patternAt = max(patternAt, at)
					}
				}
			}
			shared := int64(0)
			for _, members := range networks {
				n := int64(0)
				for _, id := range group {
					for _, member := range members {
						if id == member {
							n++
							break
						}
					}
				}
				shared = max(shared, n)
			}
			members := riskUnion([]int{inviter}, group, 40)
			suffix := fmt.Sprintf("%d:%d:%d", inviter, first, last)
			for _, id := range members {
				p := &packs[index[id]]
				p.Related = riskUnion(p.Related, members, 40)
				p.Evidence = append(p.Evidence, OmniRiskEvidence{ID: "invite-burst:" + suffix, Family: "timing", Kind: "invitation_registration_burst", Users: members, Values: map[string]int64{"inviter_id": int64(inviter), "accounts_30min": int64(len(group)), "first_at": first, "last_at": last}, Sources: []string{"users:inviter_id", "users:created_at"}})
				if events > 0 {
					p.Evidence = append(p.Evidence, OmniRiskEvidence{ID: "native-referral:" + suffix, Family: "benefit", Kind: "native_referral", Users: members, Values: map[string]int64{"inviter_id": int64(inviter), "events": events, "lifetime_reward_quota_not_window_amount": users[root].AffHistory, "untransferred_reward_quota": users[root].AffQuota, "first_at": first, "last_at": last}, Sources: []string{"logs:system_native_invitation_rewards", "users:aff_history", "users:aff_quota"}})
				}
				p.Evidence = append(p.Evidence, OmniRiskEvidence{ID: "invite-pattern:" + suffix, Family: "benefit", Kind: "invitation_benefit_pattern", Users: members, Values: map[string]int64{"inviter_id": int64(inviter), "known_registration_gifts": known, "native_reward_events": events, "rapid_users_15m": rapid, "shared_network_accounts_not_identity": shared, "first_at": first, "last_at": patternAt}, Sources: []string{"risk_auth_events:registration.success", "logs:successful_usage", "logs:system_native_invitation_rewards"}})
			}
		}
	}
	if err := riskAddChainEvidence(users, index, packs, browserAccounts, giftUsers, native, start); err != nil {
		return nil, nil, err
	}
	for _, tag := range networkKeys {
		ids := networks[tag]
		claimed := []int{}
		rapid := int64(0)
		last := int64(0)
		for _, id := range ids {
			if giftUsers[id] {
				claimed = append(claimed, id)
				last = max(last, giftTimes[id])
				if at := firstUse[id]; at >= giftTimes[id] && at-giftTimes[id] <= 900 {
					rapid++
				}
			}
		}
		if len(claimed) < 3 {
			continue
		}
		for _, id := range ids {
			p := &packs[index[id]]
			p.Evidence = append(p.Evidence, OmniRiskEvidence{ID: "related-gifts:" + tag, Family: "benefit", Kind: "related_registration_benefit_claims", Users: riskLimitIDs(claimed, 40), Values: map[string]int64{"events": int64(len(claimed)), "rapid_users_15m": rapid, "last_at": last}, Sources: []string{"logs:system_registration_gifts", "risk_auth_events:registration.success"}, Tag: tag})
		}
	}
	for i := range packs {
		unique := map[string]OmniRiskEvidence{}
		for _, e := range packs[i].Evidence {
			old, exists := unique[e.ID]
			if !exists || e.Values["last_at"] > old.Values["last_at"] || e.Values["accounts_30min"] > old.Values["accounts_30min"] {
				unique[e.ID] = e
			}
		}
		packs[i].Evidence = nil
		for _, e := range unique {
			packs[i].Evidence = append(packs[i].Evidence, e)
		}
		sort.Slice(packs[i].Evidence, func(a, b int) bool { return packs[i].Evidence[a].ID < packs[i].Evidence[b].ID })
		packs[i].Related = riskUnion(nil, packs[i].Related, 40)
	}
	return packs, overview, nil
}

// Search the actual multi-hop invitation graph. Rotating networks must not
// erase a short registration chain. Browser tags are association evidence,
// never device identity, and never sufficient for an alert by themselves.
func riskAddChainEvidence(users []riskUser, index map[int]int, packs []OmniRiskPack, browsers map[string]map[int]bool, gifts map[int]bool, native []riskNativeReward, start int64) error {
	best := map[int][]int{}
	members := map[int]map[int]bool{}
	for _, u := range users {
		if u.CreatedAt < start {
			continue
		}
		path := []int{}
		seen := map[int]bool{}
		id := u.ID
		root := id
		for depth := 0; id > 0; depth++ {
			if depth >= 100 {
				return errors.New("risk_invitation_graph_capacity_incomplete")
			}
			i, ok := index[id]
			if !ok {
				break
			}
			if seen[id] {
				path = nil
				break
			}
			seen[id] = true
			root = id
			if u.CreatedAt-users[i].CreatedAt <= 1800 && users[i].CreatedAt <= u.CreatedAt {
				path = append(path, id)
			}
			id = users[i].InviterID
		}
		if len(path) < 3 {
			continue
		}
		for l, r := 0, len(path)-1; l < r; l, r = l+1, r-1 {
			path[l], path[r] = path[r], path[l]
		}
		if members[root] == nil {
			members[root] = map[int]bool{root: true}
		}
		for _, id := range path {
			members[root][id] = true
		}
		prior := best[root]
		if len(path) > len(prior) || (len(path) == len(prior) && u.CreatedAt > packs[index[prior[len(prior)-1]]].CreatedAt) {
			best[root] = path
		}
	}
	byUser := map[int]map[int64]int64{}
	for _, r := range native {
		if byUser[r.UserID] == nil {
			byUser[r.UserID] = map[int64]int64{}
		}
		byUser[r.UserID][r.CreatedAt] += r.Events
	}
	userBrowsers := map[int][]string{}
	for tag, ids := range browsers {
		for id := range ids {
			userBrowsers[id] = append(userBrowsers[id], tag)
		}
	}
	for root, path := range best {
		first, last := packs[index[path[0]]].CreatedAt, packs[index[path[len(path)-1]]].CreatedAt
		known, events, gaps, shared := int64(0), int64(0), int64(0), int64(0)
		counts := map[string]int64{}
		for n, id := range path {
			if gifts[id] {
				known++
			}
			for _, tag := range userBrowsers[id] {
				counts[tag]++
				shared = max(shared, counts[tag])
			}
			if n == len(path)-1 {
				continue
			}
			at := packs[index[path[n+1]]].CreatedAt
			edgeEvents := int64(0)
			for delta := int64(-2); delta <= 2; delta++ {
				edgeEvents += byUser[id][at+delta]
			}
			events += edgeEvents
			u := users[index[id]]
			if edgeEvents > 0 && u.AffHistory > 0 && u.AffQuota >= 0 && (u.AffHistory-u.AffQuota)*10 >= u.AffHistory*9 {
				gaps++
			}
		}
		all := []int{}
		for id := range members[root] {
			all = append(all, id)
		}
		sort.Ints(all)
		visible := riskLimitIDs(all, 40)
		for _, id := range all {
			p := &packs[index[id]]
			p.Related = riskUnion(p.Related, all, 40)
			suffix := fmt.Sprintf("%d:%d:%d", root, first, last)
			p.Evidence = append(p.Evidence,
				OmniRiskEvidence{ID: "chain-burst:" + suffix, Family: "timing", Kind: "chain_registration_burst", Users: visible, Values: map[string]int64{"inviter_id": int64(root), "accounts_30min": int64(len(path)), "chain_edges": int64(len(path) - 1), "first_at": first, "last_at": last}, Sources: []string{"users:inviter_id", "users:created_at"}},
				OmniRiskEvidence{ID: "chain-rewards:" + suffix, Family: "benefit", Kind: "chain_native_rewards", Users: visible, Values: map[string]int64{"inviter_id": int64(root), "events": events, "first_at": first, "last_at": last}, Sources: []string{"logs:system_native_invitation_rewards"}},
				OmniRiskEvidence{ID: "chain-pattern:" + suffix, Family: "association", Kind: "chain_benefit_pattern", Users: visible, Values: map[string]int64{"inviter_id": int64(root), "known_registration_gifts": known, "same_browser_accounts_not_identity": shared, "reward_counter_gaps_not_proven_transfers": gaps, "first_at": first, "last_at": last}, Sources: []string{"audit_logs:browser_hmac", "risk_auth_events:registration.success", "users:aff_history", "users:aff_quota"}})
		}
	}
	return nil
}
func riskLimitIDs(ids []int, n int) []int {
	v := append([]int{}, ids...)
	sort.Ints(v)
	if len(v) > n {
		v = v[:n]
	}
	return v
}
func riskUnion(a, b []int, n int) []int {
	seen := map[int]bool{}
	for _, id := range append(append([]int{}, a...), b...) {
		seen[id] = true
	}
	out := []int{}
	for id := range seen {
		out = append(out, id)
	}
	return riskLimitIDs(out, n)
}

func OmniRiskStorePacks(packs []OmniRiskPack, owner string, epoch int64, now int64) (int, error) {
	changed := 0
	for _, p := range packs {
		data, err := json.Marshal(p)
		if err != nil {
			return changed, err
		}
		fingerprint := model.OmniRiskDigest(data)
		priority := 0
		for _, e := range p.Evidence {
			if e.Kind == "registration_burst" || e.Kind == "invitation_registration_burst" || e.Kind == "chain_registration_burst" {
				priority += 30
			}
			if e.Family == "benefit" {
				priority += 10
			}
		}
		err = model.DB.Transaction(func(tx *gorm.DB) error {
			if err := model.OmniRiskFence(tx, owner, epoch); err != nil {
				return err
			}
			var old model.OmniRiskSubject
			e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&old, "user_id = ?", p.SubjectID).Error
			if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
				return e
			}
			if e == nil && old.Fingerprint == fingerprint {
				return nil
			}
			if old.State == "inflight" || old.State == "unknown" {
				state := "pending"
				if p.PriorHandledAt > 0 && (p.LastActivity <= p.PriorHandledAt || p.Evidence[0].Values["status"] != 1) {
					state = "historically_handled"
				}
				return tx.Model(&old).Updates(map[string]any{"fingerprint": fingerprint, "evidence": string(data), "changed_at": now, "state": state, "call_id": ""}).Error
			} // preserve unresolved evidence and call ownership
			state := "pending"
			reviewed := old.ReviewedFingerprint
			if p.PriorHandledAt > 0 && (p.LastActivity <= p.PriorHandledAt || p.Evidence[0].Values["status"] != 1) {
				state = "historically_handled"
				reviewed = fingerprint
			}
			row := model.OmniRiskSubject{UserID: p.SubjectID, Fingerprint: fingerprint, ReviewedFingerprint: reviewed, RiskFingerprint: old.RiskFingerprint, Evidence: string(data), State: state, Priority: priority, ChangedAt: now, ReviewedAt: old.ReviewedAt}
			if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error; err != nil {
				return err
			}
			changed++
			return nil
		})
		if err != nil {
			return changed, err
		}
	}
	return changed, nil
}
