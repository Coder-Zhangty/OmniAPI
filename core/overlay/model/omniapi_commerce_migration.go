package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The gate is durable and read from the database, not an option cache.
type OmniCommerceRelease struct {
	ID           string `json:"id" gorm:"primaryKey;size:64"`
	State        string `json:"state"`
	ProfileID    string `json:"profile_id"`
	AcceptanceID string `json:"acceptance_id"`
	CreatedAt    int64  `json:"created_at"`
}
type OmniMigrationBatch struct {
	ID          string `json:"id" gorm:"primaryKey;size:64"`
	ProfileID   string `json:"profile_id"`
	ProfileHash string `json:"profile_hash"`
	PreviewHash string `json:"preview_hash"`
	PreviewJSON string `json:"-" gorm:"type:text"`
	State       string `json:"state"`
	Actor       int    `json:"actor"`
	CreatedAt   int64  `json:"created_at"`
	AppliedAt   int64  `json:"applied_at"`
}
type OmniMigrationSegment struct {
	OrderID       string `json:"order_id"`
	PayerID       int    `json:"payer_id"`
	Start         int64  `json:"start"`
	End           int64  `json:"end"`
	PaidQuota     int64  `json:"paid_quota"`
	RefundedQuota int64  `json:"refunded_quota"`
	EvidenceHash  string `json:"evidence_hash"`
}
type OmniMigrationItem struct {
	ID                string `json:"id" gorm:"primaryKey;size:96"`
	BatchID           string `json:"batch_id" gorm:"index"`
	UserID            int    `json:"user_id" gorm:"uniqueIndex"`
	SubscriptionStart int64  `json:"subscription_start"`
	OriginalEnd       int64  `json:"original_end"`
	BeforeJSON        string `json:"-" gorm:"type:text"`
	AfterJSON         string `json:"-" gorm:"type:text"`
	SegmentsJSON      string `json:"-" gorm:"type:text"`
	AppliedAt         int64  `json:"applied_at"`
	ExitID            string `json:"exit_id"`
}
type OmniMigrationExit struct {
	ID             string `json:"id" gorm:"primaryKey;size:96"`
	ItemID         string `json:"item_id" gorm:"index"`
	UserID         int    `json:"user_id" gorm:"index"`
	State          string `json:"state"`
	RequestedAt    int64  `json:"requested_at"`
	RefundQuota    int64  `json:"refund_quota"`
	AllocationJSON string `json:"-" gorm:"type:text"`
	Reason         string `json:"reason"`
	Decision       string `json:"decision"`
	Actor          int    `json:"actor"`
	ResolvedAt     int64  `json:"resolved_at"`
}
type omniMigrationCandidate struct {
	StateSnapshot string                 `json:"state_snapshot"`
	UserID        int                    `json:"user_id"`
	Target        string                 `json:"target"`
	Subscription  OmniSubscription       `json:"subscription"`
	Native        *UserSubscription      `json:"native,omitempty"`
	Segments      []OmniMigrationSegment `json:"segments"`
	Cards         []OmniCard             `json:"cards"`
}

func OmniLegacySaleClosed(tx *gorm.DB) (bool, error) {
	if !OmniCommerceReady() {
		return false, nil
	}
	var count int64
	err := tx.Model(&OmniCommerceRelease{}).Where("state IN ?", []string{"closing", "migrated", "active"}).Count(&count).Error
	return count > 0, err
}
func omniLegacySaleGuard(tx *gorm.DB) error {
	closed, err := OmniLegacySaleClosed(tx)
	if err != nil {
		return err
	}
	if closed {
		return errors.New("旧套餐已停售；已付款历史订单请申请人工核验")
	}
	return nil
}

