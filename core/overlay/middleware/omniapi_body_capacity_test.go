package middleware

import (
	"bytes"
	"compress/gzip"
	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type omniUnreadBody struct{ reads int }

func (b *omniUnreadBody) Read(p []byte) (int, error) { b.reads++; return 0, io.EOF }
func (b *omniUnreadBody) Close() error               { return nil }

func TestOmniBodyCapacityOversizeStopsBeforeReadOrModel(t *testing.T) {
	for _, path := range []string{"/v1/messages", "/v1/chat/completions"} {
		budget := &omniBodyCapacity{limit: 32 << 20}
		called := false
		r := gin.New()
		r.Use(omniModelBodyCapacityGuard(budget, 32<<20))
		r.POST(path, func(c *gin.Context) { called = true })
		body := &omniUnreadBody{}
		request := httptest.NewRequest("POST", path, nil)
		request.Body = body
		request.ContentLength = (32 << 20) + 1
		response := httptest.NewRecorder()
		r.ServeHTTP(response, request)
		require.Equal(t, http.StatusRequestEntityTooLarge, response.Code)
		require.False(t, called)
		require.Zero(t, body.reads)
		require.Zero(t, budget.used.Load())
		require.Contains(t, response.Body.String(), "request_body_too_large")
	}
}

func TestOmniBodyCapacityLargeRequestsDoNotBlockOrdinarySmallRequests(t *testing.T) {
	budget := &omniBodyCapacity{limit: 2 << 20}
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	done := make(chan struct{})
	r := gin.New()
	r.Use(omniModelBodyCapacityGuard(budget, 32<<20))
	r.POST("/v1/messages", func(c *gin.Context) {
		if c.GetHeader("X-Test-Hold") == "1" {
			entered <- struct{}{}
			<-release
		}
		c.Status(http.StatusNoContent)
	})
	request := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(strings.Repeat("x", 2<<20)))
	request.Header.Set("X-Test-Hold", "1")
	go func() { defer close(done); r.ServeHTTP(httptest.NewRecorder(), request) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("large request did not enter")
	}
	defer func() { close(release); <-done }()
	blocked := httptest.NewRecorder()
	r.ServeHTTP(blocked, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(strings.Repeat("x", 1<<20))))
	require.Equal(t, http.StatusServiceUnavailable, blocked.Code)
	require.Contains(t, blocked.Body.String(), "request_capacity_busy")
	require.Equal(t, "2", blocked.Header().Get("Retry-After"))
	small := httptest.NewRecorder()
	r.ServeHTTP(small, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{}`)))
	require.Equal(t, http.StatusNoContent, small.Code)
	require.EqualValues(t, 2<<20, budget.used.Load())
}

func TestOmniBodyCapacityCompressedLengthCannotBypassDecodedLimit(t *testing.T) {
	var packed bytes.Buffer
	writer := gzip.NewWriter(&packed)
	_, err := writer.Write(bytes.Repeat([]byte("x"), 3<<20))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	budget := &omniBodyCapacity{limit: 2 << 20}
	r := gin.New()
	r.Use(DecompressRequestMiddleware(), omniModelBodyCapacityGuard(budget, 2<<20))
	reachedModel := false
	r.POST("/v1/messages", func(c *gin.Context) {
		require.EqualValues(t, -1, c.Request.ContentLength)
		require.EqualValues(t, 2<<20, budget.used.Load())
		_, err := io.ReadAll(c.Request.Body)
		if common.IsRequestBodyTooLargeError(err) {
			c.Status(http.StatusRequestEntityTooLarge)
			return
		}
		require.NoError(t, err)
		reachedModel = true
	})
	request := httptest.NewRequest("POST", "/v1/messages", bytes.NewReader(packed.Bytes()))
	request.Header.Set("Content-Encoding", "gzip")
	response := httptest.NewRecorder()
	r.ServeHTTP(response, request)
	require.Equal(t, http.StatusRequestEntityTooLarge, response.Code)
	require.False(t, reachedModel)
	require.Zero(t, budget.used.Load())
}

func TestOmniBodyCapacitySlotReleasedOnHandlerPanic(t *testing.T) {
	budget := &omniBodyCapacity{limit: 2 << 20}
	r := gin.New()
	r.Use(omniModelBodyCapacityGuard(budget, 32<<20))
	r.POST("/v1/messages", func(c *gin.Context) { panic("synthetic handler failure") })
	func() {
		defer func() { require.NotNil(t, recover()) }()
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/messages", strings.NewReader(strings.Repeat("x", 1<<20))))
	}()
	require.Zero(t, budget.used.Load())
}
