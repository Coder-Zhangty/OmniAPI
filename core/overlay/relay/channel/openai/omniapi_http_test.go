package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func omniHTTPFixture(t *testing.T, handlers ...http.HandlerFunc) (*gin.Context, *relaycommon.RelayInfo) {
	t.Helper()
	// Transport fixtures own their policy state; package-wide tests must not
	// inherit commerce/email enablement from an unrelated acceptance fixture.
	common.OptionMapRWMutex.Lock()
	if common.OptionMap == nil { common.OptionMap = map[string]string{} }
	previousPolicies := map[string]string{}
	for _, key := range []string{"OmniAPIEmailV3", "OmniAPICommerceV3", "OmniAPIRiskV2"} {
		previousPolicies[key] = common.OptionMap[key]
		common.OptionMap[key] = `{"enabled":false}`
	}
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		for key, value := range previousPolicies { common.OptionMap[key] = value }
		common.OptionMapRWMutex.Unlock()
	})
	oldDB := model.DB
	oldCache := common.MemoryCacheEnabled
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	model.DB = db
	common.MemoryCacheEnabled = false
	sql, _ := db.DB()
	sql.SetMaxOpenConns(1)
	db.AutoMigrate(&model.Channel{})
	db.AutoMigrate(&model.User{})
	db.AutoMigrate(&model.Log{})
	if err := db.Create(&model.User{Id: 91, Username: "transport-test", Status: 1}).Error; err != nil { t.Fatal(err) }
	if err := db.Create(&model.Log{UserId: 91, Type: 1, Quota: 1000000}).Error; err != nil { t.Fatal(err) }
	model.OmniMigrate()
	service.InitHttpClient()
	ids := []int{}
	for i, handler := range handlers {
		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		url := server.URL
		id := int(time.Now().UnixNano()%100000000) + i + 1000
		db.Create(&model.Channel{Id: id, Type: 1, Status: 1, Name: "mock", Models: "test-model", Group: "default", Key: "test-key", BaseURL: &url})
		ids = append(ids, id)
	}
	policy := service.OmniRoutePolicy{Enabled: true, ContinuationEnabled: true, FirstSeconds: 1, IdleSeconds: 1, FailureThreshold: 3, CooldownSeconds: 1, Models: map[string][]int{"test-model": ids}}
	data, _ := json.Marshal(policy)
	common.OptionMapRWMutex.Lock()
	old := common.OptionMap["OmniAPIRouting"]
	if common.OptionMap == nil {
		common.OptionMap = map[string]string{}
	}
	common.OptionMap["OmniAPIRouting"] = string(data)
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		model.DB = oldDB
		common.MemoryCacheEnabled = oldCache
		sql.Close()
		common.OptionMapRWMutex.Lock()
		common.OptionMap["OmniAPIRouting"] = old
		common.OptionMapRWMutex.Unlock()
	})
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{UserId: 91, OriginModelName: "test-model", UsingGroup: "default", IsStream: true, RequestURLPath: "/v1/chat/completions", RequestId: fmt.Sprintf("mock-%d", time.Now().UnixNano())}
	return c, info
}
func omniSend(w http.ResponseWriter, id, text string, done bool) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(w, "data: {\"id\":%q,\"created\":1,\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", id, text)
	w.(http.Flusher).Flush()
	if done {
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}
}
func TestOmniHTTPErrorFailoverAndStableID(t *testing.T) {
	c, info := omniHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "retry", 503) }, func(w http.ResponseWriter, r *http.Request) { omniSend(w, "backup", "answer", true) })
	resp, err := (&Adaptor{}).omniRequest(c, info, strings.NewReader(`{"model":"test-model","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil || !strings.Contains(string(data), "[DONE]") {
		t.Fatal(string(data), err)
	}
	if n := len(model.OmniRequestAttempts(info.RequestId)); n != 2 {
		t.Fatal(n)
	}
}
func TestOmniHTTPContinuationPreservesContextAndDeduplicates(t *testing.T) {
	var received atomic.Bool
	c, info := omniHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) { omniSend(w, "main", "already written", false) }, func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]any
		json.NewDecoder(r.Body).Decode(&raw)
		messages, _ := raw["messages"].([]any)
		received.Store(len(messages) == 3)
		omniSend(w, "backup", "already written and new", true)
	})
	resp, err := (&Adaptor{}).omniRequest(c, info, strings.NewReader(`{"model":"test-model","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !received.Load() || strings.Count(s, "already written") != 1 || strings.Contains(s, `"id":"backup"`) || !strings.Contains(s, " and new") {
		t.Fatal(s)
	}
}
func TestOmniHTTPCancellationStopsUpstream(t *testing.T) {
	stopped := make(chan struct{})
	c, info := omniHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
		omniSend(w, "main", "start", false)
		<-r.Context().Done()
		close(stopped)
	})
	ctx, cancel := context.WithCancel(c.Request.Context())
	c.Request = c.Request.WithContext(ctx)
	resp, err := (&Adaptor{}).omniRequest(c, info, strings.NewReader(`{"model":"test-model","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	resp.Body.Close()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream not cancelled")
	}
}
func TestOmniHTTPFirstContentTimeout(t *testing.T) {
	c, info := omniHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": ping\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}, func(w http.ResponseWriter, r *http.Request) { omniSend(w, "backup", "recovered", true) })
	start := time.Now()
	resp, err := (&Adaptor{}).omniRequest(c, info, strings.NewReader(`{"stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil || !strings.Contains(string(data), "recovered") || time.Since(start) < time.Second {
		t.Fatal(string(data), err)
	}
}
func TestOmniHTTPToolsBufferAndRetryWholeTurn(t *testing.T) {
	var backup atomic.Bool
	c, info := omniHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) { omniSend(w, "main", "partial", false) }, func(w http.ResponseWriter, r *http.Request) { backup.Store(true); omniSend(w, "backup", "wrong", true) })
	resp, err := (&Adaptor{}).omniRequest(c, info, strings.NewReader(`{"stream":true,"tools":[],"messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
 if err != nil || !backup.Load() || !strings.Contains(string(data), "[DONE]") || strings.Contains(string(data), "partial") {
		t.Fatal(string(data), err)
	}
}

func TestOmniHTTPUpstreamBadRequestRetries(t *testing.T) {
	var backup atomic.Int32
	c, info := omniHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "bad request", 400) }, func(w http.ResponseWriter, r *http.Request) { backup.Add(1); omniSend(w, "backup", "unexpected", true) })
	resp, err := (&Adaptor{}).omniRequest(c, info, strings.NewReader(`{"model":"test-model","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	if err != nil || backup.Load() != 1 {
		t.Fatal(err, backup.Load())
	}
	resp.Body.Close()
}
func TestOmniHTTPJSONFailover(t *testing.T) {
	c, info := omniHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "retry", 503) }, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"backup","choices":[{"message":{"role":"assistant","content":"answer"}}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`)
	})
	info.IsStream = false
	resp, err := (&Adaptor{}).omniRequest(c, info, strings.NewReader(`{"model":"test-model","messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil || !strings.Contains(string(data), "answer") {
		t.Fatal(string(data), err)
	}
	if n := len(model.OmniRequestAttempts(info.RequestId)); n != 2 {
		t.Fatal(n)
	}
}

