package model

import (
	"fmt"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func riskDB(t *testing.T) *gorm.DB {
	t.Helper()
	old := DB
	var dialect gorm.Dialector = sqlite.Open(":memory:")
	dsn := os.Getenv("OMNI_RISK_TEST_DSN")
	if dsn != "" {
		require.Contains(t, dsn, "omniapi_risk_test_")
		dialect = postgres.Open(dsn)
	}
	db, err := gorm.Open(dialect, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sql, _ := db.DB()
	sql.SetMaxOpenConns(1)
	if dsn != "" {
		sql.SetMaxOpenConns(10)
		var database string
		require.NoError(t, db.Raw("SELECT current_database()").Scan(&database).Error)
		require.True(t, strings.HasPrefix(database, "omniapi_risk_test_"), "tests require a disposable risk database")
		tables, e := db.Migrator().GetTables()
		require.NoError(t, e)
		for _, table := range tables {
			require.NoError(t, db.Migrator().DropTable(table))
		}
	}
	DB = db
	t.Cleanup(func() { DB = old; sql.Close() })
	require.NoError(t, OmniRiskMigrate(db))
	return db
}
func TestOmniRiskConcurrentBudgetAndUnknownHold(t *testing.T) {
	db := riskDB(t)
	epoch, ok, err := OmniRiskAcquire("a", time.Now().Unix())
	require.NoError(t, err)
	require.True(t, ok)
	var success atomic.Int32
	var wg sync.WaitGroup
	day := OmniRiskDayKey(time.Now())
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := OmniRiskCall{ID: fmt.Sprint(i), Day: day, State: "reserved", Reserved: 200000}
			if OmniRiskReserve(&c, "a", epoch) == nil {
				success.Add(1)
			}
		}(i)
	}
	wg.Wait()
	require.Equal(t, int32(5), success.Load())
	var calls []OmniRiskCall
	require.NoError(t, db.Find(&calls).Error)
	require.Len(t, calls, 5)
	require.NoError(t, OmniRiskSettle(calls[0].ID, 0, false))
	var d OmniRiskDay
	db.First(&d, "day = ?", day)
	require.Equal(t, int64(1000000), d.Reserved)
	require.NoError(t, OmniRiskSettle(calls[0].ID, 50000, false))
	require.NoError(t, OmniRiskSettle(calls[0].ID, 50000, false))
	db.First(&d, "day = ?", day)
	require.Equal(t, int64(800000), d.Reserved)
	require.Equal(t, int64(50000), d.Spent)
	require.NoError(t, OmniRiskSettle(calls[1].ID, 0, true))
	db.First(&d, "day = ?", day)
	require.Equal(t, int64(600000), d.Reserved)
}
func TestOmniRiskLeaseFencingAndTimezone(t *testing.T) {
	db := riskDB(t)
	now := time.Now().Unix()
	epoch, ok, err := OmniRiskAcquire("a", now)
	require.NoError(t, err)
	require.True(t, ok)
	_, ok, err = OmniRiskAcquire("b", now)
	require.NoError(t, err)
	require.False(t, ok)
	require.NoError(t, db.Model(&OmniRiskLease{}).Where("key = ?", "worker").Update("expires", now-1).Error)
	next, ok, err := OmniRiskAcquire("b", now)
	require.NoError(t, err)
	require.True(t, ok)
	require.Greater(t, next, epoch)
	require.Error(t, db.Transaction(func(tx *gorm.DB) error { return OmniRiskFence(tx, "a", epoch) }))
	require.False(t, OmniRiskRenew("a", epoch, now))
	require.Equal(t, "2026-10-17", OmniRiskDayKey(time.Date(2026, 10, 16, 16, 0, 0, 0, time.UTC)))
}
func TestOmniRiskTagsAndIncidentDedup(t *testing.T) {
	db := riskDB(t)
	key := strings.Repeat("s", 40)
	tag := OmniRiskTag(key, "network", "192.0.2.1")
	require.Len(t, tag, 24)
	require.NotContains(t, tag, "192.0.2.1")
	require.NotEqual(t, tag, OmniRiskTag(key, "browser", "192.0.2.1"))
	require.NoError(t, OmniRiskOperational("review_incomplete", true))
	require.NoError(t, OmniRiskOperational("review_incomplete", true))
	var n int64
	db.Model(&OmniRiskOutbox{}).Count(&n)
	require.Equal(t, int64(1), n)
	require.NoError(t, OmniRiskOperational("review_incomplete", false))
	db.Model(&OmniRiskOutbox{}).Count(&n)
	require.Equal(t, int64(2), n)
}
