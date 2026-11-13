package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrOmniSharedUnavailable = errors.New("共享池或成员预算不足")
var ErrOmniSharedConcurrent = errors.New("共享池已达到16个进行中请求，请稍后重试")

type OmniSharedPool struct {
	ID            string `json:"id" gorm:"primaryKey;size:64"`
	OwnerID       int    `json:"owner_id"`
	Name          string `json:"name" gorm:"size:80"`
	CurrentPeriod string `json:"current_period" gorm:"size:64"`
	TransferTo    int    `json:"transfer_to"`
	TransferAt    int64  `json:"transfer_at"`
	Frozen        bool   `json:"frozen"`
	CreatedAt     int64  `json:"created_at"`
}
type OmniSharedMember struct {
	PoolID       string `json:"pool_id" gorm:"primaryKey;size:64"`
	UserID       int    `json:"user_id" gorm:"primaryKey;autoIncrement:false"`
	State        string `json:"state" gorm:"index;size:16"`
	BudgetMicros int64  `json:"budget_micros"` // -1 unlimited; not an earmarked allocation
	InvitedBy    int    `json:"invited_by"`
	JoinedAt     int64  `json:"joined_at"`
}
type OmniSharedPeriod struct {
	ID          string `json:"id" gorm:"primaryKey;size:64"`
	PoolID      string `json:"pool_id" gorm:"index;size:64"`
	BuyerID     int    `json:"buyer_id"`
	Sequence    int64  `json:"sequence"`
	State       string `json:"state" gorm:"index;size:16"`
	PriceCents  int64  `json:"price_cents"`
	WalletQuota int64  `json:"-"`
	ProfileJSON string `json:"-" gorm:"type:text"`
	Start       int64  `json:"start"`
	End         int64  `json:"end"`
	CreatedAt   int64  `json:"created_at"`
}
type OmniSharedBatch struct {
	ID             string `json:"id" gorm:"primaryKey;size:64"`
	PoolID         string `json:"pool_id" gorm:"index;size:64"`
	PeriodID       string `json:"period_id" gorm:"index;size:64"`
	BuyerID        int    `json:"buyer_id"`
	PriceCents     int64  `json:"price_cents"`
	Granted        int64  `json:"-"`
	Used           int64  `json:"-"`
	Reserved       int64  `json:"-"`
	CostUsedMicros int64  `json:"cost_used_micros"`
	CreatedAt      int64  `json:"created_at"`
	Sequence       int64  `json:"sequence"`
}
type OmniSharedAllocation struct {
	BatchID        string `json:"batch_id"`
	Quota          int64  `json:"quota"`
	CostHoldMicros int64  `json:"cost_hold_micros"`
}
type OmniSharedHold struct {
	BillingPolicy    string `json:"billing_policy" gorm:"size:40;not null;default:''"`
	PayerID          int    `json:"-"`
	EstimatedQuota   int64  `json:"-"`
	AllowanceActual  int64  `json:"-"`
	WalletActual     int64  `json:"-"`
	WalletCostMicros int64  `json:"wallet_cost_micros"`
	Owner            string `json:"-" gorm:"size:64"`
	DispatchState    string `json:"-" gorm:"size:16"`
	RequestID        string `json:"request_id" gorm:"primaryKey;size:128"`
	PoolID           string `json:"pool_id" gorm:"index;size:64"`
	PeriodID         string `json:"period_id" gorm:"index;size:64"`
	UserID           int    `json:"user_id" gorm:"index"`
	TokenID          int    `json:"-"`
	Model            string `json:"-" gorm:"size:256"`
	ProfileJSON      string `json:"-" gorm:"type:text"`
	Allocations      string `json:"-" gorm:"type:text"`
	Reserved         int64  `json:"-"`
	Actual           int64  `json:"-"`
	CostHoldMicros   int64  `json:"-"`
	CostUsedMicros   int64  `json:"cost_used_micros"`
	PlatformQuota    int64  `json:"-"`
	State            string `json:"state" gorm:"index;size:16"`
	CreatedAt        int64  `json:"created_at"`
	SettledAt        int64  `json:"settled_at"`
}
type OmniSharedEvent struct {
	Key       string `json:"key" gorm:"primaryKey;size:160"`
	PoolID    string `json:"pool_id" gorm:"index;size:64"`
	UserID    int    `json:"user_id"`
	Action    string `json:"action" gorm:"size:32"`
	Payload   string `json:"-" gorm:"type:text"`
	Result    string `json:"-" gorm:"type:text"`
	CreatedAt int64  `json:"created_at"`
}
type OmniSharedCommand struct {
	Action      string `json:"action"`
	RequestKey  string `json:"request_key"`
	Name        string `json:"name,omitempty"`
	UserID      int    `json:"user_id,omitempty"`
	BudgetCents int64  `json:"budget_cents"`
	PeriodID    string `json:"period_id,omitempty"`
	Cents       int64  `json:"cents,omitempty"`
	ProfileID   string `json:"profile_id,omitempty"`
}

