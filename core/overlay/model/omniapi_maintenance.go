package model

import (
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm/clause"
	"os"
	"time"
)

// Recovery is limited to users created after the explicit cutover. Existing
// registrations have already earned the legacy reward and must never replay.
func OmniMaintain() {
	p := GetOmniPolicy()
	if p.ReferralsEnabled && p.ReferralStartID > 0 {
		var users []User
		if err := DB.Select("id, inviter_id").Where("id >= ? AND inviter_id > 0 AND id NOT IN (SELECT CAST(SUBSTR(key, 10) AS INTEGER) FROM omni_ledgers WHERE kind = ?)", p.ReferralStartID, "referral").Limit(100).Find(&users).Error; err == nil {
			for _, u := range users {
				if err := OmniRewardReferral(u.InviterId, u.Id); err != nil {
					common.SysError("OmniAPI referral recovery: " + err.Error())
				}
			}
		}
	}
	if p.BenefitsEnabled() {
		var users []OmniSubscription
		if DB.Where("start <= ? AND (\"end\" > ? OR pending_months > 0)", time.Now().Unix(), time.Now().Unix()).Find(&users).Error == nil {
			for _, s := range users {
				if _, err := OmniClaimCards(s.UserID); err != nil {
					common.SysError("OmniAPI benefit: " + err.Error())
				}
			}
		}
	}
}
func StartOmniMaintenance() {
	go StartOmniTopupMaintenance()
	if !common.IsMasterNode {
		if os.Getenv("OMNI_BLUE_GREEN_WORKER") == "1" {
			go func() {
				ticker := time.NewTicker(10 * time.Second)
				defer ticker.Stop()
				for range ticker.C {
					if err := OmniSharedHeartbeat(); err != nil {
						common.SysError("green gateway shared heartbeat failed")
					}
				}
			}()
		}
		return
	}
	go func() {
		for {
			if DB != nil {
				var rows []OmniSubscription
				DB.Select("user_id").Where("(quota_version < ? AND \"end\" > ?) OR activation_pending = ? OR start > ? OR (\"end\" <= ? AND (pending_months > 0 OR user_id IN (SELECT user_id FROM omni_legacy_pauses WHERE state = ?)))", OmniQuotaVersion, time.Now().Unix(), true, time.Now().Unix(), time.Now().Unix(), "paused").Limit(500).Find(&rows)
				for _, s := range rows {
					_ = OmniReconcile(s.UserID)
				}
			}
			time.Sleep(30 * time.Second)
		}
	}()
	go func() {
		for {
			OmniMaintain()
			time.Sleep(time.Hour)
		}
	}()
}

var omniAnnouncements = []OmniNotice{
	{Key: "announcement:subscription-v2", Title: "新订阅体系", Body: "月付与年付订阅已上线。按API零售价同时消耗五小时与每周额度，范围内的文本模型可用；不包含的模型按钱包价格计费。余额续用可在钱包关闭，续费不刷新窗口。纯文本续写为尽力恢复，不保证与原回答完全一致。"},
	{Key: "announcement:membership-v2", Title: "VIP / SVIP 会员福利", Body: "真实累计净钱包充值满200元升级VIP，满1000元升级SVIP，充值分别加送3%与5%。VIP每订阅月赠一张五小时卡，SVIP赠两张五小时卡和一张周卡；卡片30天有效，重置不改变原周期。订阅付款、邀请与赠额不计入会员门槛。"},
}

func OmniAnnouncements(uid int) ([]OmniNotice, error) {
	rows := []OmniNotice{}
	// Customer support notices are private and independent of general announcements.
	if err := DB.Where("user_id = ? AND read = ? AND key LIKE ?", uid, false, "announcement:customer:%").Order("created_at DESC").Limit(10).Find(&rows).Error; err != nil {
		return nil, err
	}
	if !GetOmniPolicy().AnnouncementsEnabled || !OmniUserInRollout(uid) {
		return rows, nil
	}
	for _, n := range omniAnnouncements {
		n.Key = fmt.Sprintf("%s:%d", n.Key, uid)
		n.UserID = uid
		n.CreatedAt = time.Now().Unix()
		if err := DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&n).Error; err != nil {
			return nil, err
		}
		if err := DB.First(&n, "key = ?", n.Key).Error; err != nil {
			return nil, err
		}
		if !n.Read {
			rows = append(rows, n)
		}
	}
	return rows, nil
}
