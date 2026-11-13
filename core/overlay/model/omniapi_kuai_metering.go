package model

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The limits are part of the certification, not token corrections.
type OmniKuaiMeteringPolicy struct {
	EvidenceHash                 string `json:"evidence_hash"`
	MaxInputRatioPPM             int64  `json:"max_input_ratio_ppm"`
	MaxInputOverhead             int64  `json:"max_input_overhead_tokens"`
	RequireResponsesInstructions bool   `json:"require_responses_instructions,omitempty"`
	RequireResponsesStream       bool   `json:"require_responses_stream,omitempty"`
}

func (p *OmniKuaiMeteringPolicy) Valid() bool {
	if p == nil || len(p.EvidenceHash) != 64 || p.MaxInputRatioPPM < 1_000_000 || p.MaxInputRatioPPM > 4_000_000 || p.MaxInputOverhead < 0 || p.MaxInputOverhead > 2048 {
		return false
	}
	_, err := hex.DecodeString(p.EvidenceHash)
	return err == nil
}

func OmniKuaiMeteringFor(version, name string, channelID int) *OmniKuaiMeteringPolicy {
	if !strings.HasPrefix(name, "gpt-") {
		return nil
	}
	registry := OmniRegistryAt(version)
	if !registry.Enabled {
		return nil
	}
	for _, binding := range registry.Models[registry.Canonical(name)].Bindings {
		if binding.ChannelID == channelID && binding.Status == "verified" && binding.KuaiMetering.Valid() {
			return binding.KuaiMetering
		}
	}
	return nil
}

// Changing the release alone cannot clear a quarantine. A new certification
// must carry independently reviewed evidence for this exact route.
type OmniKuaiQuarantine struct {
	ChannelID    int    `gorm:"primaryKey;autoIncrement:false"`
	Model        string `gorm:"primaryKey;size:128"`
	Path         string `gorm:"primaryKey;size:64"`
	EvidenceHash string `gorm:"primaryKey;size:64"`
	Reason       string `gorm:"size:64"`
	CreatedAt    int64
}

func (q OmniKuaiQuarantine) Available() bool {
	if DB == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var row OmniKuaiQuarantine
	err := DB.WithContext(ctx).Where("channel_id = ? AND model = ? AND path = ? AND evidence_hash = ?", q.ChannelID, q.Model, q.Path, q.EvidenceHash).Take(&row).Error
	return errors.Is(err, gorm.ErrRecordNotFound)
}

func (q OmniKuaiQuarantine) Record(reason string) error {
	if DB == nil {
		return errors.New("quarantine database unavailable")
	}
	q.Reason, q.CreatedAt = reason, time.Now().Unix()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return DB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&q).Error
}
