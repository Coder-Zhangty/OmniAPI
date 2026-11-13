package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/QuantumNous/new-api/model"
	"io"
	"mime/multipart"
	"strconv"
	"strings"
)

// Read the actual supplier wire count. Never bypass the fixed-price risk guard
// merely because an edit is multipart instead of JSON.
func omniRiskFinalImageCount(body []byte) (int, error) {
	if bytes.HasPrefix(bytes.TrimSpace(body), []byte("{")) {
		var final struct {
			N *int `json:"n"`
		}
		if json.Unmarshal(body, &final) != nil {
			return 0, model.ErrOmniRiskQuote
		}
		if final.N == nil {
			return 1, nil
		}
		if *final.N < 1 || *final.N > 128 {
			return 0, model.ErrOmniRiskQuote
		}
		return *final.N, nil
	}
	line, _, ok := bytes.Cut(body, []byte("\r\n"))
	if !ok || !bytes.HasPrefix(line, []byte("--")) || len(line) > 74 {
		return 0, model.ErrOmniRiskQuote
	}
	reader := multipart.NewReader(bytes.NewReader(body), string(line[2:]))
	count := 1
	seen := false
	parts := 0
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, model.ErrOmniRiskQuote
		}
		parts++
		if parts > 32 {
			return 0, model.ErrOmniRiskQuote
		}
		if part.FormName() == "n" {
			if seen {
				return 0, model.ErrOmniRiskQuote
			}
			seen = true
			raw, err := io.ReadAll(io.LimitReader(part, 32))
			if err != nil {
				return 0, model.ErrOmniRiskQuote
			}
			count, err = strconv.Atoi(strings.TrimSpace(string(raw)))
			if err != nil || count < 1 || count > 128 {
				return 0, model.ErrOmniRiskQuote
			}
		}
		if _, err = io.Copy(io.Discard, part); err != nil {
			return 0, model.ErrOmniRiskQuote
		}
		part.Close()
	}
	if parts == 0 {
		return 0, model.ErrOmniRiskQuote
	}
	return count, nil
}
