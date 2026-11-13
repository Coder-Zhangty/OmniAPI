package middleware

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestArchivePlaintextV3CaptureAndFailClosed(t *testing.T) {
	var contents []string
	var previous string
	var mu sync.Mutex
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var value struct {
			Version int                    `json:"version"`
			Meta    map[string]interface{} `json:"meta"`
			Data    string                 `json:"data"`
		}
		if json.Unmarshal(raw, &value) != nil || value.Version != 3 {
			t.Error("not a v3 plaintext event")
		}
		data, err := base64.StdEncoding.DecodeString(value.Data)
		if err != nil {
			t.Error(err)
		}
		sha := sha256.Sum256(raw)
		hash := hex.EncodeToString(sha[:])
		mu.Lock()
		defer mu.Unlock()
		if value.Meta["prev_hash"] != previous {
			t.Error("broken hash chain")
		}
		if value.Meta["user_id"] != float64(42) {
			t.Error("wrong authenticated owner")
		}
		previous = hash
		contents = append(contents, string(data))
		json.NewEncoder(w).Encode(map[string]string{"hash": hash})
	}))
	defer collector.Close()
	t.Setenv("OMNI_ARCHIVE_ENABLED", "true")
	t.Setenv("OMNI_ARCHIVE_FORMAT", "plaintext-v3")
	t.Setenv("OMNI_ARCHIVE_PUBLIC_KEY", "")
	t.Setenv("OMNI_ARCHIVE_COLLECTOR_TOKEN", strings.Repeat("v", 64))
	t.Setenv("OMNI_ARCHIVE_COLLECTOR_URL", collector.URL)
	archiveOnce = sync.Once{}
	archiveSettings = archiveConfig{}
	router := gin.New()
	called := 0
	router.Use(func(c *gin.Context) { c.Set("id", 42) }, OmniArchive())
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		called++
		c.Header("Content-Type", "text/event-stream")
		c.Writer.WriteString("data: 明文合成回答\n\n")
		c.Writer.Flush()
		c.Writer.WriteString("data: [DONE]\n\n")
	})
	request := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"test","api_key":"private-key","messages":[{"role":"user","content":"明文合成问题"}]}`))
	request.Header.Set("Content-Type", "application/json")
	result := httptest.NewRecorder()
	router.ServeHTTP(result, request)
	if result.Code != 200 || !strings.Contains(result.Body.String(), "明文合成回答") {
		t.Fatal("client output changed", result.Code)
	}
	if len(contents) != 5 || !strings.Contains(contents[1], "[REDACTED]") || strings.Contains(contents[1], "private-key") {
		t.Fatal("capture/redaction incomplete")
	}
	collector.Close()
	request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"test"}`))
	request.Header.Set("Content-Type", "application/json")
	result = httptest.NewRecorder()
	router.ServeHTTP(result, request)
	if result.Code != 503 || called != 1 {
		t.Fatal("storage outage must reject before upstream")
	}
}

func TestArchivePlaintextV3PythonCollector(t *testing.T) {
	collector := os.Getenv("ARCHIVE_V3_TEST_COLLECTOR")
	if collector == "" {
		t.Skip("run tests/archive_v3_go.py for the Python collector")
	}
	t.Setenv("OMNI_ARCHIVE_ENABLED", "true")
	t.Setenv("OMNI_ARCHIVE_FORMAT", "plaintext-v3")
	t.Setenv("OMNI_ARCHIVE_COLLECTOR_URL", collector)
	t.Setenv("OMNI_ARCHIVE_COLLECTOR_TOKEN", strings.Repeat("a", 64))
	archiveOnce = sync.Once{}
	archiveSettings = archiveConfig{}
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("id", 812) }, OmniArchive())
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		if c.GetHeader("X-Archive-Project-ID") != "" || c.GetHeader("X-Archive-Conversation-ID") != "" {
			t.Error("grouping headers must not reach upstream")
		}
		c.Header("Content-Type", "text/event-stream")
		c.Writer.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"合成网关回答\"}}]}\n\n")
		c.Writer.Flush()
		c.Writer.WriteString("data: [DONE]\n\n")
	})
	request := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"test","api_key":"never-copy-this","messages":[{"role":"user","content":"合成网关问题"}]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Archive-Project-ID", "gateway-project")
	request.Header.Set("X-Archive-Conversation-ID", "gateway-session")
	result := httptest.NewRecorder()
	router.ServeHTTP(result, request)
	if result.Code != 200 || !strings.Contains(result.Body.String(), "合成网关回答") {
		t.Fatal("Python collector rejected the Go capture", result.Code)
	}
}
