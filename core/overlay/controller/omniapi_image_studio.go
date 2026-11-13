package controller

import (
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/gin-gonic/gin"
	"strings"
)

// Capability data is live; financial prices and historical snapshots are untouched.
func OmniImageStudioCapabilities(c *gin.Context) {
	registry := model.GetOmniModelRegistry()
	prices := model.GetOmniPrices()
	rows := map[string]any{}
	for name, identity := range registry.Models {
		if identity.Lifecycle == "unavailable" || identity.Lifecycle == "retired" {
			continue
		}
		if _, ok := prices.Models[name]; !ok {
			continue
		}
		options := []map[string]string{}
		seen := map[string]bool{}
		for _, binding := range identity.Bindings {
			if binding.Status != "verified" {
				continue
			}
			for _, capability := range binding.Supports {
				if !strings.HasPrefix(capability, "image_edit_spec:") || seen[capability] {
					continue
				}
				parts := strings.Split(capability, ":")
				if len(parts) != 3 {
					continue
				}
				seen[capability] = true
				options = append(options, map[string]string{"size": parts[1], "quality": parts[2]})
			}
		}
		if len(options) > 0 {
			rows[name] = map[string]any{"edit_options": options, "reference_images": 1}
		}
	}
	polish := ""
	if _, ok := prices.Models["glm-5.2"]; ok {
		polish = "glm-5.2"
	}
	c.Header("Cache-Control", "no-store")
	omniReply(c, map[string]any{"version": registry.Version, "models": rows, "polish_model": polish, "max_input_bytes": dto.OmniImageInputMaxBytes, "max_input_pixels": dto.OmniImageInputMaxPixels, "prompt_max_chars": 32000}, nil)
}
