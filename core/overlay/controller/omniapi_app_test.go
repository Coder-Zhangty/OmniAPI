package controller

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOmniAdminUserShowsFreshWalletAndSeparateAllowance(t *testing.T) {
 user,_:=appFixture(t)
 require.NoError(t,model.OmniMigrate())
 require.NoError(t,model.DB.Model(user).Update("quota",12345).Error)
 now:=time.Now().Unix()
 subscription:=model.OmniSubscription{UserID:user.Id,PlanKey:"standard",Start:now-18060,End:now+86400,FiveLimit:1000,FiveUsed:1200,FivePaused:true,WeekLimit:4000,WeekUsed:1500,BillingVersion:2,QuotaVersion:2}
 require.NoError(t,model.DB.Create(&subscription).Error)
 get:=func(role int)*httptest.ResponseRecorder {
  recorder:=httptest.NewRecorder();c,_:=gin.CreateTestContext(recorder)
  c.Request=httptest.NewRequest("GET","/api/user/1",nil)
  c.Params=gin.Params{{Key:"id",Value:fmt.Sprint(user.Id)}};c.Set("role",role)
  GetOmniAdminUser(c);return recorder
 }
 var result struct {Success bool `json:"success"`;Data struct {Quota int `json:"quota"`;Password string `json:"password"`;Billing model.OmniAdminUsage `json:"billing"`} `json:"data"`}
 recorder:=get(common.RoleRootUser)
 require.NoError(t,json.Unmarshal(recorder.Body.Bytes(),&result));require.True(t,result.Success)
 require.Equal(t,12345,result.Data.Quota);require.Empty(t,result.Data.Password)
 require.Equal(t,"no-store",recorder.Header().Get("Cache-Control"))
 require.Equal(t,"active",result.Data.Billing.Status);require.Len(t,result.Data.Billing.Windows,2)
 require.EqualValues(t,1000,result.Data.Billing.Windows[0].Remaining)
 require.False(t,result.Data.Billing.Windows[0].Paused)
 require.EqualValues(t,2500,result.Data.Billing.Windows[1].Remaining)
 var stored model.OmniSubscription;require.NoError(t,model.DB.First(&stored,"user_id = ?",user.Id).Error)
 require.EqualValues(t,1200,stored.FiveUsed);require.True(t,stored.FivePaused)
 require.NoError(t,model.DB.Model(user).Update("quota",12000).Error)
 require.NoError(t,json.Unmarshal(get(common.RoleRootUser).Body.Bytes(),&result));require.Equal(t,12000,result.Data.Quota)
 require.NoError(t,json.Unmarshal(get(common.RoleCommonUser).Body.Bytes(),&result));require.False(t,result.Success)
}