func OmniCommerceMigrate(tx *gorm.DB) error {
	if err := tx.AutoMigrate(&OmniCommerceRelease{}, &OmniMigrationBatch{}, &OmniMigrationItem{}, &OmniMigrationExit{}, &SubscriptionOrder{}); err != nil {
		return err
	}
	err := tx.AutoMigrate(&OmniCommerceProfile{}, &OmniSharedPool{}, &OmniSharedMember{}, &OmniSharedPeriod{}, &OmniSharedBatch{}, &OmniSharedHold{}, &OmniSharedEvent{}, &OmniCommerceTerm{}, &OmniSharedRecovery{}, &OmniSharedProcess{})
	if err == nil {
		omniCommerceReadyMu.Lock()
		omniCommerceDB = tx
		omniCommerceReadyMu.Unlock()
	}
	return err
}

func omniSharedMember(tx *gorm.DB, pool string, uid int) (OmniSharedMember, error) {
	var m OmniSharedMember
	err := tx.First(&m, "pool_id = ? AND user_id = ?", pool, uid).Error
	if err != nil || m.State != "active" {
		return m, errors.New("不是当前共享池成员")
	}
	return m, nil
}

// Caller holds the pool row. Outstanding reservations are never classified as
// depletion; expiry may start the next period while old requests remain pinned.
func omniSharedAdvance(tx *gorm.DB, p *OmniSharedPool, now int64) error {
	for {
		anchor := now
		if p.CurrentPeriod != "" {
			var current OmniSharedPeriod
			if err := tx.First(&current, "id = ?", p.CurrentPeriod).Error; err != nil {
				return err
			}
			var totals struct{ Granted, Used, Reserved int64 }
			if err := tx.Model(&OmniSharedBatch{}).Where("period_id = ?", current.ID).Select("COALESCE(SUM(granted),0) AS granted, COALESCE(SUM(used),0) AS used, COALESCE(SUM(reserved),0) AS reserved").Scan(&totals).Error; err != nil {
				return err
			}
			if current.End > now && !(totals.Granted > 0 && totals.Used == totals.Granted && totals.Reserved == 0) {
				return nil
			}
			if current.End <= now {
				anchor = current.End
			}
			if current.State == "active" {
				current.State = "ended"
				if err := tx.Save(&current).Error; err != nil {
					return err
				}
			}
		}
		var next OmniSharedPeriod
		err := tx.Where("pool_id = ? AND state = ?", p.ID, "queued").Order("sequence ASC").First(&next).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		next.State = "active"
		next.Start = max(anchor, next.CreatedAt)
		next.End = OmniCalendarEnd(next.Start, 1)
		if err = tx.Save(&next).Error; err != nil {
			return err
		}
		p.CurrentPeriod = next.ID
		if err = tx.Save(p).Error; err != nil {
			return err
		}
		if next.End > now {
			return nil
		}
	}
}

