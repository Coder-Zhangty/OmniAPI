package middleware

// Capture is opt-in. Only a public RSA key and the local collector credential
// are available here; private keys and remote replication credentials are not.
import (
	"bufio"
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
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type archiveConfig struct {
	plaintext          bool
	public             *rsa.PublicKey
	kid, origin, token string
	client             *http.Client
	err                error
}

var archiveOnce sync.Once
var archiveSettings archiveConfig

func loadArchiveConfig() archiveConfig {
	archiveOnce.Do(func() {
		cfg := &archiveSettings
		cfg.origin = strings.TrimRight(os.Getenv("OMNI_ARCHIVE_COLLECTOR_URL"), "/")
		cfg.token = os.Getenv("OMNI_ARCHIVE_COLLECTOR_TOKEN")
		// A plaintext listener is allowed only on the private container network or
		// loopback. Deployment must not publish the collector port externally.
		if cfg.origin == "" || len(cfg.token) < 32 {
			cfg.err = errors.New("archive configuration missing")
			return
		}
		cfg.client = &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil, MaxIdleConnsPerHost: 32},
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
		cfg.plaintext = os.Getenv("OMNI_ARCHIVE_FORMAT") == "plaintext-v3"
		if cfg.plaintext {
			return // Explicitly selected v3 stores no RSA/AES content envelope.
		}
		data, err := os.ReadFile(os.Getenv("OMNI_ARCHIVE_PUBLIC_KEY"))
		if err != nil {
			cfg.err = err
			return
		}
		block, _ := pem.Decode(data)
		if block == nil {
			cfg.err = errors.New("invalid archive public key")
			return
		}
		key, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			cfg.err = err
			return
		}
		public, ok := key.(*rsa.PublicKey)
		if !ok || public.N.BitLen() != 4096 {
			cfg.err = errors.New("RSA-4096 required")
			return
		}
		digest := sha256.Sum256(block.Bytes)
		cfg.kid = hex.EncodeToString(digest[:])
		cfg.public = public
		cfg.client = &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil, MaxIdleConnsPerHost: 32},
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	})
	return archiveSettings
}

type archiveEnvelope struct {
	Version    int                    `json:"version"`
	KeyID      string                 `json:"key_id"`
	Meta       map[string]interface{} `json:"meta"`
	AAD        string                 `json:"aad"`
	Nonce      string                 `json:"nonce"`
	Wrapped    string                 `json:"wrapped_key"`
	Ciphertext string                 `json:"ciphertext"`
}

type archiveCapture struct {
	cfg             archiveConfig
	meta            map[string]interface{}
	seq             int
	prev            string
	failed          bool
	mu              sync.Mutex
	transportFailed bool
}