func appFixture(t *testing.T) (*model.User, *service.AuthBundle) {
	previousDB, previousRedis, previousSecret := model.DB, common.RedisEnabled, common.SessionSecret
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.OmniAppLogin{}, &model.OmniAppChildSession{}))
	model.DB = db
	common.RedisEnabled = false
	common.SessionSecret = "omni-native-test-session-secret"
	t.Cleanup(func() {
		model.DB = previousDB
		common.RedisEnabled = previousRedis
		common.SessionSecret = previousSecret
	})
	user := &model.User{Username: "native-test-user", Password: "unused", Role: common.RoleCommonUser, Status: common.UserStatusEnabled, Group: "default", AuthVersion: 1}
	require.NoError(t, db.Create(user).Error)
	bundle, err := service.CreateLoginSession(user.Id, "password", "127.0.0.1", "test-browser")
	require.NoError(t, err)
	return user, bundle
}
func appRequest(t *testing.T, handler gin.HandlerFunc, body any, user *model.User, session *service.AuthBundle) *httptest.ResponseRecorder {
	encoded, err := json.Marshal(body)
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("POST", "https://omniapi.biz/api/app/login", bytes.NewReader(encoded))
	c.Request.Header.Set("Content-Type", "application/json")
	if user != nil {
		c.Set("id", user.Id)
		c.Set("session_id", session.Session.SID)
		c.Set("auth_version", int64(1))
		c.Set("session_version", int64(1))
	}
	handler(c)
	return recorder
}
func approvedFlow(t *testing.T, user *model.User, parent *service.AuthBundle) (string, string) {
	verifier := strings.Repeat("v", 64)
	hash := sha256.Sum256([]byte(verifier))
	recorder := appRequest(t, OmniAppStart, map[string]any{"challenge": base64.RawURLEncoding.EncodeToString(hash[:]), "device": "native-device-12345678", "platform": "windows"}, nil, nil)
	require.Equal(t, 200, recorder.Code)
	var result struct {
		Data struct {
			Request string `json:"request"`
		}
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &result))
	pending := appRequest(t, OmniAppExchange, map[string]string{"request": result.Data.Request, "verifier": verifier}, nil, nil)
	require.Equal(t, 428, pending.Code)
	approved := appRequest(t, OmniAppApprove, map[string]string{"request": result.Data.Request}, user, parent)
	require.Equal(t, 200, approved.Code)
	return result.Data.Request, verifier
}
func TestOmniAppPKCEAndOneTimeExchange(t *testing.T) {
	user, parent := appFixture(t)
	request, verifier := approvedFlow(t, user, parent)
	wrong := appRequest(t, OmniAppExchange, map[string]string{"request": request, "verifier": strings.Repeat("x", 64)}, nil, nil)
	require.Equal(t, 403, wrong.Code)
	exchanged := appRequest(t, OmniAppExchange, map[string]string{"request": request, "verifier": verifier}, nil, nil)
	require.Equal(t, 200, exchanged.Code)
	var result struct {
		Data struct {
			UserID  int                `json:"user_id"`
			Refresh string             `json:"refresh_token"`
			Device  string             `json:"device"`
			Auth    service.AuthBundle `json:"auth"`
		}
	}
	require.NoError(t, json.Unmarshal(exchanged.Body.Bytes(), &result))
	require.Equal(t, user.Id, result.Data.UserID)
	require.NotEmpty(t, result.Data.Refresh)
	require.NotEmpty(t, result.Data.Auth.AccessToken)
	require.Equal(t, "native-device-12345678", result.Data.Device)
	replay := appRequest(t, OmniAppExchange, map[string]string{"request": request, "verifier": verifier}, nil, nil)
	require.Equal(t, 410, replay.Code)
}
func TestOmniAppRevokedApprovalCannotCreateSession(t *testing.T) {
	user, parent := appFixture(t)
	request, verifier := approvedFlow(t, user, parent)
	_, err := model.RevokeUserSession(user.Id, parent.Session.SID, "test")
	require.NoError(t, err)
	recorder := appRequest(t, OmniAppExchange, map[string]string{"request": request, "verifier": verifier}, nil, nil)
	require.Equal(t, 401, recorder.Code)
}
func TestOmniAppWebViewCannotOutliveParent(t *testing.T) {
	user, parent := appFixture(t)
	request, verifier := approvedFlow(t, user, parent)
	recorder := appRequest(t, OmniAppWebExchange, map[string]string{"request": request, "verifier": verifier}, nil, nil)
	require.Equal(t, 200, recorder.Code)
	require.NotContains(t, recorder.Body.String(), "access_token")
	require.NotContains(t, recorder.Body.String(), "refresh_token")
	var child model.UserSession
	require.NoError(t, model.DB.First(&child, "login_method = ?", "native:webview").Error)
	identity := service.AuthIdentity{UserID: user.Id, SessionID: child.SID, UserAuthVersion: child.UserAuthVersion, SessionVersion: child.Version}
	_, _, err := service.ValidateLoginSession(identity)
	require.NoError(t, err)
	var cookie string
	for _, value := range recorder.Result().Cookies() {
		if value.Name == service.RefreshCookieName {
			cookie = value.Value
		}
	}
	require.NotEmpty(t, cookie)
	_, err = model.RevokeUserSession(user.Id, parent.Session.SID, "native_logout")
	require.NoError(t, err)
	_, _, err = service.ValidateLoginSession(identity)
	require.Error(t, err)
	_, _, err = service.RefreshLoginSession(cookie, child.SID, "127.0.0.1", "test")
	require.Error(t, err)
	require.Greater(t, child.ExpiresAt, time.Now().Unix())
}

