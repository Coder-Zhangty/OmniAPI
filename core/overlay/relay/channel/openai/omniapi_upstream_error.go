package openai

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Retain only a closed reason vocabulary, never an upstream body or message.
type omniHTTPError struct {
	Status int
	Reason string
}

func (e *omniHTTPError) Error() string {
	return fmt.Sprintf("upstream HTTP %d (%s)", e.Status, e.Reason)
}
func omniReadHTTPError(resp *http.Response) *omniHTTPError {
	e := &omniHTTPError{Status: resp.StatusCode, Reason: "upstream_rejected"}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16384))
	var body struct {
		Code  string `json:"code"`
		Error struct {
			Code    string `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err == nil {
		_ = json.Unmarshal(data, &body)
	}
	code := strings.ToUpper(body.Error.Code)
	if code == "" {
		code = strings.ToUpper(body.Code)
	}
	if resp.StatusCode == 413 || (resp.StatusCode == 400 || resp.StatusCode == 422) &&
		omniUpstreamTokenLimit(code, body.Error.Type, body.Error.Message) {
		e.Reason = "supplier_request_limit"
		return e
	}
	if resp.StatusCode == 400 || resp.StatusCode == 422 {
		message := strings.ReplaceAll(strings.ToLower(body.Error.Message), "`", "")
		if strings.Contains(message, "invalid signature in thinking block") ||
			(strings.Contains(message, "thinking block") && strings.Contains(message, "prefix mismatch")) {
			e.Reason = "supplier_signed_history_rejected"
			return e
		}
	}
	switch code {
	case "BILLING_INSUFFICIENT_CREDITS", "BILLING_INSUFFICIENT_BALANCE", "INSUFFICIENT_BALANCE":
		e.Reason = "supplier_balance_insufficient"
		return e
	case "INVALID_IMAGE", "INVALID_BASE64", "INVALID_ARGUMENT", "INVALID_REQUEST", "INVALID_REQUEST_ERROR", "UNSUPPORTED_IMAGE":
		e.Reason = "request_invalid"
		return e
	}
	switch resp.StatusCode {
	case 401, 403:
		e.Reason = "supplier_auth_rejected"
	case 429:
		e.Reason = "supplier_rate_limited"
	case 413, 415:
		e.Reason = "request_invalid"
	case 422:
		e.Reason = "upstream_422_unclassified"
	default:
		if resp.StatusCode >= 500 {
			e.Reason = "supplier_temporarily_unavailable"
		}
	}
	return e
}

func omniUpstreamTokenLimit(code, kind, message string) bool {
	switch code {
	case "CONTEXT_LENGTH_EXCEEDED", "CONTEXT_WINDOW_EXCEEDED", "INPUT_TOO_LONG", "PROMPT_TOO_LONG",
		"MAX_TOKENS_EXCEEDED", "MAX_OUTPUT_TOKENS_EXCEEDED", "OUTPUT_LIMIT_EXCEEDED":
		return true
	}
	message = strings.ToLower(strings.TrimSpace(message))
	if (kind == "invalid_request_error" || kind == "<nil>" || kind == "") && (strings.HasPrefix(message, "prompt is too long") || strings.HasPrefix(message, "input is too long") ||
		strings.HasPrefix(message, "this model's maximum context length")) {
		return true
	}
	return (strings.Contains(message, "max_tokens") || strings.Contains(message, "max_output_tokens") ||
		strings.Contains(message, "max_completion_tokens")) &&
		(strings.Contains(message, "must be less than") || strings.Contains(message, "must be at most") ||
			strings.Contains(message, "exceeds") || strings.Contains(message, "must be between") ||
			strings.Contains(message, "<=") || strings.Contains(message, "maximum") ||
			strings.Contains(message, "too large") || strings.Contains(message, "cannot exceed"))
}

func omniSupplierRequestLimited(err error) bool {
	var upstream *omniHTTPError
	return errors.As(err, &upstream) && upstream.Reason == "supplier_request_limit"
}

func omniSupplierHistoryRejected(err error) bool {
	var upstream *omniHTTPError
	return errors.As(err, &upstream) && upstream.Reason == "supplier_signed_history_rejected"
}