func (a *archiveCapture) save(kind string, data []byte, extra map[string]interface{}) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.failed {
		return errors.New("archive unavailable")
	}
	meta := make(map[string]interface{})
	for k, v := range a.meta {
		meta[k] = v
	}
	for k, v := range extra {
		meta[k] = v
	}
	meta["kind"] = kind
	meta["seq"] = a.seq
	meta["prev_hash"] = a.prev
	if a.cfg.plaintext {
		raw, err := json.Marshal(map[string]interface{}{"version": 3, "meta": meta, "data": base64.StdEncoding.EncodeToString(data)})
		if err != nil {
			return err
		}
		digest := sha256.Sum256(raw)
		expected := hex.EncodeToString(digest[:])
		for attempt := 0; attempt < 2; attempt++ {
			req, err := http.NewRequest(http.MethodPost, a.cfg.origin+"/internal/events", bytes.NewReader(raw))
			if err != nil {
				return err
			}
			req.Header.Set("Authorization", "Bearer "+a.cfg.token)
			req.Header.Set("Content-Type", "application/json")
			res, err := a.cfg.client.Do(req)
			if err == nil {
				var ack struct {
					Hash string `json:"hash"`
				}
				err = json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&ack)
				res.Body.Close()
				if err == nil && res.StatusCode == 200 && ack.Hash == expected {
					a.seq++
					a.prev = expected
					return nil
				}
			}
		}
		a.failed = true
		return errors.New("archive v3 persistence failed")
	}
	aad, err := json.Marshal(map[string]interface{}{"version": 1, "key_id": a.cfg.kid, "meta": meta})
	if err != nil {
		return err
	}
	dek := make([]byte, 32)
	nonce := make([]byte, 12)
	if _, err = rand.Read(dek); err != nil {
		return err
	}
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	wrapped, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, a.cfg.public, dek, nil)
	if err != nil {
		return err
	}
	b64 := base64.StdEncoding.EncodeToString
	raw, err := json.Marshal(archiveEnvelope{1, a.cfg.kid, meta, b64(aad), b64(nonce), b64(wrapped), b64(gcm.Seal(nil, nonce, data, aad))})
	if err != nil {
		return err
	}
	digest := sha256.Sum256(raw)
	expected := hex.EncodeToString(digest[:])
	// Retry exactly the same ciphertext: collector insertion is idempotent even
	// when it committed the previous attempt but its acknowledgement was lost.
	for attempt := 0; attempt < 2; attempt++ {
		req, e := http.NewRequest(http.MethodPost, a.cfg.origin+"/internal/events", bytes.NewReader(raw))
		if e != nil {
			return e
		}
		req.Header.Set("Authorization", "Bearer "+a.cfg.token)
		req.Header.Set("Content-Type", "application/json")
		res, e := a.cfg.client.Do(req)
		if e == nil {
			var ack struct {
				Hash string `json:"hash"`
			}
			e = json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&ack)
			res.Body.Close()
			if e == nil && res.StatusCode == 200 && ack.Hash == expected {
				a.seq++
				a.prev = expected
				return nil
			}
		}
	}
	a.failed = true
	return errors.New("archive persistence failed")
}

