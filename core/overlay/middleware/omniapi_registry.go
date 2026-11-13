package middleware

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"strings"
)

func omniNormalizeModel(c *gin.Context, request *ModelRequest) {
	if request == nil {
		return
	}
	snapshotOptions := model.OmniRequestOptionSnapshot()
	c.Set("omni_request_snapshot", snapshotOptions)
	p := model.OmniRegistryFromSnapshot(snapshotOptions["OmniAPIModelRegistry"])
	if !p.Enabled {
		return
	}
	c.Set("omni_registry_version", p.Version)
	if request.Model != "" {
		c.Set("omni_requested_alias", request.Model)
		original := p.Canonical(request.Model)
		request.Model = p.CanonicalForUser(c.GetInt("id"), request.Model)
		if request.Model != original {
			c.Header("X-Omni-Effective-Model", request.Model)
		}
		snapshot := model.OmniPricesFromSnapshot(snapshotOptions["OmniAPIPricing"])
		c.Set("risk_v2_fx", strings.Trim(string(snapshot.RiskFX), "\""))
		if snapshot.Enabled {
			c.Set("omni_price_captured", true)
			c.Set("omni_price_snapshot_version", snapshot.Version)
			data, available := snapshot.Models[request.Model]
			c.Set("omni_price_available", available)
			c.Set("omni_price_quote", string(data))
			var row struct {
				Expression string `json:"billing_expr"`
			}
			if common.Unmarshal(data, &row) == nil {
				c.Set("omni_price_expression", row.Expression)
			}
		}
	}
}

func omniCanonicalLimits(limit map[string]bool, name string) (map[string]bool, string) {
	return model.OmniCanonicalLimit(limit), model.OmniCanonicalModel(name)
}
