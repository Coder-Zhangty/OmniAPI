package middleware

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"gopkg.in/yaml.v3"
)

func TestArchiveCaptureDurableEncryptedAndRedacted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	private, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&private.PublicKey)
	keyfile := filepath.Join(t.TempDir(), "public.pem")
	os.WriteFile(keyfile, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0600)
	t.Setenv("OMNI_ARCHIVE_PUBLIC_KEY", keyfile)
	t.Setenv("OMNI_ARCHIVE_ENABLED", "true")
	t.Setenv("OMNI_ARCHIVE_COLLECTOR_TOKEN", strings.Repeat("a", 64))
	var mu sync.Mutex
	events := []archiveEnvelope{}
	plain := [][]byte{}
	previous := ""
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), "sensitive-key") {
			t.Error("plaintext leaked")
		}
		var envelope archiveEnvelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Error(err)
			return
		}
		decode := func(s string) []byte {
			v, e := base64.StdEncoding.DecodeString(s)
			if e != nil {
				t.Error(e)
			}
			return v
		}
		dek, e := rsa.DecryptOAEP(sha256.New(), rand.Reader, private, decode(envelope.Wrapped), nil)
		if e != nil {
			t.Error(e)
			return
		}
		block, _ := aes.NewCipher(dek)
		gcm, _ := cipher.NewGCM(block)
		clear, e := gcm.Open(nil, decode(envelope.Nonce), decode(envelope.Ciphertext), decode(envelope.AAD))
		if e != nil {
			t.Error(e)
			return
		}
		digest := sha256.Sum256(raw)
		hash := hex.EncodeToString(digest[:])
		mu.Lock()
		if envelope.Meta["prev_hash"] != previous {
			t.Error("chain broken")
		}
		previous = hash
		events = append(events, envelope)
		plain = append(plain, clear)
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]string{"hash": hash})
	}))
	defer collector.Close()
	t.Setenv("OMNI_ARCHIVE_COLLECTOR_URL", collector.URL)
	archiveOnce = sync.Once{}
	archiveSettings = archiveConfig{}
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("id", 42) }, OmniArchive())
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		raw, _ := io.ReadAll(c.Request.Body)
		if !bytes.Contains(raw, []byte("sensitive-key")) {
			t.Error("original request changed")
		}
		c.Header("Content-Type", "text/event-stream")
		c.Writer.WriteString("data: hello\n\n")
		c.Writer.Flush()
		c.Writer.WriteString("data: [DONE]\n\n")
	})
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"test","api_key":"sensitive-key","messages":[{"role":"user","content":"中文测试"}]}`))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != 200 || response.Body.String() != "data: hello\n\ndata: [DONE]\n\n" {
		t.Fatalf("response corrupted: %d %s", response.Code, response.Body.String())
	}
	if len(events) != 5 {
		t.Fatalf("expected 5 encrypted events, got %d", len(events))
	}
	if !bytes.Contains(plain[1], []byte("[REDACTED]")) || bytes.Contains(plain[1], []byte("sensitive-key")) {
		t.Error("input credentials not scrubbed")
	}
	if events[4].Meta["status"] != "completed" || events[0].Meta["user_id"] != float64(42) {
		t.Error("status/identity incorrect")
	}
	// A storage outage refuses a new call; no upstream handler runs.
	collector.Close()
	response = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"test"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(response, req)
	if response.Code != 503 {
		t.Fatalf("must fail closed on local outage, got %d", response.Code)
	}
}

func TestArchivePeerAndMultipart(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/", nil)
	c.Request.RemoteAddr = "192.0.2.4:1234"
	c.Request.Header.Set("X-Real-IP", "198.51.100.2")
	t.Setenv("OMNI_ARCHIVE_TRUSTED_PROXIES", "")
	ip, source := archivePeer(c)
	if ip != "192.0.2.4" || source != "direct_peer" {
		t.Error("untrusted forwarded IP accepted")
	}
	t.Setenv("OMNI_ARCHIVE_TRUSTED_PROXIES", "192.0.2.0/24")
	ip, source = archivePeer(c)
	if ip != "198.51.100.2" || source != "trusted_ingress" {
		t.Error("trusted ingress not recognized")
	}
	body := "--boundary\r\nContent-Disposition: form-data; name=\"password\"\r\n\r\nsecret\r\n--boundary\r\nContent-Disposition: form-data; name=\"file\"; filename=\"test.txt\"\r\nContent-Type: text/plain\r\n\r\nattachment\r\n--boundary--\r\n"
	saved, format, err := archiveInput("multipart/form-data; boundary=boundary", []byte(body))
	if err != nil || format != "multipart-json" || bytes.Contains(saved, []byte("c2VjcmV0")) {
		t.Fatal("multipart credential exclusion failed", err)
	}
	if !bytes.Contains(saved, []byte(base64.StdEncoding.EncodeToString([]byte("attachment")))) {
		t.Error("attachment lost")
	}
}

func TestArchiveWebSocketHijack(t *testing.T) {
	gin.SetMode(gin.TestMode)
	private, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&private.PublicKey)
	path := filepath.Join(t.TempDir(), "public.pem")
	os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0600)
	t.Setenv("OMNI_ARCHIVE_ENABLED", "true")
	t.Setenv("OMNI_ARCHIVE_PUBLIC_KEY", path)
	t.Setenv("OMNI_ARCHIVE_COLLECTOR_TOKEN", strings.Repeat("b", 64))
	var mu sync.Mutex
	var inputs, outputs []byte
	finished := make(chan bool, 1)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var event archiveEnvelope
		json.Unmarshal(raw, &event)
		decode := func(s string) []byte { v, _ := base64.StdEncoding.DecodeString(s); return v }
		dek, e := rsa.DecryptOAEP(sha256.New(), rand.Reader, private, decode(event.Wrapped), nil)
		if e != nil {
			t.Error(e)
			return
		}
		block, _ := aes.NewCipher(dek)
		gcm, _ := cipher.NewGCM(block)
		clear, e := gcm.Open(nil, decode(event.Nonce), decode(event.Ciphertext), decode(event.AAD))
		if e != nil {
			t.Error(e)
			return
		}
		mu.Lock()
		switch event.Meta["kind"] {
		case "input":
			inputs = append(inputs, clear...)
		case "output":
			outputs = append(outputs, clear...)
		case "final":
			finished <- true
		}
		mu.Unlock()
		digest := sha256.Sum256(raw)
		json.NewEncoder(w).Encode(map[string]string{"hash": hex.EncodeToString(digest[:])})
	}))
	defer collector.Close()
	t.Setenv("OMNI_ARCHIVE_COLLECTOR_URL", collector.URL)
	archiveOnce = sync.Once{}
	archiveSettings = archiveConfig{}
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("id", 42) }, OmniArchive())
	router.GET("/v1/responses", func(c *gin.Context) {
		conn, e := (&websocket.Upgrader{}).Upgrade(c.Writer, c.Request, nil)
		if e != nil {
			t.Error(e)
			return
		}
		defer conn.Close()
		kind, message, e := conn.ReadMessage()
		if e != nil {
			t.Error(e)
			return
		}
		if e = conn.WriteMessage(kind, message); e != nil {
			t.Error(e)
		}
	})
	server := httptest.NewServer(router)
	defer server.Close()
	conn, _, e := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
	if e != nil {
		t.Fatal(e)
	}
	conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","input":"synthetic"}`))
	_, message, e := conn.ReadMessage()
	if e != nil {
		t.Fatal(e)
	}
	conn.Close()
	if !bytes.Contains(message, []byte("synthetic")) {
		t.Error("websocket changed")
	}
	<-finished
	mu.Lock()
	defer mu.Unlock()
	if len(inputs) < 2 || !bytes.Contains(outputs, []byte("synthetic")) {
		t.Error("hijacked transport was not archived")
	}
}