func OmniMigrationOpenSales(actor int, batchID, acceptance string) error {
	if actor <= 0 {
		return errors.New("发布人无效")
	}
	var optionValue string
	err := DB.Transaction(func(tx *gorm.DB) error {
		var release OmniCommerceRelease
		if err := lockForUpdate(tx).First(&release, "id = ?", "unified-20261104").Error; err != nil {
			return err
		}
		if release.AcceptanceID != acceptance || release.State != "migrated" && release.State != "active" {
			return errors.New("迁移或发布验收尚未完成")
		}
		var batch OmniMigrationBatch
		if err := tx.First(&batch, "id = ? AND state = ? AND profile_id = ?", batchID, "applied", release.ProfileID).Error; err != nil {
			return err
		}
		var profile OmniCommerceProfile
		if err := tx.First(&profile, "id = ?", release.ProfileID).Error; err != nil {
			return err
		}
		if err := OmniCommerceLaunchReady(profile); err != nil {
			return err
		}
		candidates, err := omniMigrationCandidates(tx, time.Now().Unix())
		if err != nil {
			return err
		}
		if len(candidates) > 0 {
			return errors.New("仍有未迁移权益，不能开售")
		}
		cfg := OmniCommerceSwitches{Enabled: true, SalesEnabled: true, ProfileID: profile.ID, AcceptanceID: acceptance}
		raw, _ := json.Marshal(cfg)
		optionValue = string(raw)
		if err = tx.Save(&Option{Key: "OmniAPICommerceV3", Value: optionValue}).Error; err != nil {
			return err
		}
		release.State = "active"
		return tx.Save(&release).Error
	})
	if err == nil {
		common.OptionMapRWMutex.Lock()
		common.OptionMap["OmniAPICommerceV3"] = optionValue
		common.OptionMapRWMutex.Unlock()
	}
	return err
}