func omniSharedNewBatch(tx *gorm.DB, period OmniSharedPeriod, uid int, cents int64, profile OmniCommerceProfile) error {
	grant, err := profile.GrantCents(cents)
	if err != nil {
		return err
	}
	q, err := omniCommerceQuota(grant, profile.FXMicros)
	if err != nil {
		return err
	}
	if q <= 0 {
		return errors.New("授予额度不足")
	}
	var sequence int64
	if err = tx.Model(&OmniSharedBatch{}).Where("pool_id = ?", period.PoolID).Select("COALESCE(MAX(sequence),0)").Scan(&sequence).Error; err != nil {
		return err
	}
	return tx.Create(&OmniSharedBatch{ID: omniCommerceID("sb_"), PoolID: period.PoolID, PeriodID: period.ID, BuyerID: uid, PriceCents: cents, Granted: q, Sequence: sequence + 1, CreatedAt: time.Now().Unix()}).Error
}

func OmniSharedMutate(uid int, poolID string, b OmniSharedCommand, expected ...int64) (map[string]any, error) {
	if len(b.RequestKey) < 16 || len(b.RequestKey) > 80 {
		return nil, errors.New("invalid request key")
	}
	key := fmt.Sprintf("shared:%d:%s", uid, b.RequestKey)
	payload, _ := json.Marshal(struct {
		Pool    string
		Command OmniSharedCommand
	}{poolID, b})
	out := map[string]any{}
	err := DB.Transaction(func(tx *gorm.DB) error {
		// Serialize the business key before checking idempotency on PostgreSQL.
		if _, err := omniAccount(tx, uid); err != nil {
			return err
		}
		if err := OmniRiskBusinessMutation(tx, uid, expected...); err != nil {
			return err
		}
		var event OmniSharedEvent
		if err := tx.First(&event, "key = ?", key).Error; err == nil {
			if event.Payload != string(payload) {
				return errors.New("request key conflict")
			}
			return json.Unmarshal([]byte(event.Result), &out)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var p OmniSharedPool
		var err error
		now := time.Now().Unix()
		if b.Action == "create" {
			if len([]rune(b.Name)) < 1 || len([]rune(b.Name)) > 80 {
				return errors.New("共享池名称需为1至80字")
			}
			p = OmniSharedPool{ID: omniCommerceID("sp_"), OwnerID: uid, Name: b.Name, CreatedAt: now}
			if err = tx.Create(&p).Error; err != nil {
				return err
			}
			if err = tx.Create(&OmniSharedMember{PoolID: p.ID, UserID: uid, State: "active", BudgetMicros: -1, JoinedAt: now, InvitedBy: uid}).Error; err != nil {
				return err
			}
		} else {
			p, err = omniCommercePool(tx, poolID)
			if err != nil {
				return errors.New("共享池不存在")
			}
			if b.Action != "accept" && b.Action != "cancel" {
				if _, err = omniSharedMember(tx, p.ID, uid); err != nil {
					return err
				}
			}
			if err = omniSharedAdvance(tx, &p, now); err != nil {
				return err
			}
		}
		switch b.Action {
		case "create", "purchase", "add":
			if b.Action != "add" && p.OwnerID != uid {
				return errors.New("仅池主可购买套餐")
			}
			profile, e := OmniCommerceSaleProfile(tx)
			if e != nil {
				return e
			}
			if p.Frozen {
				return errors.New("共享池需要管理员核对")
			}
			if b.ProfileID != profile.ID {
				return errors.New("核价版本已变化，请重新确认")
			}
			if b.Action == "add" {
				if b.Cents < 1000 || b.Cents > 1000000 {
					return errors.New("加量金额需为10至10000元")
				}
			} else if b.Cents != 9900 && b.Cents != 19900 && b.Cents != 39900 {
				return errors.New("未知共享套餐")
			}
			var period OmniSharedPeriod
			if b.Action == "add" {
				if b.PeriodID == "" || b.PeriodID != p.CurrentPeriod {
					return errors.New("共享期已变化，请重新确认加量")
				}
				if e = tx.First(&period, "id = ?", p.CurrentPeriod).Error; e != nil {
					return e
				}
				if period.State != "active" || period.End <= now {
					return errors.New("当前期已结束")
				}
				var original OmniCommerceProfile
				_ = json.Unmarshal([]byte(period.ProfileJSON), &original)
				if original.ID != profile.ID {
					return errors.New("当期核价版本与加量商品不同")
				}
			} else {
				var seq int64
				if e = tx.Model(&OmniSharedPeriod{}).Where("pool_id = ?", p.ID).Select("COALESCE(MAX(sequence),0)").Scan(&seq).Error; e != nil {
					return e
				}
				raw, _ := json.Marshal(profile)
				period = OmniSharedPeriod{ID: omniCommerceID("se_"), PoolID: p.ID, BuyerID: uid, Sequence: seq + 1, State: "queued", PriceCents: b.Cents, ProfileJSON: string(raw), CreatedAt: now}
			}
			debit, e := omniCommerceWallet(tx, uid, b.Cents, profile.FXMicros)
			if e != nil {
				return e
			}
			if b.Action != "add" {
				period.WalletQuota = debit
				if e = tx.Create(&period).Error; e != nil {
					return e
				}
			}
			if e = omniSharedNewBatch(tx, period, uid, b.Cents, profile); e != nil {
				return e
			}
			if e = tx.Create(&OmniLedger{Key: key, UserID: uid, Kind: "shared:" + b.Action, Cents: b.Cents, Quota: debit, FxMicros: profile.FXMicros, CreatedAt: now}).Error; e != nil {
				return e
			}
			if e = omniSharedAdvance(tx, &p, now); e != nil {
				return e
			}
			out["period_id"] = period.ID
		case "invite":
			if p.OwnerID != uid || b.UserID == uid || b.BudgetCents < -1 || b.BudgetCents > 100000000 {
				return errors.New("邀请参数或权限无效")
			}
			var target User
			if e := tx.Select("id", "status").First(&target, b.UserID).Error; e != nil || target.Status != common.UserStatusEnabled {
				return errors.New("用户不可邀请")
			}
			var prior OmniSharedMember
			e := tx.First(&prior, "pool_id = ? AND user_id = ?", p.ID, b.UserID).Error
			if e == nil && prior.State != "removed" && prior.State != "left" {
				return errors.New("成员已存在或邀请待接受")
			}
			if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
				return e
			}
			budget := b.BudgetCents
			if budget >= 0 {
				budget *= 10000
			}
			m := OmniSharedMember{PoolID: p.ID, UserID: b.UserID, State: "invited", BudgetMicros: budget, InvitedBy: uid}
			if e = tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "pool_id"}, {Name: "user_id"}}, DoUpdates: clause.AssignmentColumns([]string{"state", "budget_micros", "invited_by", "joined_at"})}).Create(&m).Error; e != nil {
				return e
			}
		case "accept":
			r := tx.Model(&OmniSharedMember{}).Where("pool_id = ? AND user_id = ? AND state = ?", p.ID, uid, "invited").Updates(map[string]any{"state": "active", "joined_at": now})
			if r.Error != nil {
				return r.Error
			}
			if r.RowsAffected != 1 {
				return errors.New("没有待接受邀请")
			}
		case "remove", "leave":
			target := b.UserID
			if b.Action == "leave" {
				target = uid
			} else if p.OwnerID != uid {
				return errors.New("仅池主可移除成员")
			}
			if target == p.OwnerID {
				return errors.New("池主需先转交身份")
			}
			var revoked []Token
			if e := tx.Where("user_id = ? AND omni_pool_id = ?", target, p.ID).Find(&revoked).Error; e != nil {
				return e
			}
			for _, key := range revoked {
				if e := invalidateTokenCacheForMutation(key.Key); e != nil {
					return e
				}
			}
			if e := tx.Model(&Token{}).Where("user_id = ? AND omni_pool_id = ?", target, p.ID).Updates(map[string]any{"status": common.TokenStatusDisabled, "omni_shared_revoked": true}).Error; e != nil {
				return e
			}
			r := tx.Model(&OmniSharedMember{}).Where("pool_id = ? AND user_id = ?", p.ID, target).Update("state", "removed")
			if r.Error != nil {
				return r.Error
			}
			if r.RowsAffected != 1 {
				return errors.New("成员不存在")
			}
		case "budget":
			if p.OwnerID != uid || b.BudgetCents < -1 || b.BudgetCents > 100000000 {
				return errors.New("预算参数或权限无效")
			}
			if _, e := omniSharedMember(tx, p.ID, b.UserID); e != nil {
				return e
			}
			budget := b.BudgetCents
			if budget >= 0 {
				budget *= 10000
			}
			if e := tx.Model(&OmniSharedMember{}).Where("pool_id = ? AND user_id = ?", p.ID, b.UserID).Update("budget_micros", budget).Error; e != nil {
				return e
			}
		case "transfer_start":
			if p.OwnerID != uid || b.UserID == uid {
				return errors.New("转交权限无效")
			}
			if _, e := omniSharedMember(tx, p.ID, b.UserID); e != nil {
				return e
			}
			p.TransferTo = b.UserID
			p.TransferAt = now
			if e := tx.Save(&p).Error; e != nil {
				return e
			}
		case "transfer_accept":
			if p.TransferTo != uid || p.TransferAt+86400 <= now {
				return errors.New("没有有效的池主转交邀请")
			}
			p.OwnerID = uid
			p.TransferTo = 0
			p.TransferAt = 0
			if e := tx.Save(&p).Error; e != nil {
				return e
			}
		case "cancel":
			var period OmniSharedPeriod
			if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&period, "id = ? AND pool_id = ?", b.PeriodID, p.ID).Error; e != nil {
				return e
			}
			if period.BuyerID != uid || period.State != "queued" || period.Start != 0 {
				return errors.New("仅原付款人可取消未启用的预购")
			}
			if e := tx.Model(&User{}).Where("id = ?", uid).UpdateColumn("quota", gorm.Expr("quota + ?", period.WalletQuota)).Error; e != nil {
				return e
			}
			period.State = "cancelled"
			if e := tx.Save(&period).Error; e != nil {
				return e
			}
			if e := tx.Create(&OmniLedger{Key: key, UserID: uid, Kind: "shared:cancel", Cents: period.PriceCents, Quota: period.WalletQuota, CreatedAt: now}).Error; e != nil {
				return e
			}
		default:
			return errors.New("未知共享操作")
		}
		out["pool_id"] = p.ID
		raw, _ := json.Marshal(out)
		return tx.Create(&OmniSharedEvent{Key: key, PoolID: p.ID, UserID: uid, Action: b.Action, Payload: string(payload), Result: string(raw), CreatedAt: now}).Error
	})
	if err == nil {
		invalidateUserCache(uid)
	}
	return out, err
}

