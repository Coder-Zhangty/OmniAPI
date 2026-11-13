package service

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestOmniWallet403ExplainsReservation(t *testing.T) {
	previousDB := model.DB
	previousCache, previousRedis := common.MemoryCacheEnabled, common.RedisEnabled
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := database.DB()
	sqlDB.SetMaxOpenConns(1)
	model.DB = database
	common.MemoryCacheEnabled, common.RedisEnabled = false, false
	t.Cleanup(func() {
		model.DB = previousDB
		common.MemoryCacheEnabled, common.RedisEnabled = previousCache, previousRedis
		sqlDB.Close()
	})
	if err := database.AutoMigrate(&model.User{}, &model.UserSubscription{}, &model.SubscriptionPlan{}); err != nil {
		t.Fatal(err)
	}
	if err := model.OmniMigrate(); err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&model.User{Id: 9916, Username: "synthetic-wallet", Quota: 0}).Error; err != nil {
		t.Fatal(err)
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	info := &relaycommon.RelayInfo{UserId: 9916, RequestId: "synthetic-wallet-403", OriginModelName: "test-model", OmniBillingActive: true, IsPlayground: true, Request: &dto.ClaudeRequest{}}
	_, apiErr := NewOmniBillingSession(c, info, 1100)
	if apiErr == nil || apiErr.StatusCode != 403 {
		t.Fatalf("expected quota rejection: %v", apiErr)
	}
	message := apiErr.Error()
	if !strings.Contains(message, "钱包") || !strings.Contains(message, "耗尽") || strings.Contains(message, "订阅") {
		t.Fatalf("wallet failure mislabeled: %s", message)
	}
	var user model.User
	database.First(&user, 9916)
	if user.Quota != 0 {
		t.Fatal("failed request deducted wallet")
	}
}