func TestOmniPreferenceReturnsCommittedUserScopedValue(t *testing.T) {
	oldDB, oldRedis := model.DB, common.RedisEnabled
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	model.DB = database
	common.RedisEnabled = false
	t.Cleanup(func() { model.DB = oldDB; common.RedisEnabled = oldRedis; sqlDB.Close() })
	require.NoError(t, database.AutoMigrate(&model.User{}, &model.UserSubscription{}, &model.SubscriptionPlan{}))
	require.NoError(t, model.OmniMigrate())
	require.NoError(t, database.Create(&model.User{Id: 7, Username: "preference-api", Quota: 1000}).Error)
	now := time.Now().Unix()
	require.NoError(t, database.Create(&model.OmniSubscription{UserID: 7, Start: now - 60, End: now + 86400, FiveLimit: 1000, WeekLimit: 4000, FivePaused: true, BillingVersion: 2, QuotaVersion: 2, IncludedModels: `{"test-model":{"eligible":true}}`}).Error)
	require.NoError(t, database.Create(&model.OmniAccount{UserID: 8, WalletOverflow: true}).Error)
	for _, enabled := range []bool{false, true, false} {
		encoded, err := common.Marshal(map[string]any{"wallet_overflow": enabled, "user_id": 8})
		require.NoError(t, err)
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Set("id", 7)
		c.Request = httptest.NewRequest("POST", "/api/omni/preferences", bytes.NewReader(encoded))
		c.Request.Header.Set("Content-Type", "application/json")
		OmniPreference(c)
		require.Equal(t, 200, recorder.Code)
		var response struct {
			Success bool `json:"success"`
			Data    struct {
				WalletOverflow bool `json:"wallet_overflow"`
			}
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
		require.True(t, response.Success)
		require.Equal(t, enabled, response.Data.WalletOverflow)
		var account model.OmniAccount
		require.NoError(t, database.First(&account, "user_id = ?", 7).Error)
		require.Equal(t, enabled, account.WalletOverflow)
		var other model.OmniAccount
		require.NoError(t, database.First(&other, "user_id = ?", 8).Error)
		require.True(t, other.WalletOverflow)
		reservation, err := model.OmniReserveAllowance(7, fmt.Sprintf("api-immediate-%t", enabled), "test-model", 100)
		if enabled {
			require.NoError(t, err)
			require.Equal(t, "wallet", reservation.Source)
			require.EqualValues(t, 0, reservation.Reserved)
		} else {
			require.ErrorIs(t, err, model.ErrOmniInsufficient)
		}
	}
	for _, body := range []string{`{}`, `{"wallet_overflow":null}`, `{"wallet_overflow":"true"}`} {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Set("id", 7)
		c.Request = httptest.NewRequest("POST", "/api/omni/preferences", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		OmniPreference(c)
		require.Equal(t, 409, recorder.Code)
	}
	var account model.OmniAccount
	require.NoError(t, database.First(&account, "user_id = ?", 7).Error)
	require.False(t, account.WalletOverflow)
}

func TestOmniWalletContinuationAcrossNodesWithMockUpstream(t *testing.T) {
	user, _ := appFixture(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Token{}, &model.UserSubscription{}, &model.SubscriptionPlan{}))
	require.NoError(t, model.OmniMigrate())
	require.NoError(t, model.DB.Model(user).Update("quota", 10000).Error)
	now := time.Now().Unix()
	require.NoError(t, model.DB.Create(&model.OmniSubscription{UserID: user.Id, Start: now-60, End: now+86400, FiveLimit:1000, WeekLimit:4000, FivePaused:true, BillingVersion:2, QuotaVersion:2}).Error)
	upstreamCalls := atomic.Int32{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { upstreamCalls.Add(1); w.Write([]byte(`{"quota":100}`)) }))
	defer upstream.Close()
	requestID := atomic.Int32{}
	newNode := func() *httptest.Server {
		router := gin.New()
		router.Use(func(c *gin.Context) { c.Set("id",user.Id) })
		router.POST("/api/omni/preferences",OmniPreference)
		router.POST("/call",func(c *gin.Context) {
			info := &relaycommon.RelayInfo{UserId:user.Id,RequestId:fmt.Sprintf("mock-node-%d",requestID.Add(1)),OriginModelName:"test-model",OmniBillingActive:true,IsPlayground:true}
			session,err:=service.NewOmniBillingSession(c,info,800)
			if err!=nil { c.Status(http.StatusForbidden);return }
			response,fetchErr:=http.Get(upstream.URL);require.NoError(t,fetchErr);defer response.Body.Close()
			var usage struct{Quota int};require.NoError(t,json.NewDecoder(response.Body).Decode(&usage))
			require.NoError(t,session.Settle(usage.Quota));require.NoError(t,session.Settle(usage.Quota))
			c.JSON(200,gin.H{"source":info.BillingSource})
		})
		return httptest.NewServer(router)
	}
	primary,relay:=newNode(),newNode();defer primary.Close();defer relay.Close()
	for _,enabled:=range []bool{false,true,false,true} {
		body:=fmt.Sprintf(`{"wallet_overflow":%t}`,enabled)
		response,err:=http.Post(primary.URL+"/api/omni/preferences","application/json",strings.NewReader(body));require.NoError(t,err);require.Equal(t,200,response.StatusCode);response.Body.Close()
		response,err=http.Post(relay.URL+"/call","application/json",strings.NewReader(`{}`));require.NoError(t,err)
		if enabled { require.Equal(t,200,response.StatusCode) } else { require.Equal(t,403,response.StatusCode) };response.Body.Close()
	}
	require.EqualValues(t,2,upstreamCalls.Load())
	var fresh model.User;require.NoError(t,model.DB.First(&fresh,user.Id).Error);require.Equal(t,9800,fresh.Quota)
	var subscription model.OmniSubscription;require.NoError(t,model.DB.First(&subscription,"user_id = ?",user.Id).Error);require.True(t,subscription.FivePaused)
}
