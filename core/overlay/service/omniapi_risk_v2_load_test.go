package service

import (
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRiskV2PostgresEventPropagationAtDoublePeak(t *testing.T) {
	dsn := os.Getenv("OMNI_RISK_TEST_DSN")
	if dsn == "" {
		t.Skip("requires the disposable PostgreSQL runner")
	}
	require.Contains(t, dsn, "omniapi_risk_test_")
	db, e := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, e)
	var name string
	require.NoError(t, db.Raw("SELECT current_database()").Scan(&name).Error)
	require.True(t, strings.HasPrefix(name, "omniapi_risk_test_"))
	tables, e := db.Migrator().GetTables()
	require.NoError(t, e)
	for _, table := range tables {
		require.NoError(t, db.Migrator().DropTable(table))
	}
	sql, _ := db.DB()
	sql.SetMaxOpenConns(10)
	oldDB, oldRedis := model.DB, common.RedisEnabled
	model.DB = db
	common.RedisEnabled = false
	t.Cleanup(func() { model.DB = oldDB; common.RedisEnabled = oldRedis; sql.Close() })
	secret := filepath.Join(t.TempDir(), "risk-fixture.json")
	require.NoError(t, os.WriteFile(secret, []byte(`{"hmac_key":"kkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkk","api_key":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}`), 0600))
	t.Setenv("OMNI_RISK_SECRETS_FILE", secret)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.Token{}, &model.UserSession{}, &model.OmniAccount{}, &model.OmniLedger{}, &model.Log{}, &model.AuditLog{}, &model.OmniNotice{}))
	require.NoError(t, model.OmniRiskMigrate(db))
	require.NoError(t, db.Model(&model.OmniRiskPolicyV2{}).Where("id = 1").Updates(map[string]any{"auto_hold": true, "new_user_protection": true, "shadow": false}).Error)
	now := time.Now().Unix()
	for uid := 5000; uid < 5092; uid++ {
		require.NoError(t, db.Create(&model.User{Id: uid, Username: fmt.Sprint("load-", uid), AffCode: fmt.Sprint("load-aff-", uid), Role: 1, Status: 1, AuthVersion: 1, Quota: 100000, CreatedAt: now}).Error)
	}
	riskV2EventCursor = 0
	riskV2CursorLoaded = false
	riskV2LastSlow = 0
	riskV2LastReconcile = 0
	riskV2LastCache = 0
	stop := make(chan struct{})
	failures := make(chan error, 20)
	var workers sync.WaitGroup
	var attempts atomic.Int64
	workers.Add(2)
	go func() {
		defer workers.Done()
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if e := riskV2Maintenance(); e != nil {
					select {
					case failures <- e:
					default:
					}
				}
			}
		}
	}()
	// Read-only baseline found 6 requests/s. Replay 12/s while the event worker
	// applies controls, using no upstream network or production database.
	go func() {
		defer workers.Done()
		ticker := time.NewTicker(time.Second / 12)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				request := fmt.Sprint("background-", attempts.Add(1))
				r, e := model.OmniRiskReserveV2(5091, request, 14, 1000, `{"input":"1","output":"2","fx":"7"}`)
				if e == nil && r != nil {
					e = model.OmniRiskFinishV2(r.ID, 0, false, true)
				}
				if e != nil {
					select {
					case failures <- e:
					default:
					}
				}
				if e = model.OmniRiskRecordV2(5091, "model.admission", request, "9.9.9.9", "load-fixture", 0, 1); e != nil {
					select {
					case failures <- e:
					default:
					}
				}
			}
		}
	}()
	t.Cleanup(func() { close(stop); workers.Wait() })
	latencies := []time.Duration{}
	for batch := 0; batch < 30; batch++ {
		events := []model.OmniRiskEventV2{}
		for member := 0; member < 3; member++ {
			uid := 5000 + batch*3 + member
			for j, kind := range []string{"registration.success", "login.success", "token.create", "token.view"} {
				events = append(events, model.OmniRiskEventV2{Key: fmt.Sprint(uid, ":", j), UserID: uid, Kind: kind, CreatedAt: time.Now().Unix() - 3 + int64(j), Quota: 100, Network: fmt.Sprint("source-", batch), Browser: "exact-browser"})
			}
		}
		require.NoError(t, db.Create(&events).Error)
		started := time.Now()
		for {
			var n int64
			require.NoError(t, db.Model(&model.OmniRiskControl{}).Where("user_id BETWEEN ? AND ? AND state = ?", 5000+batch*3, 5002+batch*3, "hold").Count(&n).Error)
			if n == 3 {
				break
			}
			require.Less(t, time.Since(started), 5*time.Second)
			time.Sleep(10 * time.Millisecond)
		}
		latencies = append(latencies, time.Since(started))
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p95 := latencies[(len(latencies)*95+99)/100-1]
	t.Logf("event commit to hold P95 at 12 requests/s: %s; background attempts: %d", p95, attempts.Load())
	require.Less(t, p95, 2*time.Second)
	select {
	case e := <-failures:
		require.NoError(t, e)
	default:
	}
}
