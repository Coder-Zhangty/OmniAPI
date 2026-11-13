package service

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestOmniSubscription403ExplainsBothWindows(t *testing.T) {
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
	common.OptionMapRWMutex.Lock()
	oldPolicy, oldPricing := common.OptionMap["OmniAPICommercial"], common.OptionMap["OmniAPIPricing"]
	if common.OptionMap == nil {
		common.OptionMap = map[string]string{}
	}
	common.OptionMap["OmniAPICommercial"] = `{"billing_enabled":true,"model_scope":"all_available"}`
	common.OptionMap["OmniAPIPricing"] = `{"enabled":true,"models":{"test-model":{}}}`
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		model.DB = previousDB
		common.MemoryCacheEnabled, common.RedisEnabled = previousCache, previousRedis
		common.OptionMapRWMutex.Lock()
		common.OptionMap["OmniAPICommercial"], common.OptionMap["OmniAPIPricing"] = oldPolicy, oldPricing
		common.OptionMapRWMutex.Unlock()
		sqlDB.Close()
	})
	if err = database.AutoMigrate(&model.User{}, &model.UserSubscription{}, &model.SubscriptionPlan{}); err != nil {
		t.Fatal(err)
	}
	if err = model.OmniMigrate(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	if err = database.Create(&model.User{Id: 9916, Username: "synthetic-subscription", Quota: 2820950}).Error; err != nil {
		t.Fatal(err)
	}
	if err = database.Create(&model.OmniAccount{UserID: 9916}).Error; err != nil {
		t.Fatal(err)
	}
	if err = database.Model(&model.OmniAccount{}).Where("user_id = ?", 9916).Update("wallet_overflow", false).Error; err != nil {
		t.Fatal(err)
	}
	initial := model.OmniSubscription{UserID: 9916, PlanKey: "standard", Start: now - 60, End: now + 86400, FxMicros: 6735100, FiveLimit: 307345, WeekLimit: 1228638, FiveUsed: 0, WeekUsed: 3708, QuotaVersion: 2, BillingVersion: 2, IncludedModels: "{}"}
	if err = database.Create(&initial).Error; err != nil {
		t.Fatal(err)
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	info := &relaycommon.RelayInfo{UserId: 9916, RequestId: "synthetic-subscription-403", OriginModelName: "test-model", OmniBillingActive: true, IsPlayground: true, Request: &dto.AudioRequest{}}
	_, apiErr := NewOmniBillingSession(c, info, 800000)
	if apiErr == nil || apiErr.StatusCode != 403 {
		t.Fatalf("expected quota rejection: %v", apiErr)
	}
	message := apiErr.Error()
	for _, part := range []string{"订阅", "预扣", "五小时", "七天", "最大输出", "钱包余额续用", "上限"} {
		if !strings.Contains(message, part) {
			t.Errorf("missing %q in subscription error: %s", part, message)
		}
	}
	var user model.User
	database.First(&user, 9916)
	var subscription model.OmniSubscription
	database.First(&subscription, "user_id = ?", 9916)
	var count int64
	database.Model(&model.OmniReservation{}).Count(&count)
	if user.Quota != 2820950 || subscription.FiveUsed != initial.FiveUsed || subscription.WeekUsed != initial.WeekUsed || count != 0 {
		t.Fatal("rejected request changed billing state")
	}
	info.RequestId = "synthetic-subscription-small"
	session, apiErr := NewOmniBillingSession(c, info, 1000)
	if apiErr != nil {
		t.Fatal("small request must use subscription", apiErr)
	}
	if session.funding.Source() != "omni_subscription" {
		t.Fatal("small request charged wallet")
	}
	if err = session.funding.Refund(); err != nil {
		t.Fatal(err)
	}
	database.First(&user, 9916)
	if user.Quota != 2820950 {
		t.Fatal("wallet changed while overflow disabled")
	}
	if err = database.Model(&model.OmniSubscription{}).Where("user_id = ?", 9916).Update("five_used", 307000).Error; err != nil {
		t.Fatal(err)
	}
	info.RequestId = "synthetic-subscription-reset"
	_, apiErr = NewOmniBillingSession(c, info, 1000)
	if apiErr == nil || !strings.Contains(apiErr.Error(), "北京时间") || !strings.Contains(apiErr.Error(), "重置后重试") || strings.Contains(apiErr.Error(), "等待重置仍不足") {
		t.Fatalf("missing reset guidance: %v", apiErr)
	}
	info.RequestId = "synthetic-subscription-model"
	info.OriginModelName = "unsupported-model"
	_, apiErr = NewOmniBillingSession(c, info, 100)
	if apiErr == nil || !strings.Contains(apiErr.Error(), "模型或请求类型") || strings.Contains(apiErr.Error(), "等待额度重置") {
		t.Fatalf("model rejection mislabeled as quota: %v", apiErr)
	}
}
