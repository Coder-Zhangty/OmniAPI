package model

import (
	"encoding/json"
	"github.com/QuantumNous/new-api/common"
	"testing"
	"time"
)

func syncFixture(t *testing.T) {
	walletFixture(t)
	if err := DB.AutoMigrate(&OmniAppLogin{}, &OmniAppChildSession{}, &OmniSyncSubscription{}); err != nil {
		t.Fatal(err)
	}
	common.OptionMapRWMutex.Lock()
	previous := common.OptionMap["OmniAPISync"]
	raw, _ := json.Marshal(OmniSyncPolicy{Enabled: true, MonthlyCents: 500, QuotaBytes: 1073741824})
	common.OptionMap["OmniAPISync"] = string(raw)
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap["OmniAPISync"] = previous
		common.OptionMapRWMutex.Unlock()
	})
}
func TestOmniSyncPurchaseIsAtomicAndIdempotent(t *testing.T) {
	syncFixture(t)
	var before User
	DB.First(&before, 91)
	first, err := OmniSyncPurchase(91, "sync-purchase-key-1234")
	if err != nil {
		t.Fatal(err)
	}
	var after User
	DB.First(&after, 91)
	if after.Quota >= before.Quota || first.End <= time.Now().Unix() {
		t.Fatal("wallet or entitlement was not updated")
	}
	duplicate, err := OmniSyncPurchase(91, "sync-purchase-key-1234")
	if err != nil {
		t.Fatal(err)
	}
	var unchanged User
	DB.First(&unchanged, 91)
	if duplicate.End != first.End || unchanged.Quota != after.Quota {
		t.Fatal("duplicate charged or renewed twice")
	}
	DB.Model(&User{}).Where("id = ?", 91).Update("quota", 0)
	if _, err := OmniSyncPurchase(91, "sync-no-balance-key-1234"); err == nil {
		t.Fatal("purchase accepted without funds")
	}
	stored, _ := OmniSyncEntitlement(91)
	if stored.End != first.End {
		t.Fatal("failed charge changed entitlement")
	}
	var n int64
	DB.Model(&OmniLedger{}).Where("kind = ?", "sync:monthly").Count(&n)
	if n != 1 {
		t.Fatal("duplicate or failed ledger persisted")
	}
}
func TestOmniSyncDisabledAndInvalidPriceCannotCharge(t *testing.T) {
	syncFixture(t)
	common.OptionMapRWMutex.Lock()
	common.OptionMap["OmniAPISync"] = `{"enabled":true,"monthly_cents":0,"quota_bytes":1073741824}`
	common.OptionMapRWMutex.Unlock()
	if OmniSyncConfig().Enabled {
		t.Fatal("zero-price policy enabled")
	}
	if _, err := OmniSyncPurchase(91, "sync-disabled-key-1234"); err == nil {
		t.Fatal("disabled purchase accepted")
	}
	var n int64
	DB.Model(&OmniLedger{}).Where("kind = ?", "sync:monthly").Count(&n)
	if n != 0 {
		t.Fatal("disabled product charged")
	}
}
