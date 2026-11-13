package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type turnstileCheckResponse struct {
	Success  bool     `json:"success"`
	Hostname string   `json:"hostname"`
	Action   string   `json:"action"`
	Errors   []string `json:"error-codes"`
}

var omniTurnstileClient = &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func TurnstileCheck() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !common.TurnstileCheckEnabled {
			c.Next()
			return
		}
		reject := func(reason string) {
			c.Set("risk_auth_turnstile_error", reason)
			c.AbortWithStatusJSON(200, gin.H{"success": false, "code": reason, "message": "人机验证已失效或不匹配，请重新验证后重试。"})
		}
		token := c.Query("turnstile")
		if token == "" || len(token) > 2048 {
			reject("turnstile_missing_or_invalid")
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
		defer cancel()
		values := url.Values{"secret": {common.TurnstileSecretKey}, "response": {token}, "remoteip": {c.ClientIP()}}
		req, e := http.NewRequestWithContext(ctx, "POST", "https://challenges.cloudflare.com/turnstile/v0/siteverify", strings.NewReader(values.Encode()))
		if e != nil {
			reject("turnstile_request_failed")
			return
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		res, e := omniTurnstileClient.Do(req)
		if e != nil {
			if errors.Is(e, context.DeadlineExceeded) || ctx.Err() != nil {
				reject("turnstile_timeout")
			} else {
				reject("turnstile_transport_failed")
			}
			return
		}
		defer res.Body.Close()
		var data turnstileCheckResponse
		if res.StatusCode != 200 || json.NewDecoder(io.LimitReader(res.Body, 8192)).Decode(&data) != nil || !data.Success {
			reason := "turnstile_rejected"
			if res.StatusCode != 200 {
				reason = "turnstile_provider_status"
			}
			for _, code := range data.Errors {
				if code == "timeout-or-duplicate" {
					reason = "turnstile_timeout_or_duplicate"
				}
			}
			reject(reason)
			return
		}
		host := strings.ToLower(strings.Split(c.Request.Host, ":")[0])
		if host != "omniapi.biz" && host != "www.omniapi.biz" && host != "omniapi.hyeroomy.com" {
			reject("turnstile_host_not_allowed")
			return
		}
		if strings.ToLower(data.Hostname) != host {
			reject("turnstile_hostname_mismatch")
			return
		}
		p, e := model.OmniRiskV2Policy()
		if e != nil {
			reject("turnstile_policy_unavailable")
			return
		}
		strict := !p.Shadow && (p.AutoHold || p.NewUserProtection || p.TemporaryIP)
		if (strict && data.Action != "omni_auth") || (data.Action != "" && data.Action != "omni_auth") {
			reject("turnstile_action_mismatch")
			return
		}
		c.Next()
	}
}
