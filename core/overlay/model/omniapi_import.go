package model

import (
	"errors"
	"gorm.io/gorm"
	"strings"
	"time"
)

// Already credited, verified legacy receipts establish net membership spend.
// They must not issue base credit or a retrospective bonus a second time.
func OmniImportReceipt(key string, uid int, cents, originalQuota int64) error {
	key = strings.TrimPrefix(key, "legacy:")
	if len(key) == 0 || len(key) > 120 || uid <= 0 || cents <= 0 || originalQuota <= 0 {
		return errors.New("invalid legacy receipt")
	}
	var user User
	if DB.Select("id").First(&user, "id = ?", uid).Error != nil {
		return errors.New("user not found")
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		a, err := omniAccount(tx, uid)
		if err != nil {
			return err
		}
		var r OmniLedger
		err = tx.First(&r, "key = ?", key).Error
		if err == nil {
			if r.Kind != "topup" || r.UserID != uid || r.Cents != cents || r.Quota != originalQuota {
				return errors.New("legacy receipt conflict")
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		a.NetRechargeCents += cents
		if err = tx.Save(&a).Error; err != nil {
			return err
		}
		return tx.Create(&OmniLedger{Key: key, UserID: uid, Kind: "topup", Imported: true, Cents: cents, Quota: originalQuota, CreatedAt: time.Now().Unix()}).Error
	})
}