func OmniSharedReserve(uid, tokenID int, poolID, requestID, modelName string, amount int64, snapshots ...map[string]string) (*OmniSharedHold, error) {
	if err := OmniSharedHeartbeat(); err != nil {
		return nil, err
	}
	if uid <= 0 || tokenID <= 0 || len(requestID) < 1 || len(requestID) > 128 || amount <= 0 {
		return nil, errors.New("invalid shared reservation")
	}
	var out OmniSharedHold
	err := omniRequestDB(snapshots...).Transaction(func(tx *gorm.DB) error {
		if !OmniCustomerBillingEnabled() {
			if _, err := omniAccount(tx, uid); err != nil {
				return err
			}
		}
		p, err := omniCommercePool(tx, poolID)
		if err != nil {
			return err
		}
		if p.Frozen {
			return errors.New("共享池需要管理员核对")
		}
		m, err := omniSharedMember(tx, p.ID, uid)
		if err != nil {
			return err
		}
		if err = OmniRiskAdmission(tx, uid); err != nil {
			return err
		}
		var u User
		if err = tx.Select("id", "status").First(&u, uid).Error; err != nil || u.Status != common.UserStatusEnabled {
			return ErrOmniRiskHold
		}
		var token Token
		if err = tx.Select("id", "user_id", "status", "omni_pool_id", "omni_shared_revoked").First(&token, tokenID).Error; err != nil || token.UserId != uid || token.Status != common.TokenStatusEnabled || token.OmniSharedRevoked || token.OmniPoolID != poolID {
			return errors.New("共享Key归属无效")
		}
		var prior OmniReservation
		if e := tx.First(&prior, "request_id = ?", requestID).Error; e == nil && prior.Source != "omni_shared" {
			return errors.New("request already bound to personal funding")
		} else if e != nil && !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		err = tx.First(&out, "request_id = ?", requestID).Error
		if err == nil {
			expected := out.Reserved
			if out.BillingPolicy == OmniCustomerBillingV1 {
				expected = out.EstimatedQuota
			}
			if out.UserID != uid || out.TokenID != tokenID || out.PoolID != poolID || out.Model != modelName || out.State != "pending" || expected != amount {
				return errors.New("shared request key conflict")
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err = omniSharedAdvance(tx, &p, time.Now().Unix()); err != nil {
			return err
		}
		var period OmniSharedPeriod
		if err = tx.First(&period, "id = ?", p.CurrentPeriod).Error; err != nil {
			return ErrOmniSharedUnavailable
		}
		if period.State != "active" || period.End <= time.Now().Unix() {
			return ErrOmniSharedUnavailable
		}
		var profile OmniCommerceProfile
		if json.Unmarshal([]byte(period.ProfileJSON), &profile) != nil || !profile.Allows(modelName, 0) {
			return errors.New("该模型不在共享套餐核价范围")
		}
		var count int64
		if err = tx.Model(&OmniSharedHold{}).Where("pool_id = ? AND state = ?", p.ID, "pending").Count(&count).Error; err != nil {
			return err
		}
		if count >= 16 {
			return ErrOmniSharedConcurrent
		}
		if OmniCustomerBillingFor(p.OwnerID) {
			return omniCustomerSharedReserveTx(tx, p, period, m, uid, tokenID, requestID, modelName, amount, &out)
		}
		var batches []OmniSharedBatch
		if err = tx.Where("period_id = ?", period.ID).Order("sequence ASC").Find(&batches).Error; err != nil {
			return err
		}
		need := amount
		cost := int64(0)
		alloc := []OmniSharedAllocation{}
		for _, batch := range batches {
			q := min(need, batch.Granted-batch.Used-batch.Reserved)
			if q <= 0 {
				continue
			}
			c, e := omniIntRatio(q, batch.PriceCents*10000, batch.Granted, true)
			if e != nil {
				return e
			}
			cost += c
			alloc = append(alloc, OmniSharedAllocation{BatchID: batch.ID, Quota: q, CostHoldMicros: c})
			need -= q
			if need == 0 {
				break
			}
		}
		if need != 0 {
			return ErrOmniSharedUnavailable
		}
		var spent struct{ Used, Held int64 }
		if err = tx.Model(&OmniSharedHold{}).Where("period_id = ? AND user_id = ?", period.ID, uid).Select("COALESCE(SUM(cost_used_micros),0) AS used, COALESCE(SUM(CASE WHEN state = 'pending' THEN cost_hold_micros ELSE 0 END),0) AS held").Scan(&spent).Error; err != nil {
			return err
		}
		if m.BudgetMicros >= 0 && spent.Used+spent.Held+cost > m.BudgetMicros {
			return ErrOmniSharedUnavailable
		}
		for _, a := range alloc {
			if err = tx.Model(&OmniSharedBatch{}).Where("id = ?", a.BatchID).UpdateColumn("reserved", gorm.Expr("reserved + ?", a.Quota)).Error; err != nil {
				return err
			}
		}
		raw, _ := json.Marshal(alloc)
		requestProfile := omniFreezeCapabilityProfileFrom(period.ProfileJSON, omniRequestOptionsFor(tx))
		out = OmniSharedHold{Owner: OmniRiskProcessID, DispatchState: "reserved", RequestID: requestID, PoolID: p.ID, PeriodID: period.ID, UserID: uid, TokenID: tokenID, Model: modelName, ProfileJSON: requestProfile, Allocations: string(raw), Reserved: amount, CostHoldMicros: cost, State: "pending", CreatedAt: time.Now().Unix()}
		if err = tx.Create(&out).Error; err != nil {
			return err
		}
		return tx.Create(&OmniReservation{RequestID: requestID, UserID: uid, Source: "omni_shared", SharedPoolID: p.ID, SharedTokenID: tokenID, CommerceProfile: requestProfile, Reserved: amount, State: "pending", CreatedAt: time.Now().Unix()}).Error
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func OmniSharedSettle(requestID string, actual int64) error {
	_, err := OmniSharedSettleOnce(requestID, actual)
	return err
}
func OmniSharedSettleOnce(requestID string, actual int64) (bool, error) {
	return omniSharedSettleAudit(requestID, actual, nil)
}
func omniSharedSettleAudit(requestID string, actual int64, audit *OmniSharedRecovery) (bool, error) {
	changed := false
	if actual < 0 {
		return false, errors.New("negative shared usage")
	}
	var lookup OmniSharedHold
	if err := DB.First(&lookup, "request_id = ?", requestID).Error; err != nil {
		return false, err
	}
	if lookup.BillingPolicy == OmniCustomerBillingV1 {
		return omniCustomerSharedSettle(lookup, actual, audit)
	}
	err := DB.Transaction(func(tx *gorm.DB) error {
		p, err := omniCommercePool(tx, lookup.PoolID)
		if err != nil {
			return err
		}
		var h OmniSharedHold
		if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&h, "request_id = ?", requestID).Error; err != nil {
			return err
		}
		if audit != nil {
			if audit.automatic {
				if h.State != "pending" || h.DispatchState != "reserved" {
					return nil
				}
				var process OmniSharedProcess
				if e := tx.Where("id = ?", h.Owner).Find(&process).Error; e != nil {
					return e
				}
				if h.Owner == OmniRiskProcessID || process.HeartbeatAt > common.GetTimestamp()-120 {
					return nil
				}
			}
			audit.PoolID = h.PoolID
			var prior OmniSharedRecovery
			if e := tx.First(&prior, "key = ?", audit.Key).Error; e == nil {
				if prior.RequestID != requestID || prior.Actual != actual || prior.Evidence != audit.Evidence || prior.ActorID != audit.ActorID {
					return errors.New("recovery key conflict")
				}
				return nil
			} else if !errors.Is(e, gorm.ErrRecordNotFound) {
				return e
			}
			if !audit.automatic && (h.State != "pending" || h.DispatchState != "unknown") {
				return errors.New("不是待核对的重启请求")
			}
			if err = tx.Create(audit).Error; err != nil {
				return err
			}
		}

		if h.State != "pending" {
			if h.Actual != actual {
				return errors.New("shared settlement replay conflict")
			}
			return nil
		}
		var alloc []OmniSharedAllocation
		if json.Unmarshal([]byte(h.Allocations), &alloc) != nil {
			return errors.New("invalid durable shared allocation")
		}
		charge := min(actual, h.Reserved)
		remaining := charge
		cost := int64(0)
		for _, a := range alloc {
			var batch OmniSharedBatch
			if err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&batch, "id = ? AND period_id = ?", a.BatchID, h.PeriodID).Error; err != nil {
				return err
			}
			if batch.Reserved < a.Quota {
				return errors.New("shared reservation invariant failed")
			}
			q := min(remaining, a.Quota)
			remaining -= q
			paid, e := omniIntRatio(batch.Used+q, batch.PriceCents*10000, batch.Granted, false)
			if e != nil {
				return e
			}
			cost += paid - batch.CostUsedMicros
			batch.CostUsedMicros = paid
			batch.Used += q
			batch.Reserved -= a.Quota
			if batch.Used+batch.Reserved > batch.Granted {
				return errors.New("shared pool overdraft")
			}
			if err = tx.Save(&batch).Error; err != nil {
				return err
			}
		}
		if remaining != 0 || cost > h.CostHoldMicros {
			return errors.New("shared settlement invariant failed")
		}
		h.Actual = actual
		h.CostUsedMicros = cost
		h.PlatformQuota = actual - charge
		h.State = "settled"
		if actual == 0 {
			h.State = "refunded"
		}
		h.SettledAt = time.Now().Unix()
		// An estimator defect is never charged to somebody else's wallet. Freeze
		// future admission and preserve the provider-cost difference for review.
		if actual > h.Reserved {
			p.Frozen = true
			if err = tx.Save(&p).Error; err != nil {
				return err
			}
		}
		if err = tx.Save(&h).Error; err != nil {
			return err
		}
		if err = tx.Model(&OmniReservation{}).Where("request_id = ? AND source = ?", requestID, "omni_shared").Updates(map[string]any{"state": h.State, "actual": actual, "platform_quota": h.PlatformQuota}).Error; err != nil {
			return err
		}
		changed = true
		return omniSharedAdvance(tx, &p, time.Now().Unix())
	})
	return changed, err
}

func OmniSharedView(uid int, poolID, periodID string) (map[string]any, error) {
	var out map[string]any
	err := DB.Transaction(func(tx *gorm.DB) error {
		p, err := omniCommercePool(tx, poolID)
		if err != nil {
			return err
		}
		if _, err = omniSharedMember(tx, poolID, uid); err != nil {
			return err
		}
		if err = omniSharedAdvance(tx, &p, time.Now().Unix()); err != nil {
			return err
		}
		if periodID == "" {
			periodID = p.CurrentPeriod
		}
		var period OmniSharedPeriod
		if err = tx.First(&period, "id = ? AND pool_id = ?", periodID, poolID).Error; err != nil {
			return err
		}
		members := make([]OmniSharedMember, 0)
		if err = tx.Where("pool_id = ?", poolID).Find(&members).Error; err != nil {
			return err
		}
		batches := make([]OmniSharedBatch, 0)
		if err = tx.Where("period_id = ?", period.ID).Order("sequence ASC").Find(&batches).Error; err != nil {
			return err
		}
		usage := []struct {
			UserID           int   `json:"user_id"`
			CostMicros       int64 `json:"cost_micros"`
			WalletCostMicros int64 `json:"wallet_cost_micros"`
		}{}
		if err = tx.Model(&OmniSharedHold{}).Where("period_id = ? AND state <> ?", period.ID, "pending").Select("user_id, SUM(cost_used_micros) AS cost_micros, SUM(wallet_cost_micros) AS wallet_cost_micros").Group("user_id").Scan(&usage).Error; err != nil {
			return err
		}
		daily := []struct {
			UserID           int   `json:"user_id"`
			Day              int64 `json:"day"`
			CostMicros       int64 `json:"cost_micros"`
			WalletCostMicros int64 `json:"wallet_cost_micros"`
		}{}
		if err = tx.Model(&OmniSharedHold{}).Where("period_id = ? AND state <> ?", period.ID, "pending").Select("user_id, CAST((settled_at + 28800) / 86400 AS BIGINT) AS day, SUM(cost_used_micros) AS cost_micros, SUM(wallet_cost_micros) AS wallet_cost_micros").Group("user_id, CAST((settled_at + 28800) / 86400 AS BIGINT)").Order("day ASC").Scan(&daily).Error; err != nil {
			return err
		}
		paid, used := int64(0), int64(0)
		for _, b := range batches {
			paid += b.PriceCents * 10000
			used += b.CostUsedMicros
		}
		periods := make([]OmniSharedPeriod, 0)
		if err = tx.Where("pool_id = ?", poolID).Order("sequence DESC").Find(&periods).Error; err != nil {
			return err
		}
		// Project only public display names. Never serialize complete User records.
		ids := make([]int, 0, len(members)+len(usage))
		for _, m := range members {
			ids = append(ids, m.UserID)
		}
		for _, u := range usage {
			ids = append(ids, u.UserID)
		}
		names := make(map[int]string)
		if len(ids) > 0 {
			var users []struct {
				ID          int
				DisplayName string
			}
			if err = tx.Model(&User{}).Select("id, display_name").Where("id IN ?", ids).Scan(&users).Error; err != nil {
				return err
			}
			for _, u := range users {
				name := strings.TrimSpace(u.DisplayName)
				if name == "" || strings.Contains(name, "@") {
					name = fmt.Sprintf("用户 %d", u.ID)
				}
				names[u.ID] = name
			}
		}
		walletCost := int64(0)
		for _, u := range usage {
			walletCost += u.WalletCostMicros
		}
		out = map[string]any{"billing_policy": OmniCustomerPublicPolicy(p.OwnerID), "pool": p, "period": period, "periods": periods, "members": members, "member_names": names, "batches": batches, "usage": usage, "daily": daily, "paid_micros": paid, "consumed_micros": used, "wallet_overage_micros": walletCost, "total_cost_micros": used + walletCost, "unconsumed_micros": paid - used, "server_time": time.Now().Unix()}
		return nil
	})
	return out, err
}
