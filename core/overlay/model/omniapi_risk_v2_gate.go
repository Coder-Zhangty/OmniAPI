package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/QuantumNous/new-api/common"
	"io"
	"os"
	"sync"
	"time"
)

var riskBinaryOnce sync.Once
var riskBinaryHash string

// A server-owned release receipt gates activation, never review or restoration.
func OmniRiskActivationReady() error {
	reject := errors.New("risk_activation_not_verified: 发布验收尚未完成，不能开启自动处置")
	path := os.Getenv("OMNI_RISK_ACTIVATION_GATE")
	if path == "" {
		return reject
	}
	data, e := os.ReadFile(path)
	if e != nil || len(data) > 32768 {
		return reject
	}
	var gate struct {
		Passed        bool   `json:"passed"`
		Version       string `json:"version"`
		BinarySHA     string `json:"binary_sha256"`
		IssuedAt      int64  `json:"issued_at"`
		ShadowSeconds int64  `json:"shadow_seconds"`
		P95Millis     int64  `json:"propagation_p95_ms"`
		PeakRPS       int64  `json:"peak_rps"`
		LoadRPS       int64  `json:"load_rps"`
		Tests         bool   `json:"tests_passed"`
		Notices       bool   `json:"notice_verified"`
		Source        bool   `json:"source_verified"`
		Backup        bool   `json:"backup_verified"`
	}
	if json.Unmarshal(data, &gate) != nil {
		return reject
	}
	now := time.Now().Unix()
	if !gate.Passed || gate.Version != common.Version || gate.IssuedAt > now+60 || gate.IssuedAt < now-86400 || gate.ShadowSeconds < 1800 || gate.P95Millis <= 0 || gate.P95Millis >= 2000 || gate.PeakRPS < 1 || gate.LoadRPS < gate.PeakRPS*2 || !gate.Tests || !gate.Notices || !gate.Source || !gate.Backup {
		return reject
	}
	riskBinaryOnce.Do(func() {
		path, e := os.Executable()
		if e != nil {
			return
		}
		if _, e = os.Stat("/proc/self/exe"); e == nil {
			path = "/proc/self/exe"
		}
		f, e := os.Open(path)
		if e != nil {
			return
		}
		defer f.Close()
		hash := sha256.New()
		if _, e = io.Copy(hash, f); e == nil {
			riskBinaryHash = hex.EncodeToString(hash.Sum(nil))
		}
	})
	if riskBinaryHash == "" || riskBinaryHash != gate.BinarySHA {
		return reject
	}
	return nil
}