func (a *archiveCapture) chunks(kind string, data []byte) error {
	for len(data) > 0 {
		n := len(data)
		if n > 65536 {
			n = 65536
		}
		if err := a.save(kind, data[:n], nil); err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

type archiveWriter struct {
	gin.ResponseWriter
	capture *archiveCapture
	err     error
}

func (w *archiveWriter) Write(data []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if err := w.capture.chunks("output", data); err != nil {
		w.err = err
		return 0, err
	}
	n, err := w.ResponseWriter.Write(data)
	if err != nil {
		w.err = err
	}
	return n, err
}
func (w *archiveWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }

// WebSocket libraries use Hijack and then read/write the connection directly.
// Preserve those wire frames (including masking/compression), not just the
// initial HTTP upgrade. Every intercepted read/write is encrypted durably.
type archiveConn struct {
	net.Conn
	reader  *bufio.Reader
	capture *archiveCapture
}

func (c *archiveConn) Read(p []byte) (int, error) {
	n, err := c.reader.Read(p)
	if n > 0 {
		if e := c.capture.chunks("input", p[:n]); e != nil {
			return 0, e
		}
	}
	return n, err
}
func (c *archiveConn) Write(p []byte) (int, error) {
	if err := c.capture.chunks("output", p); err != nil {
		return 0, err
	}
	n, err := c.Conn.Write(p)
	if err != nil {
		c.capture.mu.Lock()
		c.capture.transportFailed = true
		c.capture.mu.Unlock()
	}
	return n, err
}
func (w *archiveWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := w.ResponseWriter.Hijack()
	if err != nil {
		return nil, nil, err
	}
	wrapped := &archiveConn{Conn: conn, reader: rw.Reader, capture: w.capture}
	return wrapped, bufio.NewReadWriter(bufio.NewReader(wrapped), bufio.NewWriter(wrapped)), nil
}

func archiveSensitive(key string) bool {
	switch strings.ToLower(strings.ReplaceAll(key, "-", "_")) {
	case "authorization", "api_key", "apikey", "password", "passwd", "access_token", "refresh_token", "client_secret", "secret", "cookie", "set_cookie":
		return true
	}
	return false
}
func archiveRedact(value interface{}) interface{} {
	switch v := value.(type) {
	case map[string]interface{}:
		for k, item := range v {
			if archiveSensitive(k) {
				v[k] = "[REDACTED]"
			} else {
				v[k] = archiveRedact(item)
			}
		}
	case []interface{}:
		for i := range v {
			v[i] = archiveRedact(v[i])
		}
	}
	return value
}
func archiveInput(contentType string, body []byte) ([]byte, string, error) {
	if len(body) == 0 {
		return nil, "binary", nil
	}
	media, params, _ := mime.ParseMediaType(contentType)
	if media == "application/json" || strings.HasSuffix(media, "+json") {
		var value interface{}
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.UseNumber()
		if err := dec.Decode(&value); err != nil {
			return nil, "", err
		}
		returnValue, err := json.Marshal(archiveRedact(value))
		return returnValue, "json", err
	}
	if media == "multipart/form-data" {
		parts := []map[string]interface{}{}
		r := multipart.NewReader(bytes.NewReader(body), params["boundary"])
		for {
			p, err := r.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, "", err
			}
			data, err := io.ReadAll(p)
			if err != nil {
				return nil, "", err
			}
			if archiveSensitive(p.FormName()) {
				data = []byte("[REDACTED]")
			}
			parts = append(parts, map[string]interface{}{"name": p.FormName(), "filename": p.FileName(), "content_type": p.Header.Get("Content-Type"), "data": base64.StdEncoding.EncodeToString(data)})
		}
		raw, err := json.Marshal(map[string]interface{}{"parts": parts})
		return raw, "multipart-json", err
	}
	return body, "binary", nil
}

func archiveRequestBodyLimit(plaintext bool) int64 {
	if !plaintext {
		return 64 << 20
	}
	// Match the gateway's general request limit. Routes with smaller limits
	// already wrap the body in MaxBytesReader before reaching this middleware.
	maxMB, err := strconv.ParseInt(os.Getenv("MAX_REQUEST_BODY_MB"), 10, 32)
	if err != nil || maxMB <= 0 {
		maxMB = 128
	}
	return maxMB << 20
}

func archivePeer(c *gin.Context) (string, string) {
	peer, _, err := net.SplitHostPort(c.Request.RemoteAddr)
	if err != nil {
		peer = c.Request.RemoteAddr
	}
	// Forwarded headers are accepted only from explicitly configured CIDRs and
	// only the single header that the trusted ingress overwrites.
	for _, cidr := range strings.Split(os.Getenv("OMNI_ARCHIVE_TRUSTED_PROXIES"), ",") {
		_, network, e := net.ParseCIDR(strings.TrimSpace(cidr))
		if e == nil && network.Contains(net.ParseIP(peer)) {
			forwarded := strings.TrimSpace(c.GetHeader("X-Real-IP"))
			if net.ParseIP(forwarded) != nil {
				return forwarded, "trusted_ingress"
			}
		}
	}
	return peer, "direct_peer"
}

// OmniArchive is inserted after identity authentication and before channel
// selection. Hijacked transports are captured at the wire-frame boundary too.
func OmniArchive() gin.HandlerFunc {
	return func(c *gin.Context) {
		if os.Getenv("OMNI_ARCHIVE_ENABLED") != "true" {
			c.Next()
			return
		}
		websocket := strings.EqualFold(c.GetHeader("Upgrade"), "websocket")
		if c.Request.Method == http.MethodGet && !websocket && !strings.Contains(c.Request.URL.Path, "responses") && !strings.Contains(c.Request.URL.Path, "video") && !strings.Contains(c.Request.URL.Path, "task") {
			c.Next()
			return
		}
		cfg := loadArchiveConfig()
		if cfg.err != nil || c.GetInt("id") <= 0 {
			c.AbortWithStatusJSON(503, gin.H{"error": gin.H{"message": "Archive storage unavailable", "type": "archive_unavailable"}})
			return
		}
		var body []byte
		var err error
		limit := archiveRequestBodyLimit(cfg.plaintext)
		if c.Request.Body != nil {
			body, err = io.ReadAll(io.LimitReader(c.Request.Body, limit+1))
			c.Request.Body.Close()
		}
		if err != nil || int64(len(body)) > limit {
			c.AbortWithStatusJSON(413, gin.H{"error": gin.H{"message": "Archive request too large"}})
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		saved, format, err := archiveInput(c.GetHeader("Content-Type"), body)
		if err != nil {
			c.AbortWithStatusJSON(400, gin.H{"error": gin.H{"message": "Invalid archive request body"}})
			return
		}
		idBytes := make([]byte, 16)
		if _, err = rand.Read(idBytes); err != nil {
			c.AbortWithStatus(503)
			return
		}
		aid := hex.EncodeToString(idBytes)
		created := float64(time.Now().UnixMilli()) / 1000
		var input struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(saved, &input)
		model := input.Model
		if len(model) > 256 {
			c.AbortWithStatus(400)
			return
		}
		project, conversation := c.GetHeader("X-Archive-Project-ID"), c.GetHeader("X-Archive-Conversation-ID")
		if len(project) > 128 || len(conversation) > 128 {
			c.AbortWithStatus(400)
			return
		}
		a := &archiveCapture{cfg: cfg, meta: map[string]interface{}{"archive_id": aid, "user_id": c.GetInt("id"), "created": created, "model": model, "project_id": project, "conversation_id": conversation}}
		ip, source := archivePeer(c)
		if websocket {
			format = "websocket-wire"
		}
		detail, _ := json.Marshal(map[string]interface{}{"path": c.Request.URL.Path, "method": c.Request.Method, "ip": ip, "ip_source": source, "content_type": c.GetHeader("Content-Type"), "input_format": format})
		if err = a.save("request", detail, nil); err == nil {
			err = a.chunks("input", saved)
		}
		if err != nil {
			c.AbortWithStatusJSON(503, gin.H{"error": gin.H{"message": "Archive storage unavailable", "type": "archive_unavailable"}})
			return
		}
		// Do not forward archive grouping headers to the upstream provider.
		c.Request.Header.Del("X-Archive-Project-ID")
		c.Request.Header.Del("X-Archive-Conversation-ID")
		c.Set("omni_archive_id", aid)
		original := c.Writer
		writer := &archiveWriter{ResponseWriter: original, capture: a}
		c.Writer = writer
		defer func() {
			c.Writer = original
			a.mu.Lock()
			failed := a.failed || a.transportFailed
			a.mu.Unlock()
			status := "completed"
			if writer.err != nil || failed {
				status = "interrupted"
			} else if c.Request.Context().Err() != nil {
				status = "cancelled"
			} else if writer.Status() >= 400 {
				status = "failed"
			}
			channel, _ := c.Get("channel_id")
			used, _ := c.Get("use_channel")
			mapping, _ := c.Get("model_mapping")
			end, _ := json.Marshal(map[string]interface{}{"http_status": writer.Status(), "content_type": writer.Header().Get("Content-Type"), "channel_id": channel, "used_channels": used, "original_model": c.GetString("original_model"), "upstream_model": c.GetString("omni_archive_upstream_model"), "model_mapping": mapping, "finished": float64(time.Now().UnixMilli()) / 1000, "bytes_written": writer.Size()})
			if e := a.save("final", end, map[string]interface{}{"status": status, "upstream_model": c.GetString("omni_archive_upstream_model")}); e != nil {
				c.Abort()
			}
		}()
		c.Next()
	}
}