func TestOmniHTTPConvertedProtocolRoutes(t *testing.T) {
	for _, format := range []types.RelayFormat{types.RelayFormatClaude, types.RelayFormatGemini} {
		t.Run(string(format), func(t *testing.T) {
			c, info := omniHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "retry", 503) }, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/chat/completions" {
					t.Errorf("unexpected upstream path %s", r.URL.Path)
				}
				omniSend(w, "backup", "answer", true)
			})
			c.Request.URL.Path = "/v1/messages"
			info.RelayFormat = format
			resp, err := (&Adaptor{}).omniRequest(c, info, strings.NewReader(`{"model":"test-model","messages":[{"role":"user","content":"hello"}],"stream":true}`))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			data, err := io.ReadAll(resp.Body)
			if err != nil || !strings.Contains(string(data), "answer") {
				t.Fatal(string(data), err)
			}
		})
	}
}
func TestOmniProtocolErrorsHaveNoCompletionMarker(t *testing.T) {
	for _, format := range []types.RelayFormat{types.RelayFormatClaude, types.RelayFormatGemini} {
		t.Run(string(format), func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
			omniStreamInterrupted(c, &relaycommon.RelayInfo{RelayFormat: format}, "")
			body := rec.Body.String()
			if !strings.Contains(body, "error") || strings.Contains(body, "[DONE]") || strings.Contains(body, "message_stop") {
				t.Fatal(body)
			}
			if format == types.RelayFormatClaude && !strings.Contains(body, "event: error") {
				t.Fatal(body)
			}
		})
	}
}

func TestOmniResponsesFailoverKeepsNativeEvents(t *testing.T) {
	c, info := omniHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "retry", 503) }, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"answer\",\"item_id\":\"original\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"original\",\"status\":\"completed\"}}\n\n")
	})
	c.Request.URL.Path = "/v1/responses"
	info.RequestURLPath = "/v1/responses"
	resp, err := (&Adaptor{}).omniRequest(c, info, strings.NewReader(`{"model":"test-model","input":"hello","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil || !strings.Contains(string(data), "response.completed") || !strings.Contains(string(data), "original") {
		t.Fatal(string(data), err)
	}
}
func TestOmniResponsesInterruptedNeverContinues(t *testing.T) {
	var backup atomic.Int32
	c, info := omniHTTPFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
	}, func(w http.ResponseWriter, r *http.Request) { backup.Add(1) })
	c.Request.URL.Path = "/v1/responses"
	info.RequestURLPath = "/v1/responses"
	resp, err := (&Adaptor{}).omniRequest(c, info, strings.NewReader(`{"model":"test-model","input":"hello","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err == nil || backup.Load() != 0 || !strings.Contains(string(data), "stream_interrupted") || strings.Contains(string(data), "response.completed") {
		t.Fatal(string(data), err, backup.Load())
	}
}