// Freezing the old storefront is a separate reviewed step before preview/drain.
func OmniMigrationCloseLegacy(actor int, profileID, acceptance string) error {
	if actor <= 0 || len(acceptance) < 16 {
		return errors.New("缺少发布验收记录")
	}
	var p OmniCommerceProfile
	if err := DB.First(&p, "id = ?", profileID).Error; err != nil {
		return err
	}
	if err := OmniCommerceLaunchReady(p); err != nil {
		return err
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		var old OmniCommerceRelease
		err := lockForUpdate(tx).First(&old, "id = ?", "unified-20261104").Error
		if err == nil {
			if old.ProfileID == profileID && old.AcceptanceID == acceptance {
				return nil
			}
			return errors.New("存在不同发布批次")
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return tx.Create(&OmniCommerceRelease{ID: "unified-20261104", State: "closing", ProfileID: profileID, AcceptanceID: acceptance, CreatedAt: time.Now().Unix()}).Error
	})
}
func omniMigrationSegments(tx *gorm.DB, s OmniSubscription) ([]OmniMigrationSegment, error) {
	if s.PendingMonths != 0 || s.CommerceQueue != "" && s.CommerceQueue != "[]" || s.ActivationPending || s.Start <= 0 || s.MonthsPurchased <= 0 {
		return nil, errors.New("存在待生效权益，须重新核对迁移")
	}
	var orders []OmniOrder
	if err := tx.Where("user_id = ? AND plan_key = ? AND state = ?", s.UserID, s.PlanKey, "fulfilled").Order("created_at DESC, id DESC").Find(&orders).Error; err != nil {
		return nil, err
	}
	selected := []OmniOrder{}
	months := 0
	for _, o := range orders {
		if months >= s.MonthsPurchased {
			break
		}
		if (o.Months != 1 && o.Months != 12) || o.Receipt == "" {
			return nil, errors.New("历史订单缺少证据")
		}
		selected = append(selected, o)
		months += o.Months
	}
	if months != s.MonthsPurchased || OmniCalendarEnd(s.Start, months) != s.End {
		return nil, errors.New("历史购买段与有效期不能对账")
	}
	sort.Slice(selected, func(i, j int) bool {
		if selected[i].CreatedAt == selected[j].CreatedAt {
			return selected[i].ID < selected[j].ID
		}
		return selected[i].CreatedAt < selected[j].CreatedAt
	})
	for i := 1; i < len(selected); i++ {
		if selected[i].CreatedAt == selected[i-1].CreatedAt && (selected[i].Months != selected[i-1].Months || selected[i].Cents != selected[i-1].Cents) {
			return nil, errors.New("同秒购买段顺序不明确，须人工核对")
		}
	}
	segments := []OmniMigrationSegment{}
	months = 0
	for _, o := range selected {
		var l OmniLedger
		if err := tx.First(&l, "key = ? AND user_id = ?", o.Receipt, s.UserID).Error; err != nil {
			return nil, err
		}
		if l.Kind != "subscription:"+s.PlanKey || l.Cents != o.Cents || l.Quota <= 0 || l.Refunded {
			return nil, errors.New("历史实付账本不完整")
		}
		start := OmniCalendarEnd(s.Start, months)
		months += o.Months
		raw, _ := json.Marshal(struct {
			Order  OmniOrder
			Ledger OmniLedger
		}{o, l})
		segments = append(segments, OmniMigrationSegment{OrderID: o.ID, PayerID: o.UserID, Start: start, End: OmniCalendarEnd(s.Start, months), PaidQuota: l.Quota, EvidenceHash: omniHash(string(raw))})
	}
	return segments, nil
}
func omniNativeMigrationSegment(tx *gorm.DB, s UserSubscription) (OmniMigrationSegment, error) {
	var plan SubscriptionPlan
	if err := tx.First(&plan, s.PlanId).Error; err != nil {
		return OmniMigrationSegment{}, err
	}
	if plan.QuotaResetPeriod != "never" || s.UpgradeGroup != "" || s.Source != "balance" {
		return OmniMigrationSegment{}, errors.New("原生权益须人工核对")
	}
	if plan.Id != 1 || plan.Title != "OmniAPI 体验包" || plan.Currency != "USD" || plan.PriceAmount != 2.815273 || plan.DurationUnit != "day" || plan.DurationValue != 30 {
		return OmniMigrationSegment{}, errors.New("原生商品与已核对的体验包不同，停止自动迁移")
	}
	var orders []SubscriptionOrder
	if err := tx.Where("user_id = ? AND plan_id = ? AND status = ? AND payment_method = ? AND complete_time BETWEEN ? AND ?", s.UserId, s.PlanId, "success", "balance", s.CreatedAt-10, s.CreatedAt+10).Find(&orders).Error; err != nil {
		return OmniMigrationSegment{}, err
	}
	if len(orders) != 1 {
		return OmniMigrationSegment{}, errors.New("原生订单无法唯一关联")
	}
	o := orders[0]
	var payload struct {
		ChargedQuota int64 `json:"charged_quota"`
	}
	if json.Unmarshal([]byte(o.ProviderPayload), &payload) != nil || payload.ChargedQuota <= 0 {
		return OmniMigrationSegment{}, errors.New("缺少原币扣款证据")
	}
	raw, _ := json.Marshal(o)
	return OmniMigrationSegment{OrderID: "native:" + o.TradeNo, PayerID: s.UserId, Start: s.StartTime, End: s.EndTime, PaidQuota: payload.ChargedQuota, EvidenceHash: omniHash(string(raw))}, nil
}
func omniMigrationCandidates(tx *gorm.DB, now int64) ([]omniMigrationCandidate, error) {
	out := []omniMigrationCandidate{}
	seen := map[int]bool{}
	targets := map[string]string{"starter": "v3_19", "standard": "v3_39", "pro": "v3_99"}
	var rows []OmniSubscription
	if err := tx.Where(clause.Gt{Column: "end", Value: now}).Where("entitlement_version < ?", 3).Order("user_id").Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, s := range rows {
		if s.Start > now || targets[s.PlanKey] == "" {
			return nil, errors.New("未知老权益或未生效权益")
		}
		parts, err := omniMigrationSegments(tx, s)
		if err != nil {
			return nil, err
		}
		var cards []OmniCard
		if err = tx.Where("user_id = ? AND subscription_start = ?", s.UserID, s.Start).Order("id").Find(&cards).Error; err != nil {
			return nil, err
		}
		out = append(out, omniMigrationCandidate{UserID: s.UserID, Target: targets[s.PlanKey], Subscription: s, Segments: parts, Cards: cards})
		seen[s.UserID] = true
	}
	var native []UserSubscription
	if err := tx.Where("status IN ? AND end_time > ?", []string{"active", "paused"}, now).Order("user_id,id").Find(&native).Error; err != nil {
		return nil, err
	}
	for _, s := range native {
		if seen[s.UserId] || s.Status != "active" {
			return nil, errors.New("存在重叠或暂停的原生权益，停止迁移")
		}
		part, err := omniNativeMigrationSegment(tx, s)
		if err != nil {
			return nil, err
		}
		var existing OmniSubscription
		if err = tx.First(&existing, "user_id = ?", s.UserId).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		out = append(out, omniMigrationCandidate{UserID: s.UserId, Target: "v3_19", Subscription: existing, Native: &s, Segments: []OmniMigrationSegment{part}})
		seen[s.UserId] = true
	}
	for i := range out {
		fields := map[string]any{}
		value := reflect.ValueOf(out[i].Subscription)
		typ := value.Type()
		for j := 0; j < value.NumField(); j++ {
			fields[typ.Field(j).Name] = value.Field(j).Interface()
		}
		raw, _ := json.Marshal(fields)
		out[i].StateSnapshot = string(raw)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UserID < out[j].UserID })
	return out, nil
}
func OmniMigrationPrepare(actor int, id, profileID string) (OmniMigrationBatch, error) {
	out := OmniMigrationBatch{}
	if actor <= 0 || len(id) < 16 || len(id) > 64 {
		return out, errors.New("迁移批次无效")
	}
	err := DB.Transaction(func(tx *gorm.DB) error {
		var p OmniCommerceProfile
		if err := tx.First(&p, "id = ?", profileID).Error; err != nil {
			return err
		}
		if err := OmniCommerceLaunchReady(p); err != nil {
			return err
		}
		rows, err := omniMigrationCandidates(tx, time.Now().Unix())
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(rows)
		out = OmniMigrationBatch{ID: id, ProfileID: p.ID, ProfileHash: p.ContentHash, PreviewHash: omniHash(string(raw)), PreviewJSON: string(raw), State: "preview", Actor: actor, CreatedAt: time.Now().Unix()}
		return tx.Create(&out).Error
	})
	return out, err
}
func OmniMigrationApply(actor int, id, previewHash string) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var release OmniCommerceRelease
		if err := lockForUpdate(tx).First(&release, "id = ?", "unified-20261104").Error; err != nil {
			return err
		}
		var batch OmniMigrationBatch
		if err := lockForUpdate(tx).First(&batch, "id = ?", id).Error; err != nil {
			return err
		}
		if batch.PreviewHash != previewHash {
			return errors.New("迁移预览不一致")
		}
		if batch.State == "applied" {
			return nil
		}
		if actor <= 0 || release.State != "closing" || release.ProfileID != batch.ProfileID || OmniCommerceSettings().SalesEnabled {
			return errors.New("须先停旧售、关闭新售并排空请求")
		}
		var p OmniCommerceProfile
		if err := tx.First(&p, "id = ?", batch.ProfileID).Error; err != nil {
			return err
		}
		if p.ContentHash != batch.ProfileHash {
			return errors.New("权益版本已变")
		}
		if err := OmniCommerceLaunchReady(p); err != nil {
			return err
		}
		// Serialize all account mutations in stable order, then re-read every snapshot.
		var expected []omniMigrationCandidate
		if json.Unmarshal([]byte(batch.PreviewJSON), &expected) != nil {
			return errors.New("预览数据损坏")
		}
		for _, row := range expected {
			if _, err := omniAccount(tx, row.UserID); err != nil {
				return err
			}
			busy, err := omniBusy(tx, row.UserID)
			if err != nil {
				return err
			}
			if busy {
				return errors.New("存在未结请求，停止迁移")
			}
		}
		now := time.Now().Unix()
		rows, err := omniMigrationCandidates(tx, now)
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(rows)
		if omniHash(string(raw)) != previewHash {
			return errors.New("权益、账本或用户清单已变，请重新预览")
		}
		plans, err := p.Plans()
		if err != nil {
			return err
		}
		planMap := map[string]OmniCommercePlan{}
		for _, v := range plans {
			planMap[v.Key] = v
		}
		profileRaw, _ := json.Marshal(p)
		for _, row := range rows {
			s := row.Subscription
			before, _ := json.Marshal(row)
			plan := planMap[row.Target]
			if row.Native != nil {
				s = OmniSubscription{UserID: row.UserID, Start: row.Native.StartTime, End: row.Native.EndTime, MonthsPurchased: 1, LastPurchaseMonths: 1}
				if err = tx.Model(&UserSubscription{}).Where("id = ? AND status = ?", row.Native.Id, "active").Update("status", "migrated").Error; err != nil {
					return err
				}
			}
			if err = s.advance(now); err != nil {
				return err
			}
			s.PlanKey = plan.Key
			s.EntitlementVersion = 3
			s.QuotaVersion = OmniQuotaVersion
			s.BillingVersion = 2
			s.FiveWindowEnabled = plan.FiveEnabled
			s.CommerceProfile = string(profileRaw)
			s.FxMicros = p.FXMicros
			s.ResetPriceCents = plan.ResetCents
			s.NativeLegacyAwait = false
			s.FiveLimit, err = omniCommerceQuota(plan.FiveCents, p.FXMicros)
			if err != nil {
				return err
			}
			s.WeekLimit, err = omniCommerceQuota(plan.WeekCents, p.FXMicros)
			if err != nil {
				return err
			}
			s.FiveUsed = 0
			s.WeekUsed = 0
			s.FivePaused = false
			s.WeekPaused = false
			s.MigrationID = id
			after, _ := json.Marshal(s)
			segments, _ := json.Marshal(row.Segments)
			item := OmniMigrationItem{ID: fmt.Sprintf("%s:%d", id, row.UserID), BatchID: id, UserID: row.UserID, SubscriptionStart: s.Start, OriginalEnd: s.End, BeforeJSON: string(before), AfterJSON: string(after), SegmentsJSON: string(segments), AppliedAt: now}
			if err = tx.Create(&item).Error; err != nil {
				return err
			}
			if err = tx.Save(&s).Error; err != nil {
				return err
			}
			// Rebind existing cards, retaining their issuance period and cost. Counts
			// include used cards, so migration cannot reissue the same free benefit.
			if err = tx.Model(&OmniCard{}).Where("user_id = ? AND subscription_start = ?", row.UserID, s.Start).Update("plan_key", s.PlanKey).Error; err != nil {
				return err
			}
			if err = tx.Create(&OmniLedger{Key: "migration:" + item.ID, UserID: row.UserID, Kind: "subscription:migrated_refill", AuditJSON: string(before), CreatedAt: now}).Error; err != nil {
				return err
			}
			if err = omniNotice(tx, "migration:"+item.ID, row.UserID, "订阅已统一升级", fmt.Sprintf("已迁移到%s，适用额度免费补满一次。原到期时间%s、自然恢复时间不变。模型及能力范围见个人套餐；迁移后30天内可申请人工退出核算。", plan.Title, time.Unix(s.End, 0).UTC().Format(time.RFC3339))); err != nil {
				return err
			}
		}
		if err = tx.Model(&OmniOrder{}).Where("state IN ?", []string{"awaiting_payment", "pending"}).Update("state", "retired_unpaid").Error; err != nil {
			return err
		}
		batch.State = "applied"
		batch.AppliedAt = now
		batch.Actor = actor
		if err = tx.Save(&batch).Error; err != nil {
			return err
		}
		release.State = "migrated"
		return tx.Save(&release).Error
	})
}