func TestArchivePythonCollectorRoundTrip(t *testing.T) {
	origin := os.Getenv("ARCHIVE_TEST_COLLECTOR_URL")
	if origin == "" {
		t.Skip("Run tests/run_archive_go.py for the actual Python collector integration")
	}
	t.Setenv("OMNI_ARCHIVE_ENABLED", "true")
	t.Setenv("OMNI_ARCHIVE_COLLECTOR_URL", origin)
	t.Setenv("OMNI_ARCHIVE_COLLECTOR_TOKEN", os.Getenv("ARCHIVE_TEST_COLLECTOR_TOKEN"))
	t.Setenv("OMNI_ARCHIVE_PUBLIC_KEY", os.Getenv("ARCHIVE_TEST_PUBLIC_KEY"))
	archiveOnce = sync.Once{}
	archiveSettings = archiveConfig{}
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("id", 42) }, OmniArchive())
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		c.JSON(200, gin.H{"choices": []gin.H{{"message": gin.H{"role": "assistant", "content": "跨语言合成回答"}}}})
	})
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"cross-language","messages":[{"role":"user","content":"跨语言合成问题"}]}`))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "跨语言合成回答") {
		t.Fatalf("Python collector rejected Go envelope (status %d)", response.Code)
	}
}

func TestArchiveDeploymentDefaults(t *testing.T) {
	path := os.Getenv("ARCHIVE_TEST_COMPOSE_PATH")
	if path == "" {
		t.Skip("Run tests/run_archive_go.py to verify the deployment overlay")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Services map[string]struct {
			Environment map[string]string `yaml:"environment"`
			Ports       []string          `yaml:"ports"`
		} `yaml:"services"`
	}
	if err = yaml.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	if config.Services["new-api"].Environment["OMNI_ARCHIVE_ENABLED"] != "${OMNI_ARCHIVE_ENABLED:-false}" {
		t.Fatal("capture must be disabled by default")
	}
	ports := config.Services["archive-collector"].Ports
	if len(ports) != 1 || ports[0] != "127.0.0.1:8789:8789" {
		t.Fatal("collector must not be exposed publicly")
	}
}
