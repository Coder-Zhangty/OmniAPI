package model

import (
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// Attach the worker to the already migrated schema. A green slot must have
// the same live funding and risk admission as the master before serving HTTP.
func OmniAttachServingWorker(db *gorm.DB) error {
	if db == nil || db != DB {
		return errors.New("serving worker database unavailable")
	}
	for _, table := range []any{
		&OmniCommerceProfile{}, &OmniCommerceRelease{}, &OmniAccount{}, &OmniReservation{},
		&OmniSubscription{}, &OmniSharedPool{}, &OmniSharedMember{}, &OmniSharedPeriod{},
		&OmniSharedBatch{}, &OmniSharedHold{}, &OmniSharedRecovery{}, &OmniSharedProcess{},
		&OmniAttempt{}, &OmniKuaiQuarantine{}, &OmniRiskPolicyV2{}, &OmniRiskControl{},
		&OmniRiskIP{}, &OmniRiskEventV2{}, &OmniRiskExposure{}, &OmniRiskTokenFence{},
	} {
		if !db.Migrator().HasTable(table) {
			return fmt.Errorf("serving worker schema is missing %T", table)
		}
	}
	var policy OmniRiskPolicyV2
	if err := db.First(&policy, 1).Error; err != nil {
		return err
	}
	omniCommerceReadyMu.Lock()
	omniCommerceDB = db
	omniCommerceReadyMu.Unlock()
	riskV2ReadyMu.Lock()
	riskV2DB, riskV2Ready = db, true
	riskV2ReadyMu.Unlock()
	return OmniSharedHeartbeat()
}