type omniExitAllocation struct {
	OrderID string `json:"order_id"`
	Quota   int64  `json:"quota"`
}

func OmniMigrationExitRequest(uid int, requestKey, reason string, expected ...int64) (OmniMigrationExit, error) {
	out := OmniMigrationExit{}
	if len(requestKey) < 16 || len(requestKey) > 64 || len(reason) > 2000 {
		return out, errors.New("退出申请无效")
	}
	id := fmt.Sprintf("exit:%d:%s", uid, requestKey)
	defer invalidateUserCache(uid)
	err := DB.Transaction(func(tx *gorm.DB) error {
		if _, err := omniAccount(tx, uid); err != nil {
			return err
		}
		if err := OmniRiskBusinessMutation(tx, uid, expected...); err != nil {
			return err
		}
		err := tx.First(&out, "id = ? AND user_id = ?", id, uid).Error
		if err == nil {
			if out.Reason != reason {
				return errors.New("申请幂等键冲突")
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var item OmniMigrationItem
		if err = lockForUpdate(tx).First(&item, "user_id = ?", uid).Error; err != nil {
			return err
		}
		now := time.Now().Unix()
		if now >= item.AppliedAt+30*86400 || now >= item.OriginalEnd || item.ExitID != "" {
			return errors.New("不在受理期或已有申请")
		}
		var s OmniSubscription
		if err = lockForUpdate(tx).First(&s, "user_id = ?", uid).Error; err != nil {
			return err
		}
		if s.MigrationID != item.BatchID || s.Start != item.SubscriptionStart {
			return errors.New("当前权益已变化")
		}
		var segments []OmniMigrationSegment
		if json.Unmarshal([]byte(item.SegmentsJSON), &segments) != nil {
			return errors.New("退款段损坏")
		}
		parts := []omniExitAllocation{}
		total := int64(0)
		for _, part := range segments {
			if part.PayerID != uid || part.End <= part.Start || part.PaidQuota <= 0 {
				return errors.New("原付款证据无效")
			}
			remaining := max(int64(0), part.End-max(now, part.Start))
			q, err := omniIntRatio(part.PaidQuota, remaining, part.End-part.Start, false)
			if err != nil {
				return err
			}
			q = max(int64(0), q-part.RefundedQuota)
			if q > math.MaxInt64-total {
				return errors.New("退款金额溢出")
			}
			total += q
			parts = append(parts, omniExitAllocation{part.OrderID, q})
		}
		if total <= 0 {
			return errors.New("没有可退的未履行部分")
		}
		raw, _ := json.Marshal(parts)
		out = OmniMigrationExit{ID: id, ItemID: item.ID, UserID: uid, State: "pending", RequestedAt: now, RefundQuota: total, AllocationJSON: string(raw), Reason: reason}
		if err = tx.Create(&out).Error; err != nil {
			return err
		}
		s.ExitApplicationID = id
		item.ExitID = id
		if err = tx.Save(&s).Error; err != nil {
			return err
		}
		return tx.Save(&item).Error
	})
	return out, err
}
func OmniMigrationExitResolve(actor, uid int, id, action, note string) error {
	if len(note) > 2000 || action != "approve" && action != "reject" && action != "withdraw" {
		return errors.New("退出处理无效")
	}
	defer invalidateUserCache(uid)
	return DB.Transaction(func(tx *gorm.DB) error {
		if _, err := omniAccount(tx, uid); err != nil {
			return err
		}
		var out OmniMigrationExit
		if err := lockForUpdate(tx).First(&out, "id = ? AND user_id = ?", id, uid).Error; err != nil {
			return err
		}
		if action == "withdraw" && actor != uid || action != "withdraw" && actor <= 0 {
			return errors.New("处理人无效")
		}
		if out.State != "pending" {
			if out.State == action {
				return nil
			}
			return errors.New("申请已处理")
		}
		var item OmniMigrationItem
		if err := lockForUpdate(tx).First(&item, "id = ?", out.ItemID).Error; err != nil {
			return err
		}
		var s OmniSubscription
		if err := lockForUpdate(tx).First(&s, "user_id = ?", uid).Error; err != nil {
			return err
		}
		if s.ExitApplicationID != id || item.ExitID != id || s.Start != item.SubscriptionStart || s.MigrationID != item.BatchID {
			return errors.New("申请绑定权益已变化")
		}
		now := time.Now().Unix()
		if action == "approve" {
			busy, err := omniBusy(tx, uid)
			if err != nil {
				return err
			}
			if busy {
				return errors.New("等待未结请求结算")
			}
			var segments []OmniMigrationSegment
			var parts []omniExitAllocation
			if json.Unmarshal([]byte(item.SegmentsJSON), &segments) != nil || json.Unmarshal([]byte(out.AllocationJSON), &parts) != nil || len(parts) != len(segments) {
				return errors.New("退款明细无效")
			}
			total := int64(0)
			for i, a := range parts {
				if a.OrderID != segments[i].OrderID || a.Quota < 0 || a.Quota > segments[i].PaidQuota-segments[i].RefundedQuota {
					return errors.New("退款超过原付款")
				}
				segments[i].RefundedQuota += a.Quota
				if a.Quota > math.MaxInt64-total {
					return errors.New("退款金额溢出")
				}
				total += a.Quota
			}
			if total != out.RefundQuota {
				return errors.New("退款金额不符")
			}
			// Detach only the migrated term. New purchases remain queued and activate
			// at their original scheduled dates; approval never grants early access.
			if err = s.advance(now); err != nil {
				return err
			}
			s.End = min(s.End, now)
			s.PendingMonths = 0
			s.PendingPlan = ""
			s.WeekUsed = s.WeekLimit
			s.FiveUsed = s.FiveLimit
			if err = tx.Model(&User{}).Where("id = ?", uid).UpdateColumn("quota", gorm.Expr("quota + ?", total)).Error; err != nil {
				return err
			}
			raw, _ := json.Marshal(segments)
			item.SegmentsJSON = string(raw)
			if err = tx.Create(&OmniLedger{Key: "migration-refund:" + id, UserID: uid, Kind: "subscription:migration_refund", Quota: total, AuditJSON: out.AllocationJSON, CreatedAt: now}).Error; err != nil {
				return err
			}
		} else {
			item.ExitID = ""
			if err := s.advance(now); err != nil {
				return err
			}
		}
		s.ExitApplicationID = ""
		if err := tx.Save(&s).Error; err != nil {
			return err
		}
		if err := tx.Save(&item).Error; err != nil {
			return err
		}
		out.State = action
		out.Decision = note
		out.Actor = actor
		out.ResolvedAt = now
		return tx.Save(&out).Error
	})
}
